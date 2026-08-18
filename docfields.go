package raglit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/agentkit/llm"
)

// Filling out a document type's schema — see doctype.go for why types exist.
//
// One row per document, replaced rather than accumulated, and written into the
// index as ONE fragment marked origin='fields'. The fragment is what makes an
// extraction worth having: a query for a work-order number, a part code or a
// patient identifier ranks the document that carries it, and none of those
// strings appear anywhere the ordinary transcript indexes them usefully — they
// sit in a box on a form, surrounded by boilerplate that every other document
// of the type also has.
//
// Marked, like the identity summary, for the same reason: a filled-in form is a
// model's reading of a document, and an agent quoting a field as the document's
// own words is quoting a paraphrase of a box on a page.

// DocFields is what a document's type schema says, filled out.
type DocFields struct {
	Type   string          `json:"type"`
	Fields json.RawMessage `json:"fields"`
	// Source is 'machine' or 'person'. A person's extraction is never
	// regenerated over — the same rule a person's caption follows.
	Source   string `json:"source,omitempty"`
	Model    string `json:"model,omitempty"`
	At       int64  `json:"at,omitempty"`
	TextHash string `json:"text_hash,omitempty"`
}

// Empty reports whether nothing has been extracted for this document.
func (f DocFields) Empty() bool {
	return strings.TrimSpace(f.Type) == "" || len(f.Fields) == 0
}

// ByPerson reports whether a person wrote these fields.
func (f DocFields) ByPerson() bool { return f.Source == IdentityByPerson }

// fragOriginFields marks the one fragment per document that holds the filled-out
// schema. See the schema note on fragments.origin.
const fragOriginFields = "fields"

// DocumentFields returns what has been extracted for a document. A document
// with no extraction returns the zero value and no error — that is a state, not
// a failure. An unknown path IS an error.
func (s *Store) DocumentFields(path string) (DocFields, error) {
	var f DocFields
	var fields string
	err := s.db.QueryRow(
		`SELECT f.type, f.fields, f.source, f.model, f.at, f.text_hash
		   FROM doc_fields f JOIN documents d ON d.id = f.doc_id
		  WHERE d.path = ?`, path).
		Scan(&f.Type, &fields, &f.Source, &f.Model, &f.At, &f.TextHash)
	if errors.Is(err, sql.ErrNoRows) {
		// Distinguish "no extraction" from "no such document".
		var n int
		if e := s.db.QueryRow(`SELECT COUNT(*) FROM documents WHERE path = ?`, path).Scan(&n); e != nil {
			return DocFields{}, e
		}
		if n == 0 {
			return DocFields{}, fmt.Errorf("raglit: no document with path %q", path)
		}
		return DocFields{}, nil
	}
	if err != nil {
		return DocFields{}, err
	}
	f.Fields = json.RawMessage(fields)
	return f, nil
}

// SetDocumentFields records an extraction and re-indexes its fragment, in one
// transaction — so the row and the searchable text can never disagree.
//
// A person's extraction is what a re-run must not touch; that rule lives in the
// CALLER, because this is also how a correction is written.
func (s *Store) SetDocumentFields(ctx context.Context, path string, f DocFields) error {
	var docID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM documents WHERE path = ?`, path).Scan(&docID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("raglit: no document with path %q", path)
		}
		return err
	}
	if len(f.Fields) == 0 {
		f.Fields = json.RawMessage("{}")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO doc_fields(doc_id, type, fields, source, model, at, text_hash)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(doc_id) DO UPDATE SET
		   type=excluded.type, fields=excluded.fields, source=excluded.source,
		   model=excluded.model, at=excluded.at, text_hash=excluded.text_hash`,
		docID, f.Type, string(f.Fields), f.Source, f.Model, f.At, f.TextHash); err != nil {
		return fmt.Errorf("raglit: set fields: %w", err)
	}
	// The document's recorded type follows the extraction, so a document that
	// resolved as one type and was then extracted as another does not read as
	// both.
	if _, err := tx.ExecContext(ctx,
		`UPDATE documents SET gen_doc_type=? WHERE id=?`, f.Type, docID); err != nil {
		return err
	}
	if err := replaceFieldsFragment(ctx, tx, docID, f); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceFieldsFragment writes the one searchable fragment per document,
// replaced rather than accumulated.
func replaceFieldsFragment(ctx context.Context, tx dbExecer, docID int64, f DocFields) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM fragments WHERE doc_id=? AND origin=?`, docID, fragOriginFields); err != nil {
		return err
	}
	text := fieldsFragmentText(f)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO fragments(doc_id, page, ord, text, origin) VALUES(?,0,0,?,?)`,
		docID, text, fragOriginFields)
	return err
}

// fieldsFragmentText flattens an extraction into the text BM25 ranks.
//
// "label: value" lines rather than the JSON: a lexical index over `{"po_number":
// "4471"}` ranks the punctuation and the word "number" alongside the value
// somebody is actually searching for. Nested objects and arrays are flattened
// with their path, so a line item's part code is findable without the reader
// having to know the schema.
func fieldsFragmentText(f DocFields) string {
	var b strings.Builder
	b.WriteString("TYPE: " + f.Type + "\n\n")
	var v any
	if json.Unmarshal(f.Fields, &v) != nil {
		return b.String()
	}
	lines := flattenFields("", v)
	sort.Strings(lines)
	b.WriteString(strings.Join(lines, "\n"))
	return b.String()
}

// flattenFields renders a decoded JSON value as "path: value" lines, skipping
// nulls — an absent field is not a fact and must not be indexed as one.
func flattenFields(prefix string, v any) []string {
	switch t := v.(type) {
	case map[string]any:
		var out []string
		for k, sub := range t {
			out = append(out, flattenFields(joinFieldPath(prefix, k), sub)...)
		}
		return out
	case []any:
		var out []string
		for i, sub := range t {
			out = append(out, flattenFields(joinFieldPath(prefix, strconv.Itoa(i+1)), sub)...)
		}
		return out
	case nil:
		return nil
	case bool:
		return []string{prefix + ": " + strconv.FormatBool(t)}
	case float64:
		return []string{prefix + ": " + strconv.FormatFloat(t, 'f', -1, 64)}
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{prefix + ": " + t}
	default:
		return []string{fmt.Sprintf("%s: %v", prefix, t)}
	}
}

func joinFieldPath(prefix, key string) string {
	key = strings.ReplaceAll(key, "_", " ")
	if prefix == "" {
		return key
	}
	return prefix + " " + key
}

// The extraction call.

// extractToolName is the tool the schema is presented as.
const extractToolName = "emit_fields"

// extractPrompt is the standing part of the extraction instruction. The type's
// own prompt goes after it, because that is the part that knows the documents.
const extractPrompt = `Read the document below and fill out the fields described by the schema.
Output ONLY a JSON object matching the schema.

- Take every value from what the document SAYS. Do not compute a field the
  document does not state, do not carry one over from what a document of this
  type usually says, and do not infer one from the document's title.
- A field the document is silent about is null. A form with a box left blank is
  a document that did not state it, and a plausible value there is worse than
  nothing — it is a guess that will be read as a record.
- Copy identifiers, reference numbers and dates EXACTLY as printed, including
  leading zeros, letter prefixes and punctuation. Do not normalise them.
- Where the document is genuinely ambiguous or unreadable at a field, leave it
  null rather than choosing.`

// ExtractFields fills out a document's type schema and stores the result.
//
// Returns ErrIdentityKept when an extraction already exists (unless forced), and
// when a person wrote it — a person's extraction is never regenerated, forced or
// not.
func (s *Store) ExtractFields(ctx context.Context, path string, force bool) (DocFields, error) {
	cur, err := s.DocumentFields(path)
	if err != nil {
		return DocFields{}, err
	}
	if cur.ByPerson() || (!cur.Empty() && !force) {
		return cur, ErrIdentityKept
	}
	id, err := s.DocumentIdentity(path)
	if err != nil {
		return DocFields{}, err
	}
	if strings.TrimSpace(id.DocType) == "" {
		return DocFields{}, ErrNoDocType
	}
	t, err := s.DocTypeByName(id.DocType)
	if err != nil {
		return DocFields{}, err
	}
	if s.identifier == nil {
		return DocFields{}, ErrNoIdentifier
	}
	text, err := s.IdentityText(ctx, path)
	if err != nil {
		return DocFields{}, err
	}
	got, err := s.identifier.ExtractFields(ctx, text, t, s.IndexHint())
	if err != nil {
		return DocFields{}, err
	}
	got.TextHash, _ = s.documentTextHash(ctx, path)
	if err := s.SetDocumentFields(ctx, path, got); err != nil {
		return DocFields{}, err
	}
	return got, nil
}

// ErrNoDocType is a document that has not resolved as any registered type.
// Not a failure: most documents in most corpora are not forms.
var ErrNoDocType = errors.New("raglit: this document is not one of the index's document types")

// ExtractFields asks the model to fill out one type's schema.
//
// The type's schema becomes the tool's parameters, so the SAME fix loop that
// holds identity to its shape holds an extraction to the corpus owner's — a
// schema authored months ago is enforced by the machinery that was already
// here, rather than by a second, worse copy of it.
func (id *Identifier) ExtractFields(ctx context.Context, text string, t DocType, hint string) (DocFields, error) {
	if n := contentChars(text); n < identityMinTextChars {
		return DocFields{}, &ErrIdentityTooShort{Chars: n}
	}
	if err := ValidateFieldSchema(t.Schema); err != nil {
		return DocFields{}, err
	}
	var params map[string]any
	if err := json.Unmarshal(t.Schema, &params); err != nil {
		return DocFields{}, fmt.Errorf("raglit: field schema: %w", err)
	}
	var td llm.ToolDef
	td.Type = "function"
	td.Function.Name = extractToolName
	td.Function.Description = "Emit the fields of this " + t.Name + "."
	td.Function.Parameters = params

	var b strings.Builder
	b.WriteString(extractPrompt)
	b.WriteString(HintBlock(hint))
	b.WriteString("\n\nTHIS DOCUMENT IS A " + strings.ToUpper(t.Name) + ".")
	if t.Description != "" {
		b.WriteString(" " + t.Description)
	}
	if strings.TrimSpace(t.Prompt) != "" {
		b.WriteString("\n\nHOW TO READ ONE:\n" + strings.TrimSpace(t.Prompt))
	}
	b.WriteString("\n\nSCHEMA:\n" + string(t.Schema))
	b.WriteString("\n\nDOCUMENT:\n" + identityExcerpt(text))

	sub := &Identifier{
		Client: id.Client, Model: id.Model, MaxRetries: id.MaxRetries,
		validator: newValidator(td),
	}
	var out DocFields
	err := sub.ask(ctx, b.String(), extractToolName, schemaShapeHint(t.Schema), func(js string) error {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(js), &probe); err != nil {
			return fmt.Errorf("unparseable: %v", err)
		}
		// A model handed a schema will sometimes answer with the schema. An
		// extraction that is a copy of its own instructions is not a record, and
		// it validates cleanly, so it has to be caught here.
		if _, echoed := probe["properties"]; echoed {
			return fmt.Errorf("that is the schema, not its values — emit the fields as read from the document")
		}
		out = DocFields{Type: t.Name, Fields: json.RawMessage(js)}
		return nil
	})
	if err != nil {
		return DocFields{}, fmt.Errorf("extract %s: %w", t.Name, err)
	}
	out.Source = IdentityByMachine
	out.Model = id.Model
	out.At = time.Now().UnixNano()
	return out, nil
}

// schemaShapeHint is the shape quoted back at the model on a retry: its own
// field names with elided values, which is shorter than the schema and says the
// same thing about what was wanted.
func schemaShapeHint(raw json.RawMessage) string {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &s) != nil || len(s.Properties) == 0 {
		return "{...}"
	}
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, `"`+k+`":...`)
	}
	sort.Strings(names)
	return "{" + strings.Join(names, ",") + "}"
}

// RecordFields is a PERSON's extraction: what they say the fields are. Never
// regenerated over.
func (s *Store) RecordFields(ctx context.Context, path string, f DocFields, by string) (DocFields, error) {
	cur, err := s.DocumentFields(path)
	if err != nil {
		return DocFields{}, err
	}
	// The type falls back to what the DOCUMENT resolved as, not only to a prior
	// extraction: a person recording fields for the first time should not have
	// to restate a type the identity already established.
	resolved := ""
	if id, ierr := s.DocumentIdentity(path); ierr == nil {
		resolved = id.DocType
	}
	out := DocFields{
		Type:     firstNonBlank(f.Type, cur.Type, resolved),
		Fields:   f.Fields,
		Source:   IdentityByPerson,
		Model:    strings.TrimSpace(by),
		At:       time.Now().UnixNano(),
		TextHash: cur.TextHash,
	}
	if strings.TrimSpace(out.Type) == "" {
		return DocFields{}, fmt.Errorf("raglit: an extraction needs a document type")
	}
	if len(out.Fields) == 0 {
		return DocFields{}, fmt.Errorf("raglit: an extraction needs fields")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(out.Fields, &probe); err != nil {
		return DocFields{}, fmt.Errorf("raglit: fields must be a JSON object: %w", err)
	}
	if err := s.SetDocumentFields(ctx, path, out); err != nil {
		return DocFields{}, err
	}
	return out, nil
}

// DocumentsMissingFields lists documents that resolved as a registered type but
// have no extraction yet — the work list for `raglit fields`.
func (s *Store) DocumentsMissingFields() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT d.path FROM documents d
		   LEFT JOIN doc_fields f ON f.doc_id = d.id
		  WHERE TRIM(d.gen_doc_type) <> '' AND f.doc_id IS NULL
		  ORDER BY d.added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FieldsCoverage counts, per registered type, how many documents resolved as it
// and how many have been extracted. What `raglit fields --list` reports.
type FieldsCoverage struct {
	Type      string `json:"type"`
	Resolved  int    `json:"resolved"`
	Extracted int    `json:"extracted"`
}

// FieldsCoverage reports extraction coverage per type, most-resolved first.
func (s *Store) FieldsCoverage() ([]FieldsCoverage, error) {
	rows, err := s.db.Query(
		`SELECT d.gen_doc_type, COUNT(*), COUNT(f.doc_id)
		   FROM documents d LEFT JOIN doc_fields f ON f.doc_id = d.id
		  WHERE TRIM(d.gen_doc_type) <> ''
		  GROUP BY d.gen_doc_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FieldsCoverage
	for rows.Next() {
		var c FieldsCoverage
		if err := rows.Scan(&c.Type, &c.Resolved, &c.Extracted); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resolved != out[j].Resolved {
			return out[i].Resolved > out[j].Resolved
		}
		return out[i].Type < out[j].Type
	})
	return out, nil
}
