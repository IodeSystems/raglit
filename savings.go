package raglit

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

// What a maintained base would save a shared project.
//
// When several people's layers sit on one base and nobody folds the shared
// documents into it, each layer holds its own copy: its own passages, its own
// embeddings, its own page reads, and every search reads every layer. This
// counts what those copies cost, from what raglit records.
//
// Measured, not estimated — and so tokens are NOT here: raglit records no token
// counts. What it does record stands in for them: page reads (each one a vision
// call unless it was reused), embeddings (one call per passage), and the model
// time an import job took. A job served from the shared pool ("pooled") or
// skipped as unchanged cost no model time, so those are left out of the time.

// DocCost is what one document of one layer cost to hold.
type DocCost struct {
	Path         string
	Hash         string
	Passages     int
	Embeddings   int
	PageReads    int
	Bytes        int64   // passage text + embeddings + saved original and page images
	ModelSeconds float64 // import jobs that did model work (not pooled, not unchanged)
}

// DocCosts lists this store's OWN documents (not a parent's) with their cost.
// Documents with no content hash are left out: there is nothing to compare.
func (s *Store) DocCosts() ([]DocCost, error) {
	rows, err := s.db.Query(`
		SELECT d.path, d.content_hash,
		  (SELECT COUNT(*) FROM fragments f WHERE f.doc_id = d.id),
		  (SELECT COUNT(*) FROM fragment_vectors v JOIN fragments f ON f.id = v.fragment_id WHERE f.doc_id = d.id),
		  (SELECT COUNT(*) FROM ocr_pages o WHERE o.doc_id = d.id),
		  (SELECT COALESCE(SUM(LENGTH(f.text)), 0) FROM fragments f WHERE f.doc_id = d.id),
		  (SELECT COALESCE(SUM(LENGTH(v.vec)), 0) FROM fragment_vectors v JOIN fragments f ON f.id = v.fragment_id WHERE f.doc_id = d.id)
		FROM documents d WHERE d.content_hash != ''`)
	if err != nil {
		return nil, fmt.Errorf("raglit: doc costs: %w", err)
	}
	defer rows.Close()
	var out []DocCost
	for rows.Next() {
		var c DocCost
		var text, vec int64
		if err := rows.Scan(&c.Path, &c.Hash, &c.Passages, &c.Embeddings, &c.PageReads, &text, &vec); err != nil {
			return nil, err
		}
		c.Bytes = text + vec
		if s.withHome {
			c.Bytes += dirSize(s.home.PageDir(c.Path))
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	secs, err := s.modelSecondsByURL()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].ModelSeconds = secs[strings.TrimPrefix(out[i].Path, "file://")]
	}
	return out, nil
}

// modelSecondsByURL sums the run time of finished import jobs that did model
// work, per document path (a queued URL with or without file://).
func (s *Store) modelSecondsByURL() (map[string]float64, error) {
	rows, err := s.db.Query(`
		SELECT url, SUM(finished_at - started_at) FROM ingest_jobs
		WHERE state = 'done' AND started_at > 0 AND finished_at > started_at
		  AND mode NOT IN ('pooled', 'unchanged')
		GROUP BY url`)
	if err != nil {
		return nil, fmt.Errorf("raglit: job times: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var url string
		var ns int64
		if err := rows.Scan(&url, &ns); err != nil {
			return nil, err
		}
		out[strings.TrimPrefix(url, "file://")] += float64(ns) / 1e9
	}
	return out, rows.Err()
}

func fileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return fi.Size()
	}
	return 0
}

// LayerSavings is one layer's share of the duplication.
type LayerSavings struct {
	Name       string `json:"name"`
	Documents  int    `json:"documents"`
	Duplicates int    `json:"duplicates"` // documents another layer (or the base) also holds
}

// ProjectSavings is the report for one project id.
type ProjectSavings struct {
	ProjectID string         `json:"project_id"`
	Base      string         `json:"base"`
	Layers    []LayerSavings `json:"layers"`
	// Counted over the layers the caller can read. A document held by N
	// readable indexes counts N-1 times: the copies beyond the first.
	DuplicateDocuments  int     `json:"duplicate_documents"`
	DuplicatePassages   int     `json:"duplicate_passages"`
	DuplicateEmbeddings int     `json:"duplicate_embeddings"`
	DuplicatePageReads  int     `json:"duplicate_page_reads"`
	DuplicateBytes      int64   `json:"duplicate_bytes"`
	ModelSeconds        float64 `json:"model_seconds"` // model time spent importing the copies
	// Indexes one layer search reads now (itself, the base, its readable
	// siblings) and with a maintained base (itself and the base).
	FanoutNow      int `json:"fanout_now"`
	FanoutWithBase int `json:"fanout_with_base"`
	// Recommend is true when a maintained base would pay: two or more layers,
	// copies between them, and a base that holds nothing of its own.
	Recommend bool   `json:"recommend"`
	Reason    string `json:"reason"`
	Note      string `json:"note"`
}

// ProjectSavings reports, for a project id, what a maintained base would save.
func (r *Registry) ProjectSavings(ctx context.Context, id string) (ProjectSavings, error) {
	rep := ProjectSavings{ProjectID: id, Base: r.ProjectBase(id),
		Note: "Tokens are not recorded; page reads, embeddings and model time stand in for them. Pooled and unchanged imports cost no model time and are not counted in it."}
	if rep.Base == "" || !r.CanRead(ctx, rep.Base) {
		return rep, ErrNoIndex
	}
	layers := r.Readable(ctx, r.LayersOf(rep.Base))
	indexes := append([]string{rep.Base}, layers...)

	type holder struct {
		index string
		cost  DocCost
	}
	byHash := map[string][]holder{}
	counts := map[string]int{}
	for _, name := range indexes {
		st, err := r.Existing(name)
		if err != nil {
			continue
		}
		costs, err := st.DocCosts()
		if err != nil {
			return rep, err
		}
		counts[name] = len(costs)
		for _, c := range costs {
			byHash[c.Hash] = append(byHash[c.Hash], holder{name, c})
		}
	}
	dups := map[string]int{}
	for _, hs := range byHash {
		if len(hs) < 2 {
			continue
		}
		// Keep the copy that cost the most model time as "the first": the
		// others are the ones a maintained base would have spared.
		sort.SliceStable(hs, func(i, j int) bool { return hs[i].cost.ModelSeconds > hs[j].cost.ModelSeconds })
		for _, h := range hs {
			dups[h.index]++
		}
		for _, h := range hs[1:] {
			rep.DuplicateDocuments++
			rep.DuplicatePassages += h.cost.Passages
			rep.DuplicateEmbeddings += h.cost.Embeddings
			rep.DuplicatePageReads += h.cost.PageReads
			rep.DuplicateBytes += h.cost.Bytes
			rep.ModelSeconds += h.cost.ModelSeconds
		}
	}
	for _, name := range layers {
		rep.Layers = append(rep.Layers, LayerSavings{Name: name, Documents: counts[name], Duplicates: dups[name]})
	}
	rep.FanoutNow = len(indexes)
	rep.FanoutWithBase = 2
	if len(layers) == 0 {
		rep.FanoutWithBase = 1
	}
	switch {
	case len(layers) < 2:
		rep.Reason = "fewer than two layers share this base"
	case counts[rep.Base] > 0:
		rep.Reason = "the base already holds documents of its own"
	case rep.DuplicateDocuments == 0:
		rep.Reason = "no document is held by more than one layer"
	default:
		rep.Recommend = true
		rep.Reason = fmt.Sprintf("%d document copies across %d layers; a maintained base would hold each once and cut a search from %d indexes to %d",
			rep.DuplicateDocuments, len(layers), rep.FanoutNow, rep.FanoutWithBase)
	}
	return rep, nil
}
