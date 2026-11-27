package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/iodesystems/agentkit/ragnotify"
	"github.com/iodesystems/raglit"
)

// Loop-closure: raglit's MCP `search` output must be parseable by agentkit's
// ragnotify.ParseHits, so ONE raglit server drives both the explicit tool and
// the proactive DocFinder channel. This pins the shared wire contract in code.
func TestServeOutput_ParsedByRagnotify(t *testing.T) {
	s, err := raglit.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Ingest(context.Background(), raglit.Document{Path: "auth.md", Title: "Auth", Fragments: []raglit.Fragment{
		{Page: 3, Ord: 0, Text: "the refresh token rotates on each use"},
	}}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("refresh token", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %v (%d hits)", err, len(hits))
	}

	// The MCP `search` handler renders the daemon's full hitRow shape (with
	// score) and trims it via trimSearchHits before it reaches a client. Feed the
	// TRIMMED output to ParseHits — the economical shape a ragnotify consumer
	// actually receives — not the untrimmed renderer, so the test exercises the
	// trim: it passes against the trim and fails if the trim is removed.
	rows := make([]hitRow, len(hits))
	for i, h := range hits {
		rows[i] = hitRow{
			Index:        "default",
			DocID:        h.Path,
			Title:        h.Title,
			Page:         h.Page,
			Score:        h.Score,
			Snippet:      h.Text,
			Origin:       h.Origin,
			Rel:          h.Path,
			Similarity:   h.Similarity,
			MatchedTerms: h.Matched,
			QueryTerms:   h.QueryTerms,
		}
	}
	full, err := json.Marshal(map[string]any{"hits": rows})
	if err != nil {
		t.Fatal(err)
	}
	payload := trimSearchHits(full)

	parsed, err := ragnotify.ParseHits(string(payload))
	if err != nil {
		t.Fatalf("ragnotify could not parse raglit output: %v\npayload: %s", err, payload)
	}
	if len(parsed) != 1 {
		t.Fatalf("want 1 DocHit, got %d", len(parsed))
	}
	// The stable fields survive the trim unrenamed; score is an internal field
	// the economical trim drops, so it parses to 0 (a trimmed hit carries no
	// score for a client-side MinScore cut — the server's ranking + MaxHits do
	// the trimming).
	if parsed[0].DocID != "auth.md" || parsed[0].Title != "Auth" || parsed[0].Line == "" || parsed[0].Score != 0 {
		t.Fatalf("round-trip lost fields or kept an internal field: %+v", parsed[0])
	}
}
