package raglit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/agentkit/llm"
)

// Schemaed documents — the ones that are FORMS.
//
// Identity (identity.go) says what a document is in prose, which is the right
// answer for a letter, a survey or a court filing: they are not the same shape
// as each other and nothing useful comes of pretending they are. But a corpus
// usually also holds documents that ARE the same shape every time — receipts,
// work orders, lab reports, bills, evaluations — and for those, prose throws
// away the thing that makes them valuable. A hundred work orders are worth far
// more as a hundred records than as a hundred summaries.
//
// Which fields, and what they are called, is a property of the CORPUS and not
// of raglit. A garage's work order and a hospital's are both work orders and
// share almost nothing. So the vocabulary is per-index, open-ended, and
// authored: a person registers a type, and every document that resolves as one
// is asked to fill out its schema.
//
// The registration is itself a model call — ProposeDocType reads one or more
// GOLD documents a person picked and proposes the schema and the extraction
// prompt. Writing a JSON Schema by hand for a form you have in front of you is
// exactly the work a model is good at, and the gold documents are kept so a
// later revision can be judged against the same examples.
//
// Two rules, and they are the same two identity follows:
//
//   - The extraction is MARKED as generated wherever it is shown, and a person
//     can overrule it. A filled-in form presented as the document's own words is
//     how a confident guess becomes a fact in somebody's records.
//   - Nothing is inferred that the document does not state. A schema is a list
//     of questions, not a list of answers that must exist; a field the document
//     is silent about comes back null.

// DocType is one registered document type of an index.
type DocType struct {
	Name string `json:"name"`
	// Description is one line saying how to RECOGNISE one — it is what the
	// identity call is shown when choosing among the registered types, so it
	// describes the document, not the fields.
	Description string `json:"description,omitempty"`
	// Prompt is the extraction instruction: how to read these fields out of this
	// kind of document, including what its conventions mean. Travels with the
	// schema because a schema alone produces a confidently filled-in form.
	Prompt string `json:"prompt,omitempty"`
	// Schema is a JSON Schema object describing the fields — the `parameters` of
	// the extraction tool call.
	Schema json.RawMessage `json:"schema,omitempty"`
	// Gold is the documents the schema was proposed from, kept so a revision can
	// be judged against the same examples rather than whatever is at hand.
	Gold      []string `json:"gold,omitempty"`
	Model     string   `json:"model,omitempty"`
	CreatedAt int64    `json:"created_at,omitempty"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
}

// Hash fingerprints the parts of a type definition that shape an EXTRACTION:
// the schema, the reading instructions, and the description (which the
// extraction prompt carries as "this document is a X — <description>").
//
// The name is deliberately NOT in it. A rename is a rename; it does not change
// what was asked of the document, and re-extracting a corpus because somebody
// fixed a capital letter is a bill for nothing. The gold paths are out for the
// same reason — they say where the schema came from, not what it asks.
//
// The schema is hashed through a decode/re-encode round trip, so a
// reformatted-but-identical schema hashes the same: Go marshals map keys in
// sorted order, which makes that canonical. A schema that will not decode is
// hashed as its raw bytes, which is the conservative answer — it will simply
// read as changed more often than it is.
func (t DocType) Hash() string {
	schema := string(t.Schema)
	var v any
	if json.Unmarshal(t.Schema, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			schema = string(b)
		}
	}
	return HashHex([]byte(strings.Join([]string{
		strings.TrimSpace(t.Description),
		strings.TrimSpace(t.Prompt),
		schema,
	}, "\x00")))
}

// FieldNames lists the schema's top-level properties, in schema order where the
// JSON preserves it and alphabetically otherwise. For a summary line.
func (t DocType) FieldNames() []string {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(t.Schema, &s) != nil {
		return nil
	}
	out := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NormalizeTypeName is how a type name is compared: lowercased and
// whitespace-collapsed, so "Work Order" and "work order" are one type rather
// than two that each hold half the corpus.
func NormalizeTypeName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// DocTypes lists the index's registered types, by name.
func (s *Store) DocTypes() ([]DocType, error) {
	rows, err := s.db.Query(
		`SELECT name, description, prompt, schema, gold, model, created_at, updated_at
		   FROM doc_types ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocType
	for rows.Next() {
		t, err := scanDocType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanDocType(r rowScanner) (DocType, error) {
	var t DocType
	var schema, gold string
	if err := r.Scan(&t.Name, &t.Description, &t.Prompt, &schema, &gold,
		&t.Model, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return DocType{}, err
	}
	t.Schema = json.RawMessage(schema)
	if err := json.Unmarshal([]byte(gold), &t.Gold); err != nil {
		t.Gold = nil
	}
	return t, nil
}

// DocTypeByName returns one registered type. An unregistered name is an error,
// not a zero value: extracting against a type nobody defined would produce a
// record whose shape is whatever the model felt like.
func (s *Store) DocTypeByName(name string) (DocType, error) {
	row := s.db.QueryRow(
		`SELECT name, description, prompt, schema, gold, model, created_at, updated_at
		   FROM doc_types WHERE name = ?`, NormalizeTypeName(name))
	t, err := scanDocType(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DocType{}, fmt.Errorf("raglit: no document type %q in this index", name)
	}
	return t, err
}

// SetDocType registers or replaces a type.
func (s *Store) SetDocType(t DocType) error {
	t.Name = NormalizeTypeName(t.Name)
	if t.Name == "" {
		return fmt.Errorf("raglit: a document type needs a name")
	}
	if err := ValidateFieldSchema(t.Schema); err != nil {
		return err
	}
	gold, err := json.Marshal(t.Gold)
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()
	if t.CreatedAt == 0 {
		t.CreatedAt = now
	}
	_, err = s.db.Exec(
		`INSERT INTO doc_types(name, description, prompt, schema, gold, model, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(name) DO UPDATE SET
		   description = excluded.description,
		   prompt      = excluded.prompt,
		   schema      = excluded.schema,
		   gold        = excluded.gold,
		   model       = excluded.model,
		   updated_at  = excluded.updated_at`,
		t.Name, t.Description, t.Prompt, string(t.Schema), string(gold), t.Model, t.CreatedAt, now)
	return err
}

// DeleteDocType removes a type. The extractions made under it are LEFT ALONE:
// they are what documents said, and deleting a type is a statement about the
// index's vocabulary, not a retraction of everything read through it.
func (s *Store) DeleteDocType(name string) error {
	_, err := s.db.Exec(`DELETE FROM doc_types WHERE name = ?`, NormalizeTypeName(name))
	return err
}

// ValidateFieldSchema refuses a schema that is not a JSON Schema OBJECT with
// properties. The extraction call turns it into a tool's `parameters`, and an
// endpoint handed a malformed one fails in a way that reads as a model problem.
func ValidateFieldSchema(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("raglit: a document type needs a field schema")
	}
	var s struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("raglit: the field schema is not JSON: %w", err)
	}
	if s.Type != "" && s.Type != "object" {
		return fmt.Errorf("raglit: a field schema must be an object schema, got %q", s.Type)
	}
	if len(s.Properties) == 0 {
		return fmt.Errorf("raglit: the field schema declares no properties")
	}
	return nil
}

// DocTypeRefs is the type list as the identity call is shown it: the names it
// may choose from, and one line each on how to recognise one.
func (s *Store) DocTypeRefs() ([]DocType, error) {
	types, err := s.DocTypes()
	if err != nil {
		return nil, err
	}
	for i := range types {
		types[i].Prompt, types[i].Schema, types[i].Gold = "", nil, nil
	}
	return types, nil
}

// Proposing a type from gold documents.

// proposeTypeToolDef is the schema the PROPOSAL comes back in: a description, an
// extraction prompt, and the field schema itself.
func proposeTypeToolDef() llm.ToolDef {
	var td llm.ToolDef
	td.Type = "function"
	td.Function.Name = "emit_doc_type"
	td.Function.Description = "Emit a document type: how to recognise one, how to read it, and its fields."
	td.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{"type": "string"},
			"prompt":      map[string]any{"type": "string"},
			"schema":      map[string]any{"type": "object"},
		},
		"required": []string{"description", "prompt", "schema"},
	}
	return td
}

const proposeTypePrompt = `You are defining a DOCUMENT TYPE for a records index, from example
documents a person picked as representative of it. Output ONLY a JSON object:
{"description":"...","prompt":"...","schema":{...}}

- "description": ONE line saying how to RECOGNISE a document of this type —
  what it looks like, what it always contains. This is shown to a model
  choosing among several types, so it must distinguish this one from its
  neighbours. Describe the document, not its fields.
- "prompt": the instruction for reading the fields OUT of one of these. Say
  where on the document each awkward field is found, what this type's
  conventions and abbreviations mean, and which fields are commonly absent.
  Write what somebody who has read these documents knows and a stranger does
  not. Do not restate the schema.
- "schema": a JSON Schema object for the fields. Rules:
    - "type" is "object" with a "properties" map. Every property declares a
      "type" and a "description" saying what it is in THIS kind of document.
    - Prefer flat fields. Use an array of objects only for a genuine repeating
      section (line items on an invoice, results in a panel).
    - Name fields as this document names them, in lowercase snake_case.
    - Use "string" for identifiers, reference numbers and dates — a work order
      number is not a number, and losing its leading zero corrupts it. Use
      "number" only for quantities and amounts that are arithmetic.
    - "required" names ONLY the fields that appear on EVERY example. A field
      the document may be silent about must not be required.
    - Do NOT invent fields the examples do not have, and do not add fields that
      belong to the index rather than the document (no path, no filename, no
      ingest date).

Fields, not prose: this schema exists so a hundred of these documents can be
read as a hundred records. If a document's substance is genuinely prose, say so
in the description and keep the schema to the few fields that are structured.`

// ProposeDocType reads gold documents and proposes the type: how to recognise
// one, how to read it, and its fields.
//
// The authoring step, and the reason document types are practical at all.
// Writing a JSON Schema by hand for a form you are looking at is exactly the
// work a model does well, and doing it from SEVERAL examples is what stops the
// schema being a transcription of one document's quirks.
//
// The proposal is returned, not stored — a schema nobody read before it started
// filling in records is a schema nobody will trust afterwards.
func (s *Store) ProposeDocType(ctx context.Context, name string, goldPaths []string) (DocType, error) {
	if s.identifier == nil {
		return DocType{}, ErrNoIdentifier
	}
	name = NormalizeTypeName(name)
	if name == "" {
		return DocType{}, fmt.Errorf("raglit: a document type needs a name")
	}
	if len(goldPaths) == 0 {
		return DocType{}, fmt.Errorf("raglit: propose a type from at least one example document")
	}
	var b strings.Builder
	b.WriteString(proposeTypePrompt)
	b.WriteString(HintBlock(s.IndexHint()))
	fmt.Fprintf(&b, "\n\nTHE TYPE IS CALLED: %s\n", name)
	for i, p := range goldPaths {
		text, err := s.IdentityText(ctx, p)
		if err != nil {
			return DocType{}, fmt.Errorf("raglit: gold document %s: %w", p, err)
		}
		if n := contentChars(text); n < identityMinTextChars {
			return DocType{}, &ErrIdentityTooShort{Chars: n}
		}
		fmt.Fprintf(&b, "\nEXAMPLE %d:\n%s\n", i+1, identityExcerpt(text))
	}

	var out DocType
	id := &Identifier{
		Client: s.identifier.Client, Model: s.identifier.Model,
		MaxRetries: s.identifier.MaxRetries,
		validator:  newValidator(proposeTypeToolDef()),
		maxTokens:  proposeMaxTokens,
	}
	err := id.ask(ctx, b.String(), "emit_doc_type",
		`{"description":"...","prompt":"...","schema":{...}}`,
		func(js string) error {
			var got struct {
				Description string          `json:"description"`
				Prompt      string          `json:"prompt"`
				Schema      json.RawMessage `json:"schema"`
			}
			if err := json.Unmarshal([]byte(js), &got); err != nil {
				return fmt.Errorf("unparseable: %v", err)
			}
			if err := ValidateFieldSchema(got.Schema); err != nil {
				return err
			}
			if strings.TrimSpace(got.Description) == "" {
				return fmt.Errorf("\"description\" must say how to recognise one of these")
			}
			out = DocType{
				Name: name, Description: strings.TrimSpace(got.Description),
				Prompt: strings.TrimSpace(got.Prompt), Schema: got.Schema,
				Gold: append([]string(nil), goldPaths...), Model: id.Model,
			}
			return nil
		})
	if err != nil {
		return DocType{}, fmt.Errorf("propose doc type: %w", err)
	}
	return out, nil
}
