package raglit

import (
	"sort"
	"strings"
)

// DocTypeNearMiss is one entry in the result of DocTypeNearMisses:
// Count captioned documents look like TypeName and carry no gen_doc_type.
type DocTypeNearMiss struct {
	TypeName string `json:"type_name"`
	Count    int    `json:"count"`
}

// DocTypeNearMisses counts, per registered type, the documents that are
// captioned, carry no gen_doc_type, and whose caption text matches at least
// one keyword from that type's Description.
//
// "Matches" is a cheap keyword test, not a model call. The type's Description
// is tokenised into lowercase content words (3+ characters, minus a stop-word
// list); a document matches when its caption text (gen_name and gen_summary,
// concatenated and lowercased) contains at least one of those words as a
// substring.
//
// This is the coverage warning from the F23 near-miss detection: "N documents
// look like type T and carry none." It is computable at identity time without
// a second model pass.
//
// Branch-aware: a branch overlays its own documents over the parent's, the
// same way CoverageFor does.
func (s *Store) DocTypeNearMisses() ([]DocTypeNearMiss, error) {
	types, err := s.DocTypes()
	if err != nil {
		return nil, err
	}

	// Build the keyword set per type, skipping types with no description.
	type keySet struct {
		name string
		keys []string
	}
	var sets []keySet
	for _, t := range types {
		desc := strings.TrimSpace(t.Description)
		if desc == "" {
			continue
		}
		keys := docTypeKeywords(desc)
		if len(keys) == 0 {
			continue
		}
		sets = append(sets, keySet{NormalizeTypeName(t.Name), keys})
	}
	if len(sets) == 0 {
		return nil, nil
	}

	rows, err := s.nearMissRows("")
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int, len(sets))
	for _, r := range rows {
		text := strings.ToLower(r.name + " " + r.summary)
		for _, ks := range sets {
			if nearMissMatch(text, ks.keys) {
				counts[ks.name]++
			}
		}
	}

	out := make([]DocTypeNearMiss, 0, len(counts))
	for name, n := range counts {
		if n > 0 {
			out = append(out, DocTypeNearMiss{TypeName: name, Count: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].TypeName < out[j].TypeName
	})
	return out, nil
}

// nearMissRow is one captioned, untyped document: just the two text columns
// the keyword test reads.
type nearMissRow struct {
	path    string
	name    string
	summary string
}

// nearMissRowsLocal returns the captioned, untyped documents under pathPrefix
// from this store's own table.
func (s *Store) nearMissRowsLocal(pathPrefix string) ([]nearMissRow, error) {
	q := `SELECT d.path, d.gen_name, d.gen_summary
	   FROM documents d
	   WHERE (d.gen_name <> '' OR d.gen_summary <> '')
	     AND TRIM(d.gen_doc_type) = ''
	     AND d.gen_source <> 'person'`
	var args []any
	if pathPrefix != "" {
		q += ` AND d.path LIKE ? ESCAPE '\'`
		args = append(args, likePrefix(pathPrefix))
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []nearMissRow
	for rows.Next() {
		var r nearMissRow
		if err := rows.Scan(&r.path, &r.name, &r.summary); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// nearMissRows overlays branch rows over the parent's, the same way
// coverageRows does.
func (s *Store) nearMissRows(pathPrefix string) ([]nearMissRow, error) {
	rows, err := s.nearMissRowsLocal(pathPrefix)
	if err != nil || s.parent == nil {
		return rows, err
	}
	shadow, err := s.shadowedPaths()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.path] = true
	}
	for _, pp := range s.parentPrefixes(pathPrefix) {
		prows, err := s.parent.nearMissRows(pp)
		if err != nil {
			return nil, err
		}
		for _, pr := range prows {
			bp, ok := s.fromParent(pr.path)
			if !ok || shadow[bp] || !s.admitParent(bp, pathPrefix) {
				continue
			}
			if s.worktree() {
				if seen[bp] {
					continue
				}
				seen[bp] = true
			}
			pr.path = bp
			rows = append(rows, pr)
		}
	}
	return rows, nil
}

// nearMissMatch reports whether text contains at least one of keys as a
// substring. Both are expected to be lowercase.
func nearMissMatch(text string, keys []string) bool {
	for _, k := range keys {
		if strings.Contains(text, k) {
			return true
		}
	}
	return false
}

// nearMissStopWords are the words removed from a description before it is
// turned into a keyword set. They are the function words that appear in most
// English descriptions and carry no recognition value on their own.
var nearMissStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true,
	"in": true, "on": true, "at": true, "to": true, "by": true, "for": true,
	"with": true, "from": true, "as": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "it": true, "its": true, "that": true,
	"this": true, "these": true, "those": true, "which": true, "who": true,
	"document": true, "documents": true, "form": true, "forms": true,
	"a document": true, "one": true, "per": true, "each": true, "when": true,
	"where": true, "what": true, "how": true, "why": true, "if": true, "then": true,
	"than": true, "also": true, "not": true, "no": true, "yes": true, "all": true,
	"any": true, "some": true, "such": true, "into": true, "out": true, "up": true,
	"down": true, "over": true, "under": true, "between": true, "during": true,
	"about": true, "after": true, "before": true, "against": true, "among": true,
	"through": true, "upon": true, "here": true, "there": true,
}

// docTypeKeywords extracts a set of lowercase content words from a description:
// words of 3+ characters, minus the stop-word list. Order is not significant;
// duplicates are removed.
func docTypeKeywords(desc string) []string {
	fields := strings.Fields(strings.ToLower(desc))
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		w := strings.Trim(f, ".,;:()[]\"'-/\\")
		if len(w) < 3 {
			continue
		}
		if nearMissStopWords[w] {
			continue
		}
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}
