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

	// The serve `search` tool renders hits via taggedHits (index-tagged, but the
	// same doc_id/title/score/snippet shape ragnotify.ParseHits consumes).
	ih := make([]indexedHit, len(hits))
	for i, h := range hits {
		ih[i] = indexedHit{index: "default", hit: h}
	}
	payload, err := json.Marshal(taggedHits(ih))
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := ragnotify.ParseHits(string(payload))
	if err != nil {
		t.Fatalf("ragnotify could not parse raglit output: %v\npayload: %s", err, payload)
	}
	if len(parsed) != 1 {
		t.Fatalf("want 1 DocHit, got %d", len(parsed))
	}
	if parsed[0].DocID != "auth.md" || parsed[0].Title != "Auth" || parsed[0].Line == "" || parsed[0].Score <= 0 {
		t.Fatalf("round-trip lost fields: %+v", parsed[0])
	}
}
