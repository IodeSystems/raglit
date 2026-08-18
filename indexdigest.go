package raglit

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/agentkit/llm"
)

// What an INDEX is, as opposed to what a document is.
//
// Identity answers "what is this document" (identity.go). Nothing answered
// "what is in this index", and the absence showed up as a specific failure: an
// agent searching a corpus for a topic it does not hold gets an empty result,
// which is indistinguishable from a badly phrased query, so it rephrases and
// searches again. Four times. The index knows the answer — it has a kind and a
// set of tags for every document it holds — and simply never said it.
//
// Two forms, because they answer to different costs:
//
//   - The DIGEST is counted, not generated: documents, kinds, top content and
//     role tags. One query over `documents`, no model, never stale. It is the
//     one attached to an empty search, because that path must stay cheap.
//   - The ABOUT is a paragraph a model wrote from the captions. It reads far
//     better than a tag histogram and costs a call, so it is generated on
//     demand, stored in index_meta, and stamped with the document count it was
//     written from — a summary of 40 documents shown for an index of 400 is
//     worse than none, and the stamp is what makes that detectable.

// TagCount is one tag or kind and how many documents carry it.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// IndexDigest is what an index contains, counted from what is already stored.
type IndexDigest struct {
	// Name is the index this describes. Set by a caller reporting across
	// several; a store does not know what it is called.
	Name string `json:"index,omitempty"`
	// Path is the subtree this digest was computed over, empty for the whole
	// index. Reported because a digest of the whole index shown for a
	// path-scoped search claims coverage the subtree does not have.
	Path      string     `json:"path,omitempty"`
	Documents int        `json:"documents"`
	Untagged  int        `json:"untagged,omitempty"`
	Kinds     []TagCount `json:"kinds,omitempty"`
	Content   []TagCount `json:"content,omitempty"`
	Roles     []TagCount `json:"roles,omitempty"`
	// Types is the index's SCHEMAED documents — which registered document types
	// its documents resolved as, and how many of each have been extracted. Part
	// of the digest because "this corpus holds 88 work orders whose fields are
	// readable" is the single most useful thing that can be said about an index
	// that has any, and nothing else says it.
	Types []FieldsCoverage `json:"types,omitempty"`
	// About is the generated paragraph, when one has been written and still
	// matches the corpus it was written from. See IndexAbout.
	About string `json:"about,omitempty"`
	// AboutStale marks an About written from a materially different corpus —
	// present so a caller can show it AND say it is behind, rather than
	// choosing between a stale summary and none.
	AboutStale bool `json:"about_stale,omitempty"`
}

// Empty reports whether there is nothing to say about this index.
func (d IndexDigest) Empty() bool { return d.Documents == 0 }

// digestRow is the one row the digest reads per document. Deliberately not
// DocSummary: that carries per-document fragment and engine counts, each a
// correlated subquery, and the digest wants none of them.
type digestRow struct {
	path    string
	kind    string
	content []string
	roles   []string
}

func (s *Store) digestRowsLocal(pathPrefix string) ([]digestRow, error) {
	q := `SELECT path, gen_kind, gen_content_tags, gen_role_tags FROM documents`
	var args []any
	if pathPrefix != "" {
		q += ` WHERE path LIKE ? ESCAPE '\'`
		args = append(args, likePrefix(pathPrefix))
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []digestRow
	for rows.Next() {
		var r digestRow
		var ct, rt string
		if err := rows.Scan(&r.path, &r.kind, &ct, &rt); err != nil {
			return nil, err
		}
		r.content, r.roles = splitTagList(ct), splitTagList(rt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// likePrefix escapes a path prefix for a LIKE, so a document path containing %
// or _ constrains to itself rather than to everything.
func likePrefix(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(p) + "%"
}

// digestRows overlays branch rows over the parent's, the same way Documents
// does — a branch that reports only its own documents describes an index the
// reader is not searching.
func (s *Store) digestRows(pathPrefix string) ([]digestRow, error) {
	rows, err := s.digestRowsLocal(pathPrefix)
	if err != nil || s.parent == nil {
		return rows, err
	}
	shadow, err := s.shadowedPaths()
	if err != nil {
		return nil, err
	}
	prows, err := s.parent.digestRows(pathPrefix)
	if err != nil {
		return nil, err
	}
	for _, pr := range prows {
		if !shadow[pr.path] {
			rows = append(rows, pr)
		}
	}
	return rows, nil
}

// IndexDigestFor counts what the index holds under a path prefix — empty for
// the whole index. topN caps each list; 0 means "all of them".
func (s *Store) IndexDigestFor(pathPrefix string, topN int) (IndexDigest, error) {
	rows, err := s.digestRows(pathPrefix)
	if err != nil {
		return IndexDigest{}, err
	}
	d := IndexDigest{Path: pathPrefix, Documents: len(rows)}
	kinds, content, roles := map[string]int{}, map[string]int{}, map[string]int{}
	for _, r := range rows {
		if r.kind != "" {
			kinds[r.kind]++
		}
		if len(r.content) == 0 && len(r.roles) == 0 {
			d.Untagged++
		}
		for _, t := range r.content {
			content[t]++
		}
		for _, t := range r.roles {
			roles[t]++
		}
	}
	d.Kinds = rankTags(kinds, topN)
	d.Content = rankTags(content, topN)
	d.Roles = rankTags(roles, topN)
	return d, nil
}

// IndexDigest counts the whole index, keeping the ten most common content tags
// and every kind and role — the closed vocabularies are short enough to show
// entire, and a truncated one reads as an absence.
func (s *Store) IndexDigest() (IndexDigest, error) {
	d, err := s.IndexDigestFor("", 0)
	if err != nil {
		return d, err
	}
	if len(d.Content) > indexDigestTopTags {
		d.Content = d.Content[:indexDigestTopTags]
	}
	d.Types, _ = s.FieldsCoverage()
	about, stale, _ := s.indexAboutStored(d.Documents)
	d.About, d.AboutStale = about, stale
	return d, nil
}

// indexDigestTopTags is how many content tags a digest shows: enough to
// recognise the corpus, short enough to read.
const indexDigestTopTags = 10

// rankTags orders a histogram by count then name — name as the tiebreak so the
// same corpus reports the same digest twice running. n <= 0 keeps all.
func rankTags(m map[string]int, n int) []TagCount {
	out := make([]TagCount, 0, len(m))
	for t, c := range m {
		out = append(out, TagCount{Tag: t, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// TagLine renders tags as "tag(count), tag(count)" — the form the identity
// prompt takes its vocabulary in, and the form the digest prints.
func TagLine(tags []TagCount) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		parts = append(parts, fmt.Sprintf("%s(%d)", t.Tag, t.Count))
	}
	return strings.Join(parts, ", ")
}

// The generated paragraph.

const (
	metaIndexAbout      = "index_about"       // the paragraph
	metaIndexAboutDocs  = "index_about_docs"  // the document count it was written from
	metaIndexAboutModel = "index_about_model" // which model wrote it
)

// indexAboutStaleRatio is how far the corpus may move before a stored About is
// reported stale: a fifth of the documents. Below that the paragraph still
// describes the index; above it, it describes a different one.
const indexAboutStaleRatio = 0.2

// indexAboutStored reads the stored paragraph and says whether the corpus has
// moved out from under it.
func (s *Store) indexAboutStored(docs int) (about string, stale bool, model string) {
	about, ok := s.Meta(metaIndexAbout)
	if !ok || strings.TrimSpace(about) == "" {
		return "", false, ""
	}
	model, _ = s.Meta(metaIndexAboutModel)
	was := 0
	if v, ok := s.Meta(metaIndexAboutDocs); ok {
		fmt.Sscanf(v, "%d", &was)
	}
	if was <= 0 {
		return about, true, model
	}
	drift := float64(docs-was) / float64(was)
	if drift < 0 {
		drift = -drift
	}
	return about, drift > indexAboutStaleRatio, model
}

// IndexAbout is the stored paragraph and whether it is behind the corpus.
// Empty when none has been written; writing one is WriteIndexAbout.
func (s *Store) IndexAbout() (about string, stale bool, err error) {
	d, err := s.IndexDigestFor("", 0)
	if err != nil {
		return "", false, err
	}
	about, stale, _ = s.indexAboutStored(d.Documents)
	return about, stale, nil
}

// indexAboutPrompt asks for the paragraph. It is given captions and tags, never
// document text: the summaries are already the model's account of the
// documents, and re-reading hundreds of transcripts to say what a corpus is
// about costs more than the answer is worth.
const indexAboutPrompt = `You are describing a DOCUMENT INDEX to somebody who has not seen it, so
they can tell whether what they are looking for is in it.

Write ONE paragraph, 3 to 6 sentences, plain prose. Say what this collection
IS: the matter or subject it concerns, the kinds of document it holds, the
period it covers if the captions show one, and anything it conspicuously
contains a lot of. Name real specifics from the captions — parties, places,
instruments — rather than describing the collection in the abstract.

Do not list the documents. Do not use bullet points. Do not speculate about
what the collection is FOR, and do not describe documents that are not in the
list below. If the captions do not support a claim, leave it out.

Output the paragraph only, with no preamble and no heading.`

// indexAboutMaxCaptions is how many captions the prompt carries. A corpus of
// hundreds is described as well by a broad sample as by all of it, and the
// whole list would not fit.
const indexAboutMaxCaptions = 120

// WriteIndexAbout asks the model what this index is and stores the answer.
//
// Uses the identity model, because it is the one already configured to read
// this corpus and the task is the same task one level up.
func (s *Store) WriteIndexAbout(ctx context.Context) (string, error) {
	if s.identifier == nil {
		return "", ErrNoIdentifier
	}
	d, err := s.IndexDigestFor("", 0)
	if err != nil {
		return "", err
	}
	if d.Documents == 0 {
		return "", fmt.Errorf("raglit: this index holds no documents")
	}
	ids, err := s.Identities()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(indexAboutPrompt)
	b.WriteString("\n\nTHE INDEX HOLDS " + fmt.Sprint(d.Documents) + " DOCUMENTS.")
	if len(d.Kinds) > 0 {
		b.WriteString("\nKINDS: " + TagLine(d.Kinds))
	}
	if len(d.Content) > 0 {
		b.WriteString("\nTAGS: " + TagLine(d.Content))
	}
	b.WriteString("\n\nCAPTIONS:\n")
	n := 0
	for _, st := range ids {
		if strings.TrimSpace(st.Name) == "" {
			continue
		}
		if n >= indexAboutMaxCaptions {
			fmt.Fprintf(&b, "… and %d more.\n", d.Documents-n)
			break
		}
		b.WriteString("- " + oneLineTag(st.Name))
		if st.Kind != "" {
			b.WriteString(" [" + st.Kind + "]")
		}
		b.WriteString("\n")
		n++
	}
	if n == 0 {
		return "", fmt.Errorf("raglit: no document in this index has a caption yet — run `raglit identify` first")
	}

	msgs := []llm.Message{{Role: "user", Parts: []llm.ContentPart{llm.TextPart(b.String())}}}
	out, _, err := collectStream(ctx, s.identifier.Client, msgs, &llm.ChatOpts{MaxTokens: identityMaxTokens})
	if err != nil {
		return "", err
	}
	about := strings.TrimSpace(out)
	if about == "" {
		return "", fmt.Errorf("raglit: the model returned no summary")
	}
	now := time.Now().UnixNano()
	if err := s.SetMeta(metaIndexAbout, about, now); err != nil {
		return "", err
	}
	if err := s.SetMeta(metaIndexAboutDocs, fmt.Sprint(d.Documents), now); err != nil {
		return "", err
	}
	if err := s.SetMeta(metaIndexAboutModel, s.identifier.Model, now); err != nil {
		return "", err
	}
	return about, nil
}

// oneLineTag flattens a caption onto one line for a prompt list.
func oneLineTag(s string) string { return strings.Join(strings.Fields(s), " ") }

// The index hint.

const (
	metaIndexHint   = "index_hint"
	metaIndexHintAt = "index_hint_at"
)

// IndexHint is what a person tells the models about THIS corpus, in their own
// words: how to decode it, which of its ambiguities resolve which way, what
// matters in it.
//
// It exists because the model is answering a general question about a specific
// corpus, and the corpus is the half it cannot see. "RO" on a garage's paperwork
// is a repair order, not "received"; a survey's marginal figures are bearings,
// not measurements; a scanned carbon copy's second column is the customer's, not
// a duplicate. None of that is inferable from one page, all of it is obvious to
// whoever owns the corpus, and every prompt that reads a document is worse for
// not having it.
//
// So it travels the whole way down: the page transcription, the segmentation,
// the identity, and the field extraction. It is part of the READING recipe for
// that reason — a changed hint changes what a page says, and pooled work read
// under the old one must not be replayed under the new.
func (s *Store) IndexHint() string {
	h, _ := s.Meta(metaIndexHint)
	return strings.TrimSpace(h)
}

// SetIndexHint records it. Empty clears it.
func (s *Store) SetIndexHint(hint string, now int64) error {
	hint = strings.TrimSpace(hint)
	if err := s.SetMeta(metaIndexHint, hint, now); err != nil {
		return err
	}
	return s.SetMeta(metaIndexHintAt, fmt.Sprint(now), now)
}

// HintBlock renders the hint for a prompt, or "" when there is none. One
// wording, so the four prompts that carry it cannot present it differently —
// and labelled as the corpus owner's words, so a model weighs it as context
// about the collection rather than as an instruction from the document.
func HintBlock(hint string) string {
	if strings.TrimSpace(hint) == "" {
		return ""
	}
	return "\n\nABOUT THIS COLLECTION (from the person who owns it — context for" +
		" reading it, not part of any document):\n" + strings.TrimSpace(hint)
}
