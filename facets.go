// Facet counts over the full matching set for a bm25 (FTS) search.
//
// A facet is a column of documents (kind, role tag, content tag, doc type) or
// of fragments (origin). FacetCounts runs one COUNT per facet value over the
// SAME FTS WHERE clause searchLocal uses (match + SearchFilter predicate),
// with no LIMIT, so the counts cover every matching document in scope, not
// just the truncated page a search would return. This is the server-side
// number a UI rail shows; counting the returned page client-side would
// under-count on a truncated result and is what this API exists to replace.
//
// The overlay (branch) path mirrors SearchFiltered's shape: this layer's
// facetLocal, then the parent's for documents the branch does not shadow.
package raglit

import (
	"sort"
	"strings"
)

// Facet is one facet-value pair from a FacetCounts result.
//
// Key is the facet name: "kind", "role_tag", "content_tag", "doc_type" or
// "origin". Value is the individual value (e.g. "reference" for role_tag,
// "described" for origin). Count is how many matching documents in the full
// set carry that value. A facet with no non-empty values among the matching
// documents is simply absent from the result.
type Facet struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Count int    `json:"count"`
}

// FacetCounts returns per-facet-value counts over the full matching set for a
// bm25 search. query and f are the same arguments SearchFiltered would take
// (the FTS query text and the SearchFilter that scopes it); there is no limit
// because the counts are over the whole set, not a page.
//
// The result carries "kind", "role_tag", "content_tag", "doc_type" and
// "origin" entries for every non-empty value found in the matching documents.
// An empty query (no FTS match) returns nil, nil: there is nothing to facet.
//
// Branch overlay: a document the branch has a copy of (or tombstoned)
// shadows the parent's, and its stale values must not count — the same
// reason a shadowed parent hit is excluded from search results. mergeFacets
// keeps them out.
func (s *Store) FacetCounts(query string, f SearchFilter) ([]Facet, error) {
	local, err := s.facetLocal(query, f)
	if err != nil {
		return nil, err
	}
	if s.parent == nil {
		return local, nil
	}
	s.touchAccess()
	shadow, err := s.shadowedPaths()
	if err != nil {
		return nil, err
	}
	// Translate the shadowed branch paths to the parent's, so the parent's
	// counts can have exactly those documents subtracted out.
	shadowParent := map[string]bool{}
	for bp := range shadow {
		if pp, ok := s.toParent(bp); ok {
			shadowParent[pp] = true
		}
	}
	// The filter's path prefix is translated into parent-space prefixes the
	// same way the search overlay does it (overlayHits); each translated
	// prefix gets its own parent count.
	var parentFacets []Facet
	for _, pp := range s.parentPrefixes(f.PathPrefix) {
		pf := f
		pf.PathPrefix = pp
		pf2, err := s.parent.facetLocal(query, pf)
		if err != nil {
			return nil, err
		}
		parentFacets = append(parentFacets, pf2...)
	}
	sub, err := s.parent.shadowedFacetValues(query, f, shadowParent)
	if err != nil {
		return nil, err
	}
	return mergeFacets(local, parentFacets, sub), nil
}

// facetLocal runs the per-facet COUNT queries for this layer's own documents
// and returns the raw facet rows, sorted, with zero-value entries dropped.
func (s *Store) facetLocal(query string, f SearchFilter) ([]Facet, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	pred, pargs, err := filterPredicate(f)
	if err != nil {
		return nil, err
	}
	if pred != "" {
		pred = " AND " + pred
	}
	// The same FTS match and the same SearchFilter predicate searchLocal puts
	// in its WHERE, so the set being counted is the set a search would rank —
	// before the LIMIT.
	baseWhere := "WHERE fragments_fts MATCH ?" + pred

	var out []Facet

	// Single-valued columns on documents: kind and doc type.
	for _, col := range []struct {
		name string
		col  string
	}{
		{"kind", "gen_kind"},
		{"doc_type", "gen_doc_type"},
	} {
		args := append([]any{match}, pargs...)
		q := "SELECT d." + col.col + ", COUNT(DISTINCT d.id)\n" +
			"  FROM fragments_fts\n" +
			"  JOIN fragments fr ON fr.id = fragments_fts.rowid\n" +
			"  JOIN documents d ON d.id = fr.doc_id\n" +
			baseWhere + "\n" +
			"  AND d." + col.col + " != ''\n" +
			"  GROUP BY d." + col.col
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			var n int
			if err := rows.Scan(&v, &n); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, Facet{Key: col.name, Value: v, Count: n})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	// Comma-joined tag columns: role tags and content tags. SQLite has no
	// string-split, so a recursive CTE peels one entry per row: the entry is
	// the text before the first comma, or the whole string when none is left.
	for _, col := range []struct {
		name string
		col  string
	}{
		{"role_tag", "gen_role_tags"},
		{"content_tag", "gen_content_tags"},
	} {
		args := append([]any{match}, pargs...)
		c := col.col
		q := `WITH RECURSIVE exploded(doc_id, tag, rest) AS (
			SELECT d.id,
			       CASE WHEN instr(d.`+c+`, ',') = 0 THEN d.`+c+`
			            ELSE substr(d.`+c+`, 1, instr(d.`+c+`, ',') - 1) END,
			       CASE WHEN instr(d.`+c+`, ',') = 0 THEN ''
			            ELSE substr(d.`+c+`, instr(d.`+c+`, ',') + 1) END
			FROM documents d
			WHERE d.`+c+` != ''
			  AND EXISTS (SELECT 1 FROM fragments_fts
			               JOIN fragments fr ON fr.id = fragments_fts.rowid
			               WHERE fr.doc_id = d.id
			                AND fragments_fts MATCH ?`+pred+`)
			UNION ALL
			SELECT e.doc_id,
			       CASE WHEN instr(e.rest, ',') = 0 THEN e.rest
			            ELSE substr(e.rest, 1, instr(e.rest, ',') - 1) END,
			       CASE WHEN instr(e.rest, ',') = 0 THEN ''
			            ELSE substr(e.rest, instr(e.rest, ',') + 1) END
			FROM exploded e
			WHERE e.rest != ''
		)
		SELECT trim(e.tag) AS tag, COUNT(DISTINCT e.doc_id)
		  FROM exploded e
		 WHERE trim(e.tag) != ''
		 GROUP BY trim(e.tag)`
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			var n int
			if err := rows.Scan(&v, &n); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, Facet{Key: col.name, Value: v, Count: n})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	// origin lives on fragments, not documents: count the distinct documents
	// that have a matching fragment of each origin.
	args := append([]any{match}, pargs...)
	q := "SELECT fr.origin, COUNT(DISTINCT d.id)\n" +
		"  FROM fragments_fts\n" +
		"  JOIN fragments fr ON fr.id = fragments_fts.rowid\n" +
		"  JOIN documents d ON d.id = fr.doc_id\n" +
		baseWhere + "\n" +
		"  AND fr.origin != ''\n" +
		"  GROUP BY fr.origin"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v string
		var n int
		if err := rows.Scan(&v, &n); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, Facet{Key: "origin", Value: v, Count: n})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out, nil
}

// shadowedFacetValues counts this layer's facet values for the documents in
// shadowParent (this layer's paths) that match query under f, so the caller
// can subtract them from this layer's full counts. The shadow set is small —
// the documents a branch has changed or tombstoned — so one query per facet
// is fine.
func (s *Store) shadowedFacetValues(query string, f SearchFilter, shadowParent map[string]bool) ([]Facet, error) {
	if len(shadowParent) == 0 {
		return nil, nil
	}
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	pred, pargs, err := filterPredicate(f)
	if err != nil {
		return nil, err
	}
	if pred != "" {
		pred = " AND " + pred
	}
	baseWhere := "WHERE fragments_fts MATCH ?" + pred + " AND d.path IN (" + placeholders(len(shadowParent)) + ")"
	in := make([]any, 0, len(shadowParent))
	for p := range shadowParent {
		in = append(in, p)
	}

	var out []Facet
	for _, col := range []struct {
		name string
		col  string
	}{
		{"kind", "gen_kind"},
		{"doc_type", "gen_doc_type"},
	} {
		// baseWhere puts MATCH ? first and the IN (...) args last, so the args
		// are [match, pargs..., in...] — the same order facetLocal and the
		// origin query below use.
		args := append([]any{match}, pargs...)
		args = append(args, in...)
		q := "SELECT d." + col.col + ", COUNT(DISTINCT d.id)\n" +
			"  FROM fragments_fts\n" +
			"  JOIN fragments fr ON fr.id = fragments_fts.rowid\n" +
			"  JOIN documents d ON d.id = fr.doc_id\n" +
			baseWhere + "\n" +
			"  AND d." + col.col + " != ''\n" +
			"  GROUP BY d." + col.col
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			var n int
			if err := rows.Scan(&v, &n); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, Facet{Key: col.name, Value: v, Count: n})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	for _, col := range []struct {
		name string
		col  string
	}{
		{"role_tag", "gen_role_tags"},
		{"content_tag", "gen_content_tags"},
	} {
		// The IN clause args come first (they appear before MATCH ? in the SQL).
		args := append([]any{}, in...)
		args = append(args, match)
		args = append(args, pargs...)
		c := col.col
		q := `WITH RECURSIVE exploded(doc_id, tag, rest) AS (
			SELECT d.id,
			       CASE WHEN instr(d.`+c+`, ',') = 0 THEN d.`+c+`
			            ELSE substr(d.`+c+`, 1, instr(d.`+c+`, ',') - 1) END,
			       CASE WHEN instr(d.`+c+`, ',') = 0 THEN ''
			            ELSE substr(d.`+c+`, instr(d.`+c+`, ',') + 1) END
			FROM documents d
			WHERE d.`+c+` != ''
			  AND d.path IN (`+placeholders(len(shadowParent))+`)
			  AND EXISTS (SELECT 1 FROM fragments_fts
			               JOIN fragments fr ON fr.id = fragments_fts.rowid
			               WHERE fr.doc_id = d.id
			                AND fragments_fts MATCH ?`+pred+`)
			UNION ALL
			SELECT e.doc_id,
			       CASE WHEN instr(e.rest, ',') = 0 THEN e.rest
			            ELSE substr(e.rest, 1, instr(e.rest, ',') - 1) END,
			       CASE WHEN instr(e.rest, ',') = 0 THEN ''
			            ELSE substr(e.rest, instr(e.rest, ',') + 1) END
			FROM exploded e
			WHERE e.rest != ''
		)
		SELECT trim(e.tag) AS tag, COUNT(DISTINCT e.doc_id)
		  FROM exploded e
		 WHERE trim(e.tag) != ''
		 GROUP BY trim(e.tag)`
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			var n int
			if err := rows.Scan(&v, &n); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, Facet{Key: col.name, Value: v, Count: n})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	args := append([]any{match}, pargs...)
	args = append(args, in...)
	q := "SELECT fr.origin, COUNT(DISTINCT d.id)\n" +
		"  FROM fragments_fts\n" +
		"  JOIN fragments fr ON fr.id = fragments_fts.rowid\n" +
		"  JOIN documents d ON d.id = fr.doc_id\n" +
		baseWhere + "\n" +
		"  AND fr.origin != ''\n" +
		"  GROUP BY fr.origin"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v string
		var n int
		if err := rows.Scan(&v, &n); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, Facet{Key: "origin", Value: v, Count: n})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return out, nil
}

// mergeFacets sums the branch layer's and the parent layer's facet counts and
// subtracts the shadowed parent documents' contribution (sub), so a document
// the branch has replaced is counted once, with the branch's values.
func mergeFacets(local, parent, sub []Facet) []Facet {
	type key struct{ k, v string }
	merged := map[key]int{}
	add := func(fs []Facet, sign int) {
		for _, fc := range fs {
			merged[key{fc.Key, fc.Value}] += sign * fc.Count
		}
	}
	add(local, 1)
	add(parent, 1)
	add(sub, -1)
	var out []Facet
	for k, c := range merged {
		if c > 0 {
			out = append(out, Facet{Key: k.k, Value: k.v, Count: c})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// placeholders is n comma-separated "?" for a SQL IN clause.
func placeholders(n int) string {
	if n <= 0 {
		return "''"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
