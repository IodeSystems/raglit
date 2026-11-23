// Facet counts for search, over the set each mode would rank.
//
// A facet is a column of documents (kind, role tag, content tag, doc type) or
// of fragments (origin). In bm25 the counts run one COUNT per facet value
// over the SAME FTS WHERE clause searchLocal uses (match + SearchFilter
// predicate), with no LIMIT, so they cover every matching document in scope,
// not just the truncated page a search would return. Vec and hybrid have no
// SQL MATCH: their counts cover the candidate pool the search path itself
// ranks before the page limit (the limit*4 pool hybrid over-fetches by),
// with the hybrid bm25 side still counted over its full FTS set. This is the
// server-side number a UI rail shows; counting the returned page
// client-side would under-count on a truncated result and is what this API
// exists to replace.
//
// The overlay (branch) path mirrors SearchFiltered's shape: this layer's
// facetLocalPool, then the parent's for documents the branch does not shadow.
package raglit

import (
	"context"
	"fmt"
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
	return s.FacetCountsMode(context.Background(), query, f, "", 0)
}

// FacetCountsMode returns facet counts for the search mode a UI is showing.
// Mode is "" / "bm25", "vec" or "hybrid" (the same spellings SearchOwnLayer
// takes).
//
//   - bm25: the counts are over the FULL FTS matching set — the same WHERE
//     searchLocal ranks, without its LIMIT.
//   - vec and hybrid: there is no SQL MATCH on the vector side, so the counts
//     are over the CANDIDATE POOL the search path itself would rank before the
//     page limit: for vec, the limit*4 best-scoring vector hits this layer
//     returns (the multiple HybridSearchFiltered over-fetches each side by);
//     for hybrid, that pool plus the bm25 side's — and the bm25 side is
//     counted over its full FTS set, as above, so a document that matches the
//     query textually is never lost to the pool's truncation. limit is the
//     page limit the counts are relative to; <= 0 means 10, the search
//     paths' default.
//
// Branch overlay: as in FacetCounts — this layer's counts, then the parent's
// for documents this layer does not shadow. The parent's bm25-side counts
// stay over its full FTS set; only this layer's vec side is pool-bounded.
func (s *Store) FacetCountsMode(ctx context.Context, query string, f SearchFilter, mode string, limit int) ([]Facet, error) {
	if limit <= 0 {
		limit = 10
	}
	qv, err := s.embedForFacets(ctx, query, mode)
	if err != nil {
		return nil, err
	}
	return s.facetOverlay(ctx, qv, query, f, limit, mode)
}

// facetCountsOwnLayer is FacetCountsMode restricted to this layer's OWN
// documents — not its parent, the same reason SearchOwnLayer stops at this
// layer: a sibling's overlay already carries the shared parent, and folding
// it in again would count it twice. The wrapper in project.go takes a
// pathPrefix the way SearchOwnLayer does.
func (s *Store) facetCountsOwnLayer(ctx context.Context, query string, f SearchFilter, mode string, limit int) ([]Facet, error) {
	if limit <= 0 {
		limit = 10
	}
	qv, err := s.embedForFacets(ctx, query, mode)
	if err != nil {
		return nil, err
	}
	pool, err := s.facetCandidates(ctx, qv, query, f, limit, mode)
	if err != nil {
		return nil, err
	}
	return s.facetLocalPool(query, f, pool)
}

// embedForFacets embeds the query once for a vec/hybrid facet count; nil for
// bm25, which has no vector side.
func (s *Store) embedForFacets(ctx context.Context, query, mode string) ([]float32, error) {
	if mode != "vec" && mode != "hybrid" {
		return nil, nil
	}
	emb := s.textEmbedder()
	if emb == nil {
		return nil, fmt.Errorf("raglit: facet counts in %s mode need an embedder (SetEmbedder)", mode)
	}
	return emb.EmbedQuery(s.withActivity(ctx), query)
}

// facetOverlay assembles the branch-overlay facet result: this layer's counts
// (scoped by candidate pool for vec/hybrid, full FTS set for bm25), then the
// parent's for the translated path prefixes, minus the shadowed documents'
// contribution. qv is the embedded query, embedded once at the top layer; the
// parent side is always counted over its full FTS set (see FacetCountsMode).
func (s *Store) facetOverlay(ctx context.Context, qv []float32, query string, f SearchFilter, limit int, mode string) ([]Facet, error) {
	pool, err := s.facetCandidates(ctx, qv, query, f, limit, mode)
	if err != nil {
		return nil, err
	}
	local, err := s.facetLocalPool(query, f, pool)
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
		pf2, err := s.parent.facetLocalPool(query, pf, nil)
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

// facetLocalPool runs the per-facet COUNT queries for this layer's own
// documents and returns the raw facet rows, sorted, with zero-value entries
// dropped. pool scopes the set to the documents a vec/hybrid candidate pool
// holds (path-keyed, see facetCandidates); nil means no scoping — the full
// FTS matching set, the bm25 case.
func (s *Store) facetLocalPool(query string, f SearchFilter, pool map[string]bool) ([]Facet, error) {
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
	// before the LIMIT. The pool constraint (vec/hybrid) rides alongside it:
	// the candidate pool is already the search path's own result, so it is
	// the set a vec/hybrid search would show, and counting it is what
	// "the server count" means in those modes. baseWhere places the pool IN
	// clause after MATCH ? and the filter predicate, so its args are
	// [match, filter..., pool...]. The tag CTE query places the pool IN
	// clause before MATCH, so its args are [pool..., match, filter...].
	// Both are built per-query from pargs (filter only) and poolArgs(pool).
	baseWhere := "WHERE fragments_fts MATCH ?" + pred + poolWhereSQL(pool)

	var out []Facet

	// Single-valued columns on documents: kind and doc type.
	for _, col := range []struct {
		name string
		col  string
	}{
		{"kind", "gen_kind"},
		{"doc_type", "gen_doc_type"},
	} {
		// baseWhere order: [match, filter..., pool...].
		args := append(append([]any{match}, pargs...), poolArgs(pool)...)
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
		// CTE order: pool IN is before MATCH, so [pool..., match, filter...].
		args := append(append(poolArgs(pool), match), pargs...)
		c := col.col
		q := `WITH RECURSIVE exploded(doc_id, tag, rest) AS (
			SELECT d.id,
			       CASE WHEN instr(d.`+c+`, ',') = 0 THEN d.`+c+`
			            ELSE substr(d.`+c+`, 1, instr(d.`+c+`, ',') - 1) END,
			       CASE WHEN instr(d.`+c+`, ',') = 0 THEN ''
			            ELSE substr(d.`+c+`, instr(d.`+c+`, ',') + 1) END
			FROM documents d
			WHERE d.`+c+` != ''`+poolWhereSQL(pool)+`
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
	// baseWhere order: [match, filter..., pool...].
	args := append(append([]any{match}, pargs...), poolArgs(pool)...)
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

// poolWhereSQL is the SQL term that scopes a facet query to a candidate pool
// ("AND d.path IN (...)"), or "" when pool is nil (no scoping, the full FTS
// set). The path list is appended to the query's args as poolArgs returns it,
// in the same order as the placeholders. An EMPTY (non-nil) pool is the vec
// case with zero candidates: it must scope to nothing, not to the whole set.
func poolWhereSQL(pool map[string]bool) string {
	if pool == nil {
		return ""
	}
	if len(pool) == 0 {
		return " AND d.path IN ('')"
	}
	return " AND d.path IN (" + placeholders(len(pool)) + ")"
}

// poolArgs is the pool's document paths, in the order poolWhereSQL's
// placeholders expect. Nil when there is no pool.
func poolArgs(pool map[string]bool) []any {
	if pool == nil {
		return nil
	}
	paths := make([]string, 0, len(pool))
	for p := range pool {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]any, len(paths))
	for i, p := range paths {
		out[i] = p
	}
	return out
}

// facetCandidates is the set of document paths this layer's vec/hybrid search
// would rank before the page limit: for vec, the limit*4 best vector hits;
// for hybrid, that pool plus the bm25 pool (limit*4, as
// SearchOwnLayer/HybridSearchFiltered over-fetch each side). nil means no
// scoping — the full FTS set. The pool is already pruned of gone files by the
// search paths that produce it, so it is what a search would show, not what
// the table holds.
func (s *Store) facetCandidates(ctx context.Context, qv []float32, query string, f SearchFilter, limit int, mode string) (map[string]bool, error) {
	// bm25 has no vector side and is counted over its full FTS set: nil is
	// the "no scoping" sentinel (see poolWhereSQL). An empty NON-nil pool
	// would scope to nothing and zero every count.
	if mode != "vec" && mode != "hybrid" {
		return nil, nil
	}
	pool := map[string]bool{}
	if mode == "hybrid" {
		lex, err := s.searchLocal(query, f, limit*4)
		if err != nil {
			return nil, err
		}
		for _, h := range lex {
			pool[h.Path] = true
		}
	}
	vec, err := s.vecSearchLocal(qv, query, f, limit*4)
	if err != nil {
		return nil, err
	}
	for _, h := range vec {
		pool[h.Path] = true
	}
	// The empty map is the zero-candidate vec/hybrid case, and it must stay
	// non-nil: nil means "no scoping" (the bm25 case), and an empty vec pool
	// must count as zero documents, not as the whole set.
	return pool, nil
}
