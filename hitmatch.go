package raglit

import (
	"fmt"
	"strings"
	"unicode"
)

// queryStopWords are terms that do not count toward a hit's term coverage.
// They still go to FTS5 (ftsQuery ORs every token); they are only left out of
// Matched and QueryTerms, where "the" matching would make a weak hit look
// covered.
var queryStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "do": true, "does": true, "for": true, "from": true,
	"how": true, "i": true, "in": true, "is": true, "it": true, "of": true,
	"on": true, "or": true, "that": true, "the": true, "this": true, "to": true,
	"was": true, "what": true, "when": true, "where": true, "which": true,
	"who": true, "why": true, "with": true,
}

// searchTerms is the query's distinct terms in order: the same whitespace
// tokens ftsQuery sends, lowercased, edge punctuation trimmed, stopwords out.
func searchTerms(query string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.Fields(query) {
		t := strings.ToLower(strings.TrimFunc(tok, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}))
		if t == "" || queryStopWords[t] || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// markMatched sets Matched and QueryTerms on hits from this layer. FTS5 says
// which terms match, so the porter stemming agrees with the ranking: "restart"
// matches a fragment that says "restarts". One query per term, over the hit
// ids only.
func (s *Store) markMatched(query string, hits []Hit) error {
	terms := searchTerms(query)
	for i := range hits {
		hits[i].Matched = []string{}
		hits[i].QueryTerms = len(terms)
	}
	if len(hits) == 0 || len(terms) == 0 {
		return nil
	}
	at := map[int64][]int{}
	args := make([]any, 1, len(hits)+1)
	for i, h := range hits {
		if _, ok := at[h.ID]; !ok {
			args = append(args, h.ID)
		}
		at[h.ID] = append(at[h.ID], i)
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(args)-1), ",")
	for _, t := range terms {
		args[0] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
		rows, err := s.db.Query(`SELECT rowid FROM fragments_fts WHERE fragments_fts MATCH ? AND rowid IN (`+in+`)`, args...)
		if err != nil {
			return fmt.Errorf("raglit: matched terms: %w", err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			for _, i := range at[id] {
				hits[i].Matched = append(hits[i].Matched, t)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}
