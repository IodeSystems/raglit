package main

import (
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
	if err := s.Ingest(raglit.Document{Path: "auth.md", Title: "Auth", Fragments: []raglit.Fragment{
		{Page: 3, Ord: 0, Text: "the refresh token rotates on each use"},
	}}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("refresh token", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %v (%d hits)", err, len(hits))
	}

	payload, err := hitsJSON(hits)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := ragnotify.ParseHits(payload)
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
