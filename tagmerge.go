package raglit

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Tag cleanup: collapsing the drift the prompt did not prevent.
//
// Content tags are an open vocabulary, so they drift — "lead paint", "LBP" and
// "paint inspection" arrive from three documents meaning one thing. Seeding the
// prompt with the index's established tags (Store.TagContext) stops most of it
// PROSPECTIVELY, and stops none of what is already stored: an index captioned
// before that existed, or captioned in parallel before any tag was common
// enough to seed anything, holds the drift already.
//
// So this is the retroactive half, and it is deliberately NOT automatic. A
// merge is a claim that two terms mean the same thing, and no measure of
// spelling establishes that — "lead paint" and "lead paint disclosure" share
// every significant word and are different facts. `raglit audit-tags` proposes;
// a person names the merge; this applies it.

// TagMerge is one collapse: every tag in From becomes To.
type TagMerge struct {
	From []string `json:"from"`
	To   string   `json:"to"`
}

// TagMergeResult is what a merge did.
type TagMergeResult struct {
	To        string   `json:"to"`
	From      []string `json:"from"`
	Documents int      `json:"documents"` // documents whose tags changed
	Collapsed []string `json:"collapsed"` // the From tags actually present
	Missing   []string `json:"missing"`   // From tags no document carried
}

// ParseTagMerge reads the "a,b=>c" form: the tags to collapse, then the tag to
// collapse them into. Whitespace around any term is ignored.
func ParseTagMerge(spec string) (TagMerge, error) {
	left, right, ok := strings.Cut(spec, "=>")
	if !ok {
		return TagMerge{}, fmt.Errorf("raglit: a merge looks like \"old,other=>new\"; got %q", spec)
	}
	m := TagMerge{To: normalizeContentTag(right)}
	if m.To == "" {
		return TagMerge{}, fmt.Errorf("raglit: merge %q names no tag to merge INTO", spec)
	}
	for _, f := range strings.Split(left, ",") {
		if f = normalizeContentTag(f); f != "" && f != m.To {
			m.From = append(m.From, f)
		}
	}
	if len(m.From) == 0 {
		return TagMerge{}, fmt.Errorf("raglit: merge %q names no tag to merge FROM", spec)
	}
	return m, nil
}

// String renders a merge back in the form ParseTagMerge reads.
func (m TagMerge) String() string { return strings.Join(m.From, ",") + "=>" + m.To }

// MergeTags collapses From into To across every document in this index,
// re-indexing each changed document's identity fragment so the searchable text
// and the columns cannot disagree.
//
// Content tags only. Roles are a closed vocabulary and cannot drift; a role
// outside it is a validation failure to fix at the source, not a tag to merge.
//
// The caption is untouched, and so is its authorship: a person's identity keeps
// gen_source='person'. Merging a tag is not overruling anybody's caption.
func (s *Store) MergeTags(ctx context.Context, m TagMerge) (TagMergeResult, error) {
	res := TagMergeResult{To: m.To, From: m.From}
	if len(m.From) == 0 || m.To == "" {
		return res, fmt.Errorf("raglit: merge names no tags")
	}
	from := map[string]bool{}
	for _, f := range m.From {
		from[f] = true
	}

	rows, err := s.digestRowsLocal("")
	if err != nil {
		return res, err
	}
	seen := map[string]bool{}
	for _, r := range rows {
		next, changed := applyTagMerge(r.content, from, m.To)
		if !changed {
			continue
		}
		for _, t := range r.content {
			if from[t] {
				seen[t] = true
			}
		}
		cur, err := s.DocumentIdentity(r.path)
		if err != nil {
			return res, err
		}
		cur.ContentTags = next
		if err := s.SetDocumentIdentity(ctx, r.path, cur); err != nil {
			return res, err
		}
		res.Documents++
	}
	for _, f := range m.From {
		if seen[f] {
			res.Collapsed = append(res.Collapsed, f)
		} else {
			res.Missing = append(res.Missing, f)
		}
	}
	return res, nil
}

// applyTagMerge rewrites one document's tags, reporting whether anything moved.
// Order is preserved and the result is deduplicated — a document carrying both
// "LBP" and "lead paint" ends with one tag, not two of the same.
func applyTagMerge(tags []string, from map[string]bool, to string) ([]string, bool) {
	changed := false
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if from[t] {
			t, changed = to, true
		}
		if !tagContains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) != len(tags) {
		changed = true
	}
	if !changed {
		return tags, false
	}
	return out, true
}

// TagNeighbours reports, for each content tag, the other tags that share a
// significant word with it — the drift a person is being asked to rule on.
//
// A LEXICAL proposal and nothing more. It cannot see that "escrow closing" and
// "settlement" are the same thing, and it will pair "lead paint" with "lead
// paint disclosure", which are not. That is why nothing here writes.
func (s *Store) TagNeighbours() (map[string][]string, error) {
	d, err := s.IndexDigestFor("", 0)
	if err != nil {
		return nil, err
	}
	words := make(map[string][]string, len(d.Content))
	for _, t := range d.Content {
		words[t.Tag] = significantWords(t.Tag)
	}
	out := map[string][]string{}
	for _, a := range d.Content {
		var near []string
		for _, b := range d.Content {
			if a.Tag == b.Tag {
				continue
			}
			if sharesWord(words[a.Tag], words[b.Tag]) {
				near = append(near, b.Tag)
			}
		}
		if len(near) > 0 {
			sort.Strings(near)
			out[a.Tag] = near
		}
	}
	return out, nil
}

func sharesWord(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// tagStopWords are words that carry no subject: two tags sharing only these
// share nothing.
var tagStopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"this": true, "that": true, "into": true, "over": true, "under": true,
	"per": true, "via": true, "its": true, "was": true, "are": true,
	"other": true, "general": true, "misc": true, "various": true,
}

// significantWords are a tag's words with the stopwords and the very short ones
// removed. WHOLE words, not substrings: matching on substrings pairs "data"
// with "metadata" and "validation", and a proposal list nobody trusts is a
// proposal list nobody reads.
func significantWords(tag string) []string {
	var out []string
	for _, w := range strings.Fields(tag) {
		w = strings.Trim(w, ".,;:()")
		if len(w) >= 4 && !tagStopWords[w] {
			out = append(out, w)
		}
	}
	return out
}
