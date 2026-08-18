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
	// ContentTags are 3–5 short noun phrases describing what the document is
	// ABOUT (subject matter, entities, procedures). Open vocabulary but
	// constrained in shape; see validateIdentity.
	ContentTags []string `json:"content_tags,omitempty"`
	// RoleTags are 1–3 terms from identityRoleKinds describing what job the
	// document does in the corpus (documentation, reference, overview…).
	// Closed vocabulary so they stay groupable and filterable.
	RoleTags []string `json:"role_tags,omitempty"`
	// DocType is which of the INDEX's registered document types this resolved
	// as, empty for none. Its vocabulary is per-index and authored (doctype.go),
	// unlike Kind, which is raglit's and closed. A document that resolves as one
	// is asked to fill out that type's schema; most documents resolve as none,
	// and that is the normal case rather than a failure.
	DocType string `json:"doc_type,omitempty"`
	// Source is 'machine' (a model read the transcript) or 'person' (someone
	// corrected it). A person's identity is never overwritten by a re-run.
	Source string `json:"source,omitempty"`
	Model  string `json:"model,omitempty"`
	At     int64  `json:"at,omitempty"` // unix nanos
	// TextHash fingerprints the text this caption was written from, so a
	// transcript that changes underneath it can be detected. See
	// documents.gen_text_hash.
	TextHash string `json:"text_hash,omitempty"`
}

// IdentityTextHash fingerprints the text a caption is written from. Content
// only — the same words in the same order hash the same however they were
// fragmented, so a re-fragmentation that changes no words does not read as a
// changed document.
func IdentityTextHash(text string) string {
	return HashHex([]byte(strings.Join(strings.Fields(text), " ")))
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
	// notes — a document somebody MADE about the matter rather than one that was
	// filed in it: a timeline, a witness list, a call transcript, a packet
	// assembled for counsel. Added deliberately, once, because "other" was 9% of
	// a real corpus after the junk was removed, and a working file is not an
	// absence of a kind — it is a kind the vocabulary did not have.
	"notes",
	// commercial — a record of work done or goods sold: an invoice, a work
	// order, a receipt, a property listing. Added for the same reason as notes,
	// from the same corpus: these are neither filed instruments nor
	// correspondence nor anybody's analysis, and they were landing in "other"
	// with nowhere else to go.
	"commercial",
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
	"timeline":           "notes",
	"transcript":         "notes",
	"call transcript":    "notes",
	"summary":            "notes",
	"worklist":           "notes",
	"checklist":          "notes",
	"packet":             "notes",
	"witness list":       "notes",
	"working document":   "notes",
	"work product":       "notes",
	"log":                "notes",
	"invoice":            "commercial",
	"receipt":            "commercial",
	"bill":               "commercial",
	"statement":          "commercial",
	"work order":         "commercial",
	"estimate":           "commercial",
	"quote":              "commercial",
	"listing":            "commercial",
	"mls listing":        "commercial",
	"advertisement":      "commercial",
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

// identityRoleKinds is the CLOSED vocabulary for role tags: what job a document
// does in the corpus. Closed for the same reason kind is — an open one produces
// forty spellings of "documentation", and a role nobody can group by is a role
// nobody can filter on. A document gets 1–3 of these (see validateIdentity).
var identityRoleKinds = []string{
	"documentation", // explains how something works; meant to be read
	"reference",     // looked up for a fact, not read through
	"overview",      // orienting summary of a larger body of work
	"specification", // prescribes what something should be or do
	"guide",         // step-by-step instructions for doing something
	"changelog",     // records what changed and when
	"notes",         // working notes, scratch, intermediate thinking
	"report",        // findings from an investigation or measurement
	"data",          // tabular or structured data, not prose about it
	"other",         // none of the above genuinely fits
}

// identityRoleAliases maps common model spellings onto the vocabulary.
var identityRoleAliases = map[string]string{
	"doc":           "documentation",
	"docs":          "documentation",
	"manual":        "documentation",
	"handbook":      "documentation",
	"wiki":          "documentation",
	"readme":        "documentation",
	"ref":           "reference",
	"lookup":        "reference",
	"cheat sheet":   "reference",
	"overview doc":  "overview",
	"summary":       "overview",
	"introduction":  "overview",
	"intro":         "overview",
	"spec":          "specification",
	"requirements":  "specification",
	"tutorial":      "guide",
	"how-to":        "guide",
	"howto":         "guide",
	"walkthrough":   "guide",
	"changelog":     "changelog",
	"release notes": "changelog",
	"scratch":       "notes",
	"draft":         "notes",
	"meeting notes": "notes",
	"minutes":       "notes",
	"csv":           "data",
	"spreadsheet":   "data",
	"dataset":       "data",
}

// NormalizeRole maps a model's role tag onto identityRoleKinds, returning
// ok=false when it is not a term this vocabulary knows.
func NormalizeRole(s string) (string, bool) {
	r := strings.ToLower(strings.TrimSpace(s))
	r = strings.Trim(r, ".")
	if r == "" {
		return "", false
	}
	for _, want := range identityRoleKinds {
		if r == want {
			return want, true
		}
	}
	if alias, ok := identityRoleAliases[r]; ok {
		return alias, true
	}
	return "", false
}

// IdentityRoleKinds is the vocabulary, for a help string or a UI filter.
func IdentityRoleKinds() []string { return append([]string(nil), identityRoleKinds...) }

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

// newValidator builds a validator over one tool def — the per-call form, for an
// ask whose schema is not known until the call (a document type's fields, a
// type proposal, an identity whose doc_type enum is this index's types).
func newValidator(tds ...llm.ToolDef) *agent.SchemaValidator {
	return agent.NewSchemaValidator(tds)
}

// NewIdentifier builds an Identifier over a chat client (an *llm.Client).
func NewIdentifier(c Chatter, model string) *Identifier {
	return &Identifier{
		Client:     c,
		Model:      model,
		MaxRetries: 2,
		validator:  agent.NewSchemaValidator([]llm.ToolDef{identityToolDef(), identityTagsToolDef()}),
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
			"name":         map[string]any{"type": "string"},
			"summary":      map[string]any{"type": "string"},
			"kind":         map[string]any{"type": "string", "enum": identityKinds},
			"content_tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 3, "maxItems": 5},
			"role_tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": identityRoleKinds}, "minItems": 1, "maxItems": 3},
		},
		"required": []string{"name", "summary", "kind", "content_tags", "role_tags"},
	}
	return td
}

// identityTagsToolDef is the schema for the TAGS-ONLY ask (IdentifyTags). The
// same two fields, without the caption a backfill must not touch.
func identityTagsToolDef() llm.ToolDef {
	var td llm.ToolDef
	td.Type = "function"
	td.Function.Name = "emit_tags"
	td.Function.Description = "Emit what this document is about, and what job it does in the corpus."
	td.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content_tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 3, "maxItems": 5},
			"role_tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": identityRoleKinds}, "minItems": 1, "maxItems": 3},
		},
		"required": []string{"content_tags", "role_tags"},
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
// tagContext is the index's established tag vocabulary — see Store.TagContext.
// It is a PARAMETER rather than a field because one *Identifier is shared by
// every index in a registry and by every slot of the captioning queue: a field
// would be a data race, and would carry one index's vocabulary into the next.
func (id *Identifier) Identify(ctx context.Context, ask IdentityAsk) (DocIdentity, error) {
	if n := contentChars(ask.Text); n < identityMinTextChars {
		return DocIdentity{}, &ErrIdentityTooShort{Chars: n}
	}
	names := typeNames(ask.DocTypes)
	validator := id.validator
	if len(names) > 0 {
		// The doc_type enum is THIS index's vocabulary, so the schema is not
		// known until the call. Built per ask rather than per Identifier for the
		// same reason the tag context is a parameter: one *Identifier serves
		// every index in a registry.
		validator = newValidator(identityToolDefWithTypes(names))
	}
	var got DocIdentity
	err := id.askWith(ctx, validator, ask.prompt(), "emit_identity", identityJSONShape,
		func(js string) error {
			var d DocIdentity
			if err := json.Unmarshal([]byte(js), &d); err != nil {
				return fmt.Errorf("unparseable: %v", err)
			}
			d, err := validateIdentity(d)
			if err != nil {
				return err
			}
			if d.DocType, err = normalizeDocType(d.DocType, names); err != nil {
				return err
			}
			got = d
			return nil
		})
	if err != nil {
		return DocIdentity{}, fmt.Errorf("identity: %w", err)
	}
	got.Source = IdentityByMachine
	got.Model = id.Model
	got.At = time.Now().UnixNano()
	return got, nil
}

// IdentityAsk is everything one identity call needs to know that is not the
// prompt: the document, and the three things about the INDEX that shape the
// answer.
//
// A struct rather than four parameters, and a parameter rather than fields on
// the Identifier, because one *Identifier is shared by every index in a
// registry and by every slot of the captioning queue — state there is a data
// race and carries one index's context into the next.
type IdentityAsk struct {
	// Text is the document's own words, as indexed.
	Text string
	// TagContext is the index's established tag vocabulary (Store.TagContext),
	// so new tags align with terms the corpus already uses.
	TagContext string
	// IndexHint is what the corpus owner says about reading this collection
	// (Store.IndexHint).
	IndexHint string
	// DocTypes are the index's registered document types, which the answer may
	// choose one of. Empty → the doc_type field is not asked for at all, which
	// is right for an index that has registered none.
	DocTypes []DocType
}

func (a IdentityAsk) prompt() string {
	p := identityPrompt + docTypeBlock(a.DocTypes) + tagContextBlock(a.TagContext) +
		HintBlock(a.IndexHint)
	return p + "\n\nDOCUMENT:\n" + identityExcerpt(a.Text)
}

func typeNames(types []DocType) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		if n := NormalizeTypeName(t.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// identityToolDefWithTypes is the identity schema with this index's document
// types as the doc_type enum. Not required — most documents are not forms, and
// a required enum with no "none" member is an instruction to pick one anyway.
func identityToolDefWithTypes(names []string) llm.ToolDef {
	td := identityToolDef()
	params, _ := td.Function.Parameters.(map[string]any)
	props, _ := params["properties"].(map[string]any)
	props["doc_type"] = map[string]any{
		"type": "string",
		"enum": append(append([]string{}, names...), ""),
	}
	return td
}

// docTypeBlock names the registered types for the prompt, with the one line
// each that says how to recognise one.
func docTypeBlock(types []DocType) string {
	if len(types) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n- \"doc_type\": if this document is one of the following, name it" +
		" EXACTLY as written here. If it is not clearly one of them, use \"\" —" +
		" most documents are not, and a wrong type produces a filled-in form of" +
		" guesses:\n")
	for _, t := range types {
		fmt.Fprintf(&b, "    %s", NormalizeTypeName(t.Name))
		if d := strings.TrimSpace(t.Description); d != "" {
			b.WriteString(" — " + oneLineTag(d))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// normalizeDocType holds the answer to the index's registered names. An
// unregistered one is REFUSED rather than recorded: it would resolve to no
// schema, so a document carrying it is a document that claims a type nothing
// can extract.
func normalizeDocType(got string, names []string) (string, error) {
	got = NormalizeTypeName(got)
	if got == "" || len(names) == 0 {
		return "", nil
	}
	for _, n := range names {
		if got == n {
			return n, nil
		}
	}
	return "", fmt.Errorf("\"doc_type\" must be \"\" or exactly one of: %s", strings.Join(names, ", "))
}

// IdentifyTags asks for TAGS ALONE, leaving a caption that already exists
// alone with it. The backfill for a corpus captioned before tags existed: the
// full identity would rewrite hundreds of names that are already right (and a
// person's, which must never be regenerated), for the sake of two columns.
//
// Returns the content and role tags. Source/Model/At are the CALLER's to
// merge, because what is being written is part of an identity somebody else
// already authored.
func (id *Identifier) IdentifyTags(ctx context.Context, ask IdentityAsk) (content, roles []string, err error) {
	if n := contentChars(ask.Text); n < identityMinTextChars {
		return nil, nil, &ErrIdentityTooShort{Chars: n}
	}
	prompt := identityTagsPrompt + tagContextBlock(ask.TagContext) + HintBlock(ask.IndexHint) +
		"\n\nDOCUMENT:\n" + identityExcerpt(ask.Text)
	err = id.ask(ctx, prompt, "emit_tags", identityTagsJSONShape, func(js string) error {
		var d DocIdentity
		if uerr := json.Unmarshal([]byte(js), &d); uerr != nil {
			return fmt.Errorf("unparseable: %v", uerr)
		}
		c, r, verr := validateTags(d.ContentTags, d.RoleTags)
		if verr != nil {
			return verr
		}
		content, roles = c, r
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("identity tags: %w", err)
	}
	return content, roles, nil
}

// tagContextBlock renders the index's established vocabulary for a prompt.
func tagContextBlock(tagContext string) string {
	if strings.TrimSpace(tagContext) == "" {
		return ""
	}
	return "\n\nExisting tags in this index (reuse these terms when they fit):\n" + tagContext
}

// ask runs one bounded question with its JSON fix loop: schema-validate the
// answer, hand it to accept for the checks a schema cannot express, and quote
// the failure back with the shape on a retry.
//
// Shared by both asks. The loop is the part that is easy to get subtly wrong —
// a cut-off answer needs a different sampler, a wrong answer needs the reason
// quoted back — and having it once is what keeps the tags ask from being a
// worse version of it.
func (id *Identifier) ask(ctx context.Context, prompt, tool, shape string, accept func(js string) error) error {
	return id.askWith(ctx, id.validator, prompt, tool, shape, accept)
}

func (id *Identifier) askWith(ctx context.Context, validator *agent.SchemaValidator,
	prompt, tool, shape string, accept func(js string) error) error {
	msgs := []llm.Message{{Role: "user", Parts: []llm.ContentPart{llm.TextPart(prompt)}}}
	opts := &llm.ChatOpts{MaxTokens: identityMaxTokens}
	var lastErr error
	for attempt := 0; attempt <= id.MaxRetries; attempt++ {
		out, rep, err := collectStream(ctx, id.Client, msgs, opts)
		if err != nil {
			return err
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
					lastErr, shape)})
			continue
		}
		js := extractJSON(out)
		if lastErr = validator.ValidateArgs(tool, js); lastErr == nil {
			if lastErr = accept(js); lastErr == nil {
				return nil
			}
		}
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: excerptForRetry(out)},
			llm.Message{Role: "user", Content: fmt.Sprintf(
				"That was not valid: %v. Output ONLY the JSON object %s.", lastErr, shape)})
	}
	return lastErr
}

// identityJSONShape is the shape quoted back at the model on a retry.
const identityJSONShape = `{"name":"...","summary":"...","kind":"...","content_tags":["..."],"role_tags":["..."]}`

// identityTagsJSONShape is the same, for the tags-only ask.
const identityTagsJSONShape = `{"content_tags":["..."],"role_tags":["..."]}`

// identityTagsPrompt is the tags-only ask. It shares the field descriptions
// with identityPrompt rather than restating them, because two prompts that
// drift produce two vocabularies — which is the exact failure tags exist to
// avoid, one level up.
var identityTagsPrompt = `You are tagging a document that is already catalogued in a legal/records
index. It already has a caption; do not write another one. Read it and output
ONLY a JSON object:
` + identityTagsJSONShape + `

` + identityTagFields + `

Describe only what the document says. Do not infer a subject it does not state.`

// identityTagFields describes the two tag fields, for both prompts that ask for
// them. One text, so the two asks cannot describe the same field differently.
const identityTagFields = `- "content_tags": 3 to 5 short noun phrases (1–3 words each, lowercase) naming
  what the document is ABOUT — its subject matter, the entities or procedures it
  concerns. Not the document type, and not a summary of its content. Examples:
  "lead paint inspection", "boundary survey", "escrow closing". No commas inside
  a tag. Reuse terms from the existing tag list below when they fit; do not
  invent a new spelling for a concept already tagged.
- "role_tags": 1 to 3 of the following, naming what job this document does in
  the corpus:
    documentation — explains how something works; meant to be read
    reference     — looked up for a fact, not read through
    overview      — orienting summary of a larger body of work
    specification — prescribes what something should be or do
    guide         — step-by-step instructions for doing something
    changelog     — records what changed and when
    notes         — working notes, scratch, intermediate thinking
    report        — findings from an investigation or measurement
    data          — tabular or structured data, not prose about it
    other         — none of the above genuinely fits`

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
- "kind": exactly one of the following. Choose by what the document IS, not by
  what it is about:
    deed           — an instrument conveying or encumbering land
    survey         — a survey, plat or map of land
    agreement      — a contract between parties, signed or offered
    correspondence — a letter, email, memo or notice between people
    court filing   — anything filed in or issued by a court
    certification  — a certificate, affidavit, permit or official attestation
    analysis       — a report, assessment, inspection or study of something
    notes          — a document somebody MADE about the matter rather than one
                     filed in it: a timeline, a witness list, a call transcript,
                     a packet assembled for counsel, a worklist
    commercial     — a record of work done or goods sold: an invoice, a work
                     order, a receipt, a statement, a property listing
    other          — none of the above genuinely fits. Prefer any term above to
                     this one; "other" is a last resort, not a default.

` + identityTagFields + `

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
	ct, rt, err := validateTags(d.ContentTags, d.RoleTags)
	if err != nil {
		return d, err
	}
	d.ContentTags, d.RoleTags = ct, rt
	return d, nil
}

// validateTags enforces the tag shapes: 3–5 short content phrases, 1–3 roles
// from the closed vocabulary. Shared by the full ask and the tags-only one.
func validateTags(content, roles []string) ([]string, []string, error) {
	var ct []string
	for _, tag := range content {
		tag = normalizeContentTag(tag)
		if tag == "" || len(tag) > identityMaxTagChars || len(strings.Fields(tag)) > identityMaxTagWords {
			continue
		}
		if !tagContains(ct, tag) {
			ct = append(ct, tag)
		}
	}
	if len(ct) < 3 {
		return nil, nil, fmt.Errorf("\"content_tags\" needs 3 to 5 short noun phrases (1–3 words each, lowercase, no commas); got %d usable", len(ct))
	}
	var rt []string
	for _, tag := range roles {
		r, ok := NormalizeRole(tag)
		if !ok {
			continue
		}
		if !tagContains(rt, r) {
			rt = append(rt, r)
		}
	}
	if len(rt) < 1 {
		return nil, nil, fmt.Errorf("\"role_tags\" needs 1 to 3 of: %s", strings.Join(identityRoleKinds, ", "))
	}
	return ct[:min(len(ct), 5)], rt[:min(len(rt), 3)], nil
}

const (
	// identityMaxTagChars / identityMaxTagWords bound a tag to a phrase. Past
	// them the model has written a summary into a tag field, and a tag nothing
	// else will ever repeat cannot group anything.
	identityMaxTagChars = 40
	identityMaxTagWords = 3
)

// normalizeContentTag lowercases a tag, collapses its whitespace, and strips
// the punctuation that would break the storage.
//
// The COMMA matters: tags are stored comma-separated, so a tag containing one
// comes back as two tags on the next read — silently, and only for the
// documents that happened to get one. Stripped rather than rejected, because
// "escrow, closing" is a usable tag with a stray comma in it, not junk.
func normalizeContentTag(tag string) string {
	tag = strings.Map(func(r rune) rune {
		switch r {
		case ',', ';', '"', '\'':
			return ' '
		}
		return r
	}, tag)
	tag = strings.ToLower(strings.Join(strings.Fields(tag), " "))
	return strings.Trim(tag, ".-·")
}

func tagContains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// DocumentIdentity returns what is recorded about a document. A document with
// no caption yet returns the zero value and no error — that is a state, not a
// failure. An unknown path IS an error.
func (s *Store) DocumentIdentity(path string) (DocIdentity, error) {
	var d DocIdentity
	var ctStr, rtStr string
	err := s.db.QueryRow(
		`SELECT gen_name, gen_summary, gen_kind, gen_source, gen_model, gen_at, gen_text_hash,
		        gen_content_tags, gen_role_tags, gen_doc_type
		   FROM documents WHERE path = ?`, path).
		Scan(&d.Name, &d.Summary, &d.Kind, &d.Source, &d.Model, &d.At, &d.TextHash, &ctStr, &rtStr, &d.DocType)
	if errors.Is(err, sql.ErrNoRows) {
		return DocIdentity{}, fmt.Errorf("raglit: no document with path %q", path)
	}
	if err != nil {
		return DocIdentity{}, err
	}
	d.ContentTags = splitTagList(ctStr)
	d.RoleTags = splitTagList(rtStr)
	return d, nil
}

// splitTagList turns a comma-separated tag column back into a slice.
func splitTagList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// identityAsk assembles everything about THIS index that shapes an identity
// answer: the established tag vocabulary, the corpus owner's hint, and the
// registered document types the answer may choose one of.
func (s *Store) identityAsk(text string) (IdentityAsk, error) {
	types, err := s.DocTypeRefs()
	if err != nil {
		return IdentityAsk{}, err
	}
	return IdentityAsk{
		Text:       text,
		TagContext: s.TagContext(),
		IndexHint:  s.IndexHint(),
		DocTypes:   types,
	}, nil
}

// identityTagContextSize is how much of the index's vocabulary the prompt
// carries: enough that an established term is recognisable, short enough that
// it does not crowd out the document itself.
const identityTagContextSize = 15

// TagContext is this index's established tag vocabulary, as one line for the
// identity prompt — the mechanism that keeps "lead paint" from drifting into
// "LBP", "paint inspection" and "lead-based paint hazards" across a corpus.
// Empty for a fresh index, and empty rather than an error when the read fails:
// a caption without the vocabulary is worse than one with it, not a failure.
func (s *Store) TagContext() string {
	d, err := s.IndexDigestFor("", identityTagContextSize)
	if err != nil || len(d.Content) == 0 {
		return ""
	}
	return TagLine(d.Content)
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
		`UPDATE documents SET gen_name=?, gen_summary=?, gen_kind=?, gen_source=?, gen_model=?, gen_at=?, gen_text_hash=?,
		   gen_content_tags=?, gen_role_tags=?, gen_doc_type=?
		  WHERE id=?`,
		d.Name, d.Summary, d.Kind, d.Source, d.Model, d.At, d.TextHash,
		strings.Join(d.ContentTags, ","), strings.Join(d.RoleTags, ","), d.DocType, docID); err != nil {
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

// documentTextHash fingerprints a document's indexed words — the same measure
// commitDoc applies to what it just committed.
func (s *Store) documentTextHash(ctx context.Context, path string) (string, error) {
	_ = ctx
	c, err := s.DocText(path, 0, 0, 0)
	if err != nil {
		return "", err
	}
	return IdentityTextHash(c.Text), nil
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
	ask, err := s.identityAsk(text)
	if err != nil {
		return DocIdentity{}, err
	}
	id, err := s.identifier.Identify(ctx, ask)
	if err != nil {
		return DocIdentity{}, err
	}
	// Hashed on the INDEXED text, not on the text the model read. The two differ
	// when a person has corrected a page — IdentityText overlays the reading in
	// force — and commitDoc can only compare what it commits, which is
	// fragments. Measuring both sides the same way keeps a corrected document
	// from reading as permanently stale. A correction has its own edge; see
	// AddPageReading.
	id.TextHash, _ = s.documentTextHash(ctx, path)
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
		// A person's caption carries the CURRENT text's hash, so a later re-read
		// does not mark it stale — it would not be regenerated anyway, and a
		// permanent "stale" on a ruling nobody will overturn is noise.
		TextHash: cur.TextHash,
		Name:     firstNonBlank(d.Name, cur.Name),
		Summary:  firstNonBlank(d.Summary, cur.Summary),
		Kind:     firstNonBlank(d.Kind, cur.Kind),
		// Tags are the machine's until a person supplies their own; a correction
		// to the name does not silently blank the tags.
		ContentTags: firstNonEmpty(d.ContentTags, cur.ContentTags),
		RoleTags:    firstNonEmpty(d.RoleTags, cur.RoleTags),
		DocType:     firstNonBlank(d.DocType, cur.DocType),
		Source:      IdentityByPerson,
		Model:       strings.TrimSpace(by),
		At:          time.Now().UnixNano(),
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

// firstNonEmpty returns the first non-empty slice, or nil.
func firstNonEmpty(ss ...[]string) []string {
	for _, s := range ss {
		if len(s) > 0 {
			return s
		}
	}
	return nil
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

// DocumentsMissingTags lists documents that HAVE a caption but no tags — a
// corpus captioned before tags existed. Its own selector rather than a flag on
// the one above, because the two are different work: one needs a caption
// written, the other needs a caption LEFT ALONE and two columns filled in.
func (s *Store) DocumentsMissingTags() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT path FROM documents
		  WHERE TRIM(gen_name) <> '' AND TRIM(gen_content_tags) = ''
		  ORDER BY added_at`)
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

// TagDocument fills in a document's tags WITHOUT touching its caption.
//
// The caption's authorship is preserved exactly — a person's stays a person's,
// a machine's keeps the model and timestamp that wrote it — because tags are
// not a re-reading of the document, they are two columns that were never asked
// for. Returns ErrIdentityKept when the document already has tags, unless
// forced.
func (s *Store) TagDocument(ctx context.Context, path string, force bool) (DocIdentity, error) {
	cur, err := s.DocumentIdentity(path)
	if err != nil {
		return DocIdentity{}, err
	}
	if cur.Empty() {
		return DocIdentity{}, fmt.Errorf("raglit: %s has no caption to tag — run identify first", path)
	}
	if len(cur.ContentTags) > 0 && !force {
		return cur, ErrIdentityKept
	}
	if s.identifier == nil {
		return DocIdentity{}, ErrNoIdentifier
	}
	text, err := s.IdentityText(ctx, path)
	if err != nil {
		return DocIdentity{}, err
	}
	ask, err := s.identityAsk(text)
	if err != nil {
		return DocIdentity{}, err
	}
	content, roles, err := s.identifier.IdentifyTags(ctx, ask)
	if err != nil {
		return DocIdentity{}, err
	}
	out := cur
	out.ContentTags, out.RoleTags = content, roles
	if err := s.SetDocumentIdentity(ctx, path, out); err != nil {
		return DocIdentity{}, err
	}
	return out, nil
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
		`SELECT path, gen_name, gen_summary, gen_kind, gen_source, gen_model, gen_at,
		        gen_content_tags, gen_role_tags, gen_doc_type
		   FROM documents ORDER BY added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityStatus
	for rows.Next() {
		var st IdentityStatus
		var ctStr, rtStr string
		if err := rows.Scan(&st.Path, &st.Name, &st.Summary, &st.Kind, &st.Source, &st.Model, &st.At, &ctStr, &rtStr, &st.DocType); err != nil {
			return nil, err
		}
		st.ContentTags = splitTagList(ctStr)
		st.RoleTags = splitTagList(rtStr)
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
	if len(d.ContentTags) > 0 {
		b.WriteString("CONTENT: " + strings.Join(d.ContentTags, ", ") + "\n")
	}
	if len(d.RoleTags) > 0 {
		b.WriteString("ROLE: " + strings.Join(d.RoleTags, ", ") + "\n")
	}
	if d.DocType != "" {
		b.WriteString("TYPE: " + d.DocType + "\n")
	}
	if d.Summary != "" {
		b.WriteString("\n" + d.Summary + "\n")
	}
	return b.String()
}
