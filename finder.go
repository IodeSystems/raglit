package raglit

import (
	"context"

	"github.com/iodesystems/agentkit/agent"
)

// Finder adapts a raglit Store to agent.DocFinder, so a local index drops
// straight into agentkit's proactive-retrieval seam (agent.FinderPreparer) and
// its MCP tool bridge — the SAME interface a remote ragtag client satisfies.
// That is the scale path: start with a local .sqlite Finder, swap to a remote
// impl later without touching the agent wiring.
//
// A DocFinder hit is per-DOCUMENT (the notify layer dedups by DocID and injects
// a pointer), but Search returns per-FRAGMENT rows. Finder collapses fragments
// to the best-scoring one per document, so a doc pings once with its strongest
// passage as the pointer line.
type Finder struct {
	Store *Store
	// Limit caps fragments pulled from the index per query before collapsing to
	// documents. Default 20.
	Limit int
}

// NewFinder wraps a Store as an agent.DocFinder.
func NewFinder(s *Store) *Finder { return &Finder{Store: s} }

// Find implements agent.DocFinder: it searches the joined conversation text and
// returns one DocHit per matched document (best fragment wins). DocID is the
// document path — stable, and what a fetch tool would use to pull the full doc.
func (f *Finder) Find(ctx context.Context, texts []string) ([]agent.DocHit, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	query := join(texts)
	if query == "" {
		return nil, nil
	}
	// Hybrid (BM25 + vectors) when the store has an embedder; lexical otherwise.
	var hits []Hit
	var err error
	if f.Store.embedder != nil {
		hits, err = f.Store.HybridSearch(ctx, query, limit)
	} else {
		hits, err = f.Store.Search(query, limit)
	}
	if err != nil {
		return nil, err
	}
	// Best fragment per document (Search already returns best-first, so the
	// first fragment seen for a path is its strongest).
	seen := map[string]bool{}
	var out []agent.DocHit
	for _, h := range hits {
		if seen[h.Path] {
			continue
		}
		seen[h.Path] = true
		title := h.Title
		if title == "" {
			title = h.Path
		}
		out = append(out, agent.DocHit{
			DocID: h.Path,
			Title: title,
			Score: h.Score,
			Line:  clip(h.Text, 240),
		})
	}
	return out, nil
}

func join(texts []string) string {
	out := ""
	for _, t := range texts {
		if t == "" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += t
	}
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
