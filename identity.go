package raglit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iodesystems/agentkit/agent"
	"github.com/iodesystems/agentkit/llm"
)

// Document identity — what a document IS, as opposed to what its file is called.
//
// A corpus is named by whatever produced its files: a scanner, a mail client, a
// download. Measured on a 406-document index, the names are not merely
// uninformative — one of them named a DIFFERENT document. A 30-page purchase and
// sale agreement at the centre of a live dispute was stored as
// "24053 North Northlea - Form 22J -Lead-Based Paint (1).pdf", and nothing in the
// index could have surfaced it: the title was the filename, the search text was
// the body, and no query for "purchase and sale agreement" ranks a file called
// "Lead-Based Paint". It was found by a person opening files one by one.
//
// So at the end of a read — the point where the whole transcript exists in one
// place and has cost nothing extra to obtain — the model is asked for three
// things: a caption a person would use, a few sentences on what the document
// covers, and a kind from a closed vocabulary. They are stored on `documents`
// and indexed as ONE fragment marked origin='identity', which is what fixes
// discovery: the summary says the words the body never says in a form BM25 can
// rank.
//
// Three rules hold this to a machine claim rather than a fact:
//
//   - The FILE IS NEVER RENAMED. The path is what fragments, page images, region
//     trees, readings, verdicts, the audit trail and every citation already
//     written into a legal packet join on. This is a display name and a search
//     target, nothing else.
//   - The generated text is MARKED, in the column and in the text itself, and
//     every path that reassembles a document filters it out. A hit on a summary
//     is a hit on a paraphrase.
//   - A person can overrule it, and their caption is not regenerated
//     (gen_source='person'). A generated caption presented as fact is how
//     "Lead-Based Paint" happens again in the other direction.

// DocIdentity is what a document is, in one record: the caption, the summary,
// the kind, and who said so.
type DocIdentity struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Kind    string `json:"kind"`
	// Source is 'machine' (a model read the transcript) or 'person' (someone
	// corrected it). A person's identity is never overwritten by a re-run.
	Source string `json:"source,omitempty"`
	Model  string `json:"model,omitempty"`
	At     int64  `json:"at,omitempty"` // unix nanos
}

// Empty reports whether nothing has been established about this document.
func (d DocIdentity) Empty() bool {
	return strings.TrimSpace(d.Name) == "" && strings.TrimSpace(d.Summary) == ""
}

// ByPerson reports whether a person, not a model, is the author of this caption.
func (d DocIdentity) ByPerson() bool { return d.Source == IdentityByPerson }

const (
	// IdentityByMachine / IdentityByPerson are gen_source's two values.
	IdentityByMachine = "machine"
	IdentityByPerson  = "person"

	// fragOriginIdentity marks the one fragment per document that holds the
	// generated caption + summary. See the schema note on fragments.origin.
	fragOriginIdentity = "identity"
)

// identityKinds is the CLOSED vocabulary for gen_kind.
//
// Closed because an open one produces forty spellings of "letter", and a kind
// nobody can group by is a kind nobody can filter on. Settled here, before
// anything writes it, rather than by whatever the first model happened to
// return.
//
// "other" is the escape hatch and is meant to stay rare: a corpus where it is
// common is a corpus whose vocabulary is wrong, and the fix is to add a term
// here — deliberately, once — not to let the model invent one per document.
var identityKinds = []string{
	"deed",
	"survey",
	"agreement",
	"correspondence",
	"court filing",
	"certification",
	"analysis",
	"other",
}

// identityKindAliases maps what a model actually returns onto the vocabulary.
// Everything is lowercased and trimmed before lookup.
var identityKindAliases = map[string]string{
	"letter":             "correspondence",
	"letters":            "correspondence",
	"email":              "correspondence",
	"e-mail":             "correspondence",
	"memo":               "correspondence",
	"memorandum":         "correspondence",
	"notice":             "correspondence",
	"contract":           "agreement",
	"lease":              "agreement",
	"purchase agreement": "agreement",
	"addendum":           "agreement",
	"amendment":          "agreement",
	"plat":               "survey",
	"map":                "survey",
	"record of survey":   "survey",
	"title":              "deed",
	"conveyance":         "deed",
	"easement":           "deed",
	"pleading":           "court filing",
	"motion":             "court filing",
	"complaint":          "court filing",
	"petition":           "court filing",
	"declaration":        "court filing",
	"order":              "court filing",
	"filing":             "court filing",
	"certificate":        "certification",
	"affidavit":          "certification",
	"permit":             "certification",
	"report":             "analysis",
	"assessment":         "analysis",
	"inspection":         "analysis",
	"study":              "analysis",
}

// NormalizeKind maps a model's answer onto identityKinds, returning ok=false
// when it is not a term this vocabulary knows — which is the caller's cue to ask
// again with the list, rather than to store a fortieth spelling.
func NormalizeKind(s string) (string, bool) {
	k := strings.ToLower(strings.TrimSpace(s))
	k = strings.Trim(k, ".")
	if k == "" {
		return "", false
	}
	for _, want := range identityKinds {
		if k == want {
			return want, true
		}
	}
	if alias, ok := identityKindAliases[k]; ok {
		return alias, true
	}
	return "", false
}

// IdentityKinds is the vocabulary, for a help string or a UI filter.
func IdentityKinds() []string { return append([]string(nil), identityKinds...) }

// Identifier asks a model what a document is. One call per document, on the
// assembled transcript rather than per page — cheap next to the OCR that
// produced the transcript.
type Identifier struct {
	Client Chatter
	// Model is recorded on the document, so a caption can be told from one a
	// different model wrote (and from a person's).
	Model      string
	MaxRetries int // JSON fix-loop attempts after the first try (default 2)

	validator *agent.SchemaValidator
}

// NewIdentifier builds an Identifier over a chat client (an *llm.Client).
func NewIdentifier(c Chatter, model string) *Identifier {
	return &Identifier{
		Client:     c,
		Model:      model,
		MaxRetries: 2,
		validator:  agent.NewSchemaValidator([]llm.ToolDef{identityToolDef()}),
	}
}

// identityToolDef is the schema the answer is validated against.
func identityToolDef() llm.ToolDef {
	var td llm.ToolDef
	td.Type = "function"
	td.Function.Name = "emit_identity"
	td.Function.Description = "Emit what this document is: a caption, a summary, and its kind."
	td.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    map[string]any{"type": "string"},
			"summary": map[string]any{"type": "string"},
			"kind":    map[string]any{"type": "string", "enum": identityKinds},
		},
		"required": []string{"name", "summary", "kind"},
	}
	return td
}

const (
	// identityHeadChars / identityTailChars bound what one identity call sends.
	//
	// An instrument says what it is at the top — the caption, the parties, the
	// date, the recording stamp — and how it ends matters for a document that is
	// several things stapled together (a permit followed by an escrow letter).
	// The middle is where the length is and adds least, so a long document sends
	// its head and its tail with the gap marked rather than paying to send
	// thirty pages of legal description.
	identityHeadChars = 12000
	identityTailChars = 4000

	// identityMaxTokens caps the answer. Unlike transcription this is not a
	// re-emission of the input — it is three short fields — so the cap is a
	// constant, and anything past it is not a caption any more.
	identityMaxTokens = 800

	// identityMaxNameChars is the longest caption worth storing. A name is a
	// caption for a list, not an abstract; past this the model has written a
	// second summary into the name field.
	identityMaxNameChars = 200
	// identityMaxSummaryChars bounds the summary at roughly a long paragraph.
	identityMaxSummaryChars = 2500
	// identityMinSummaryChars rejects "A legal document." — an answer that
	// distinguishes nothing is worse than none, because it looks like work.
	identityMinSummaryChars = 40
	// identityMinTextChars is the shortest transcript worth captioning. Below it
	// there is nothing to read and the model would be inventing.
	identityMinTextChars = 200
)

// ErrIdentityTooShort reports a document with too little text to identify.
type ErrIdentityTooShort struct{ Chars int }

func (e *ErrIdentityTooShort) Error() string {
	return fmt.Sprintf("no transcript to read (%d characters) — a caption is downstream of the text; re-read the document first (raglit reread, or ingest --fresh)", e.Chars)
}

// Identify reads a document's assembled text and returns what it is.
//
// The FILENAME IS DELIBERATELY NOT PASSED. This exists because filenames lie,
// and a model given "Lead-Based Paint (1).pdf" alongside a purchase and sale
// agreement will hedge toward the name it was handed — which reproduces exactly
// the failure the caption is supposed to catch. The document says what it is.
//
// Returns an error rather than a fallback. A document with no caption is a
// document a person can still find by its filename; a document with a WRONG
// caption is one whose list entry now lies with a machine's confidence. There is
// no degraded answer worth storing here.
func (id *Identifier) Identify(ctx context.Context, text string) (DocIdentity, error) {
	if n := contentChars(text); n < identityMinTextChars {
		return DocIdentity{}, &ErrIdentityTooShort{Chars: n}
	}
	msgs := []llm.Message{{Role: "user", Parts: []llm.ContentPart{
		llm.TextPart(identityPrompt + "\n\nDOCUMENT:\n" + identityExcerpt(text)),
	}}}
	opts := &llm.ChatOpts{MaxTokens: identityMaxTokens}
	var lastErr error
	for attempt := 0; attempt <= id.MaxRetries; attempt++ {
		out, rep, err := collectStream(ctx, id.Client, msgs, opts)
		if err != nil {
			return DocIdentity{}, err
		}
		if rep != nil {
			// Three short fields cannot legitimately collapse into repetition, so
			// the answer is junk. Say what happened and change the sampler, the
			// same way the segmenter does — at temperature 0 an unchanged retry
			// reproduces the loop token for token.
			lastErr = fmt.Errorf("you %s", rep)
			opts = loopBreakSampling(opts)
			msgs = append(msgs,
				llm.Message{Role: "assistant", Content: excerptForRetry(out)},
				llm.Message{Role: "user", Content: fmt.Sprintf(
					"Your answer was cut off: %v. Answer once, briefly, and output ONLY the JSON object %s.",
					lastErr, identityJSONShape)})
			continue
		}
		js := extractJSON(out)
		if lastErr = id.validator.ValidateArgs("emit_identity", js); lastErr == nil {
			var got DocIdentity
			if err := json.Unmarshal([]byte(js), &got); err != nil {
				lastErr = fmt.Errorf("unparseable: %v", err)
			} else if got, lastErr = validateIdentity(got); lastErr == nil {
				got.Source = IdentityByMachine
				got.Model = id.Model
				got.At = time.Now().UnixNano()
				return got, nil
			}
		}
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: excerptForRetry(out)},
			llm.Message{Role: "user", Content: fmt.Sprintf(
				"That was not valid: %v. Output ONLY the JSON object %s.", lastErr, identityJSONShape)})
	}
	return DocIdentity{}, fmt.Errorf("identity: %w", lastErr)
}

// identityJSONShape is the shape quoted back at the model on a retry.
const identityJSONShape = `{"name":"...","summary":"...","kind":"..."}`

// identityPrompt asks for the three fields. It names the vocabulary inline
// because a model cannot pick from a list it was not shown, and the fix loop
// should be for genuine failures rather than for a term nobody mentioned.
var identityPrompt = `You are cataloguing a document for a legal/records index. Read it and
output ONLY a JSON object:
` + identityJSONShape + `

- "name": what a person filing this would call it — the instrument, its date,
  and the parties or property it concerns. A caption for a list, not a filename
  and not a sentence. Example: "2021-05-25 Form 21 purchase and sale agreement,
  executed (Ardley/Brannock)". Use the document's own dates and names; if it does
  not state one, leave it out rather than guessing.
- "summary": a few sentences — what the instrument IS, who the parties are, the
  date, the property or matter it concerns, and what it does. Enough that someone
  reading only this can tell whether they need the document.
- "kind": exactly one of: ` + strings.Join(identityKinds, ", ") + `.
  Use "other" only when none of the rest fit.

Describe only what the document says. Do not infer a purpose it does not state,
and do not carry over an assumption from how the document is titled.`

// identityExcerpt bounds one call's input: the head, and the tail when the
// document is long enough that they differ, with the gap MARKED so the model
// does not read across a cut as if it were continuous prose.
func identityExcerpt(text string) string {
	if len(text) <= identityHeadChars+identityTailChars {
		return text
	}
	head := text[:cutAtBoundary(text, identityHeadChars)]
	tailStart := len(text) - identityTailChars
	if r := cutAtBoundary(text[tailStart:], 200); r > 0 {
		tailStart += r
	}
	return head + "\n\n[…the middle of this document is not shown…]\n\n" + text[tailStart:]
}

// validateIdentity enforces what the schema cannot: a caption that is a caption,
// a summary that says something, and a kind from the vocabulary. The returned
// error is phrased as an instruction, because it goes straight back to the model.
func validateIdentity(d DocIdentity) (DocIdentity, error) {
	d.Name = strings.Join(strings.Fields(strings.TrimSpace(d.Name)), " ")
	d.Summary = strings.TrimSpace(d.Summary)
	if d.Name == "" {
		return d, fmt.Errorf("\"name\" was empty")
	}
	if len(d.Name) > identityMaxNameChars {
		return d, fmt.Errorf("\"name\" is %d characters — it is a caption for a list, keep it under %d",
			len(d.Name), identityMaxNameChars)
	}
	if len(d.Summary) < identityMinSummaryChars {
		return d, fmt.Errorf("\"summary\" says nothing that distinguishes this document from another; state the instrument, the parties, the date and what it does")
	}
	if len(d.Summary) > identityMaxSummaryChars {
		return d, fmt.Errorf("\"summary\" is %d characters — keep it under %d",
			len(d.Summary), identityMaxSummaryChars)
	}
	kind, ok := NormalizeKind(d.Kind)
	if !ok {
		return d, fmt.Errorf("\"kind\" must be exactly one of: %s", strings.Join(identityKinds, ", "))
	}
	d.Kind = kind
	return d, nil
}

// DocumentIdentity returns what is recorded about a document. A document with
// no caption yet returns the zero value and no error — that is a state, not a
// failure. An unknown path IS an error.
func (s *Store) DocumentIdentity(path string) (DocIdentity, error) {
	var d DocIdentity
	err := s.db.QueryRow(
		`SELECT gen_name, gen_summary, gen_kind, gen_source, gen_model, gen_at
		   FROM documents WHERE path = ?`, path).
		Scan(&d.Name, &d.Summary, &d.Kind, &d.Source, &d.Model, &d.At)
	if errors.Is(err, sql.ErrNoRows) {
		return DocIdentity{}, fmt.Errorf("raglit: no document with path %q", path)
	}
	return d, err
}

// SetDocumentIdentity records what a document is and re-indexes its identity
// fragment, in one transaction — so the columns and the searchable text can
// never disagree.
//
// A person's identity (Source 'person') is what a re-run must not touch; that
// rule lives in the CALLER, because this is also how a correction is written.
func (s *Store) SetDocumentIdentity(ctx context.Context, path string, d DocIdentity) error {
	// The lookup is OUTSIDE the transaction on purpose. A transaction that reads
	// and then writes takes a read snapshot first, and upgrading it fails with
	// SQLITE_BUSY the instant another writer has committed — a failure
	// busy_timeout cannot wait out. Resolving the id first leaves a transaction
	// that only writes, which waits its turn like any other writer.
	//
	// Safe against a document deleted in between: the UPDATE and INSERT below
	// then affect nothing and touch no other document, because a row id is never
	// reused for a different path within a live index.
	var docID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM documents WHERE path = ?`, path).Scan(&docID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("raglit: no document with path %q", path)
		}
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := writeIdentity(ctx, tx, docID, d); err != nil {
		return err
	}
	return tx.Commit()
}

// writeIdentity is the identity half of a commit: the columns, then the one
// fragment that makes them searchable. Shared by SetDocumentIdentity and
// commitDoc so an ingest and a correction produce the identical row.
//
// Raw SQL, following the precedent TruePages set — see its comment: regenerating
// the sqlc layer with the installed toolchain corrupts the SQL text of every
// existing query, so new columns pay for themselves in raw SQL rather than in
// sixty broken ones.
func writeIdentity(ctx context.Context, tx dbExecer, docID int64, d DocIdentity) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE documents SET gen_name=?, gen_summary=?, gen_kind=?, gen_source=?, gen_model=?, gen_at=?
		  WHERE id=?`,
		d.Name, d.Summary, d.Kind, d.Source, d.Model, d.At, docID); err != nil {
		return fmt.Errorf("raglit: set identity: %w", err)
	}
	// One identity fragment per document, replaced rather than accumulated.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM fragments WHERE doc_id=? AND origin=?`, docID, fragOriginIdentity); err != nil {
		return err
	}
	text := identityFragmentText(d)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// Page 0, which is "no page": this text is not ON a page of the document,
	// and citing it as if it were would be the same lie the origin column exists
	// to prevent.
	//
	// NOT embedded, deliberately, even on an --embed index. Whether a paraphrase
	// belongs in the same vector space as the document's own words is an open
	// question to settle by measuring on a real corpus; lexically it is
	// unambiguously right, and that is the half being shipped. A summary with no
	// vector is invisible to VecSearch and ranks normally in BM25.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO fragments(doc_id, page, ord, text, start_off, end_off, page_spans, origin)
		 VALUES(?, 0, 0, ?, 0, 0, '', ?)`, docID, text, fragOriginIdentity); err != nil {
		return fmt.Errorf("raglit: index identity: %w", err)
	}
	return nil
}

// dbExecer is the sliver of *sql.Tx / *sql.DB that writeIdentity needs.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// IdentityText is the text a caption is written from: the reading IN FORCE for
// each page, not the machine's first attempt at it.
//
// The distinction is the whole of why this function exists. Fragments hold what
// OCR produced, and they are deliberately NOT rewritten when a person corrects a
// page — re-fragmenting would move every offset and invalidate every citation
// already taken. So a correction lives in `page_readings` as the active reading,
// and anything that wants what the document SAYS has to go there.
//
// Captioning is exactly such a consumer, and the case that proves it is the one
// this was found on: a disputed record of survey whose corrected page fixed the
// surveyor's name and certificate number. Those are the facts a caption states.
// Written from fragments, the caption would have asserted, in a machine's voice
// and at the top of a document list, the very reading a person had already ruled
// wrong.
//
// Pages with no ruling fall back to the indexed text, so a corpus nobody has
// corrected — the overwhelming majority — is unaffected and pays one indexed
// lookup.
func (s *Store) IdentityText(ctx context.Context, path string) (string, error) {
	active, err := s.ActiveReadings(ctx, path)
	if err != nil {
		return "", err
	}
	if len(active) == 0 {
		c, err := s.DocText(path, 0, 0, 0)
		if err != nil {
			return "", err
		}
		return c.Text, nil
	}
	// Page grain, honouring page_spans — a reading is per page, so the text it
	// replaces has to be identified per page too. DocText's grouping attributes a
	// stitched fragment wholly to the page it opened on, which would overlay the
	// correction onto the wrong page's worth of text.
	pages, err := s.TruePages(path)
	if err != nil {
		return "", err
	}
	if len(pages) == 0 {
		c, err := s.DocText(path, 0, 0, 0)
		if err != nil {
			return "", err
		}
		return c.Text, nil
	}
	parts := make([]string, 0, len(pages))
	for _, p := range pages {
		if r, ok := active[p.Page]; ok && strings.TrimSpace(r.Text) != "" {
			parts = append(parts, r.Text)
			continue
		}
		parts = append(parts, p.Text)
	}
	return strings.Join(parts, pageSep), nil
}

// Errors IdentifyDocument returns for the two states that are not failures.
var (
	// ErrNoIdentifier means this store was never given a model to caption with.
	ErrNoIdentifier = errors.New("raglit: no identity model configured")
	// ErrIdentityKept means the document already has an identity that this run
	// must not replace — a person's, or a machine's without --force.
	ErrIdentityKept = errors.New("raglit: identity already recorded")
)

// IdentifyDocument captions one already-indexed document: read its indexed text,
// ask the model what it is, store it. This is the re-runnable half — a corpus of
// 406 documents is not going to be re-OCR'd to get captions, and a caption is
// worth having on documents indexed long before this existed.
//
// force replaces a MACHINE caption. It does not replace a PERSON's: someone who
// corrected a caption did so because the model's was wrong, and re-running the
// same model on the same text produces the same wrong answer. Changing a
// person's caption is done by recording another one.
func (s *Store) IdentifyDocument(ctx context.Context, path string, force bool) (DocIdentity, error) {
	cur, err := s.DocumentIdentity(path)
	if err != nil {
		return DocIdentity{}, err
	}
	if cur.ByPerson() || (!cur.Empty() && !force) {
		return cur, ErrIdentityKept
	}
	if s.identifier == nil {
		return DocIdentity{}, ErrNoIdentifier
	}
	// The document's own words — origin='' excludes the last caption of it — and
	// the reading in force where a person has corrected one. See IdentityText.
	text, err := s.IdentityText(ctx, path)
	if err != nil {
		return DocIdentity{}, err
	}
	id, err := s.identifier.Identify(ctx, text)
	if err != nil {
		return DocIdentity{}, err
	}
	if err := s.SetDocumentIdentity(ctx, path, id); err != nil {
		return DocIdentity{}, err
	}
	return id, nil
}

// RecordIdentity is a PERSON saying what a document is. It supersedes whatever
// the machine said and is not regenerated afterwards.
//
// Fields left empty keep what is already recorded, so correcting only the name
// does not silently blank the summary. by is recorded in Model's place — the
// column names who produced the claim, and for a person that is the person.
func (s *Store) RecordIdentity(ctx context.Context, path string, d DocIdentity, by string) (DocIdentity, error) {
	cur, err := s.DocumentIdentity(path)
	if err != nil {
		return DocIdentity{}, err
	}
	out := DocIdentity{
		Name:    firstNonBlank(d.Name, cur.Name),
		Summary: firstNonBlank(d.Summary, cur.Summary),
		Kind:    firstNonBlank(d.Kind, cur.Kind),
		Source:  IdentityByPerson,
		Model:   strings.TrimSpace(by),
		At:      time.Now().UnixNano(),
	}
	if strings.TrimSpace(out.Name) == "" {
		return DocIdentity{}, fmt.Errorf("raglit: an identity needs a name")
	}
	if out.Kind != "" {
		kind, ok := NormalizeKind(out.Kind)
		if !ok {
			return DocIdentity{}, fmt.Errorf("raglit: kind %q is not one of: %s",
				out.Kind, strings.Join(identityKinds, ", "))
		}
		out.Kind = kind
	}
	if err := s.SetDocumentIdentity(ctx, path, out); err != nil {
		return DocIdentity{}, err
	}
	return out, nil
}

func firstNonBlank(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// IdentityStatus is one document's identity, for a listing.
type IdentityStatus struct {
	Path string `json:"path"`
	DocIdentity
}

// DocumentsMissingIdentity lists the paths of indexed documents with no caption
// yet, oldest first — the work list for `raglit identify`. A corpus indexed
// before identity existed is entirely this list.
func (s *Store) DocumentsMissingIdentity() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT path FROM documents WHERE TRIM(gen_name) = '' ORDER BY added_at`)
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

// Identities lists every document's identity, newest first, including the ones
// with none — a corpus's captioning coverage is exactly as interesting as the
// captions.
func (s *Store) Identities() ([]IdentityStatus, error) {
	rows, err := s.db.Query(
		`SELECT path, gen_name, gen_summary, gen_kind, gen_source, gen_model, gen_at
		   FROM documents ORDER BY added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityStatus
	for rows.Next() {
		var st IdentityStatus
		if err := rows.Scan(&st.Path, &st.Name, &st.Summary, &st.Kind, &st.Source, &st.Model, &st.At); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// identityFragmentText is what gets INDEXED for an identity — the caption, the
// kind and the summary as one searchable unit.
//
// It leads with a line saying what it is. The origin column is how code tells a
// paraphrase from the document, but text travels: a fragment reaches a search
// result, a context window, a person's clipboard. Whatever carries it, it should
// arrive saying that a machine wrote it.
func identityFragmentText(d DocIdentity) string {
	if d.Empty() {
		return ""
	}
	who := "generated by " + d.Model
	if d.ByPerson() {
		who = "recorded by a person"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[what this document is — a description, %s; NOT the document's own words]\n", who)
	if d.Name != "" {
		b.WriteString("NAME: " + d.Name + "\n")
	}
	if d.Kind != "" {
		b.WriteString("KIND: " + d.Kind + "\n")
	}
	if d.Summary != "" {
		b.WriteString("\n" + d.Summary + "\n")
	}
	return b.String()
}
