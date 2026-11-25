package raglit

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// fixtureQualityEvents are the events an export must survive: one search with
// three hits (three origins, three sizes) and one get_document fetch that
// joins back to it by session.
func fixtureQualityEvents() []QualityEvent {
	return []QualityEvent{
		{
			Type:    "search",
			Project: "proj",
			Session: "session-1",
			Query:   "what is the protocol",
			Mode:    "hybrid",
			Index:   "proj:main",
			Hits: []QualityHit{
				{DocID: "/home/someone/docs/protocol.pdf", Rank: 1, Score: 0.92, Origin: "body", Chars: 4500},
				{DocID: "/home/someone/docs/protocol.pdf", Rank: 2, Score: 0.81, Origin: "identity", Chars: 200},
				{DocID: "/home/someone/docs/faq.txt", Rank: 3, Score: 0.74, Origin: "generated question", Chars: 1200},
			},
		},
		{
			Type:      "get_document",
			Project:   "proj",
			Session:   "session-1",
			Path:      "/home/someone/docs/protocol.pdf",
			DocID:     "/home/someone/docs/protocol.pdf",
			IndexHit:  "proj:main",
			Chars:     4500,
			FetchedBy: "session-1",
		},
	}
}

// TestQualityLog_AnonymizeExport_HashesPreservedFields is the composition
// test for the anonymize export: it drives the log the way the daemon writes
// it, reads it back through the export path, and asserts what the spec says
// leaves and what it says stays.
func TestQualityLog_AnonymizeExport_HashesPreservedFields(t *testing.T) {
	home := Home(t.TempDir())
	w, err := NewQualityLogWriter(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	evs := fixtureQualityEvents()
	for _, ev := range evs {
		if err := w.Record(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	k, err := NewAnonKey()
	if err != nil {
		t.Fatal(err)
	}
	out, err := AnonymizeExport(home, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(evs) {
		t.Fatalf("export has %d events, want %d", len(out), len(evs))
	}

	// WHAT LEAVES: no original path, query, id or session survives the
	// export, in any field.
	leaving, err := jsonLines(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"/home/someone/docs/protocol.pdf",
		"/home/someone/docs/faq.txt",
		"what is the protocol",
		"session-1",
	} {
		if strings.Contains(leaving, secret) {
			t.Errorf("the export still contains %q — it was not replaced", secret)
		}
	}

	// WHAT STAYS: ranks, scores, origins, sizes, mode and index scope, and
	// the fetch/use flag, byte for byte.
	s, f := out[0], out[1]
	if s.Type != "search" || f.Type != "get_document" {
		t.Fatalf("event types drifted: %q, %q", s.Type, f.Type)
	}
	if s.Mode != "hybrid" || s.Index != "proj:main" {
		t.Errorf("mode/index not preserved: %q %q", s.Mode, s.Index)
	}
	if len(s.Hits) != 3 {
		t.Fatalf("want 3 hits in the export, got %d", len(s.Hits))
	}
	for i, want := range []QualityHit{
		{Rank: 1, Score: 0.92, Origin: "body", Chars: 4500},
		{Rank: 2, Score: 0.81, Origin: "identity", Chars: 200},
		{Rank: 3, Score: 0.74, Origin: "generated question", Chars: 1200},
	} {
		g := s.Hits[i]
		if g.Rank != want.Rank || g.Score != want.Score || g.Origin != want.Origin || g.Chars != want.Chars {
			t.Errorf("hit[%d] = %+v, want %+v (preserved fields changed)", i, g, want)
		}
	}
	if f.Chars != 4500 || f.IndexHit != "proj:main" {
		t.Errorf("fetch size/index not preserved: %d %q", f.Chars, f.IndexHit)
	}

	// STABLE FOR THIS EXPORT: the same path in two hits and in the fetch
	// gets one value; the same session in the event and in FetchedBy gets
	// one value, so the search → fetch chain still joins.
	if s.Hits[0].DocID != s.Hits[1].DocID {
		t.Error("the same path hashed to two values inside one export")
	}
	if s.Hits[0].DocID == s.Hits[2].DocID {
		t.Error("two different paths hashed to the same value")
	}
	if s.Hits[0].DocID != f.DocID || s.Hits[0].DocID != f.Path {
		t.Error("the path in the search hits and in the fetch did not match — the join is broken")
	}
	if s.Session != f.Session || s.Session != f.FetchedBy {
		t.Error("the session id did not join its fetch inside the export")
	}
	// Kinds differ: a query and a path with the same text must not collide.
	if k.Hash("query", "x") == k.Hash("path", "x") {
		t.Error("the kind tag is not separating value kinds")
	}

	// PER-EXPORT: a second key for the same log shares no values with the
	// first, so exports cannot be merged back into each other.
	k2, err := NewAnonKey()
	if err != nil {
		t.Fatal(err)
	}
	out2, err := AnonymizeExport(home, k2)
	if err != nil {
		t.Fatal(err)
	}
	if out2[0].Session == out[0].Session || out2[0].Hits[0].DocID == out[0].Hits[0].DocID ||
		out2[1].Path == out[1].Path {
		t.Error("two exports of the same log share a hash value — hashes are not per-export")
	}

	// The absence case: a project whose log is off exports nothing, and the
	// export does not create the log file on its way out.
	empty := Home(t.TempDir())
	got, err := AnonymizeExport(empty, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a project with no log exported %d events, want 0", len(got))
	}
	if _, err := os.Stat(empty.QualityLogPath()); !os.IsNotExist(err) {
		t.Errorf("the export created a log file in a project that has none; stat err=%v", err)
	}
}

// TestQualityLog_AnonymizeExport_ReadsRotatedGenerations: a log that rotated
// exports every generation, oldest first, so nothing that was logged leaves
// unexported.
func TestQualityLog_AnonymizeExport_ReadsRotatedGenerations(t *testing.T) {
	home := Home(t.TempDir())
	if err := os.MkdirAll(string(home), 0o755); err != nil {
		t.Fatal(err)
	}
	const cap = 128
	w, err := NewQualityLogWriter(home, cap)
	if err != nil {
		t.Fatal(err)
	}
	// Four events, exactly three rotations: .1, .2 and the current file
	// each hold one, and nothing is dropped (the design bounds the log at
	// three files; a fourth rotation would have lost the oldest).
	for i := 0; i < 4; i++ {
		if err := w.Record(QualityEvent{
			Type:    "search",
			Project: "rot",
			Query:   strings.Repeat("q", 40),
			Hits:    []QualityHit{{DocID: "d", Rank: 1, Score: 0.5, Origin: "body", Chars: 10}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// The log must have rotated (a .1 generation exists) for this test to
	// say anything.
	if _, err := os.Stat(home.QualityLogPath() + ".1"); err != nil {
		t.Fatalf("setup: no rotation happened (cap %d): %v", cap, err)
	}

	k, err := NewAnonKey()
	if err != nil {
		t.Fatal(err)
	}
	out, err := AnonymizeExport(home, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Errorf("the export has %d events, want 4 (every generation that exists)", len(out))
	}
	// Every event is anonymized, none is raw.
	leaving, err := jsonLines(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(leaving, strings.Repeat("q", 40)) {
		t.Error("a raw query survived the export of a rotated log")
	}
}

// TestQualityLog_AnonymizeExport_OriginalLogUntouched: the export reads the
// log; it does not rewrite it. The local file keeps what it had.
func TestQualityLog_AnonymizeExport_OriginalLogUntouched(t *testing.T) {
	home := Home(t.TempDir())
	w, err := NewQualityLogWriter(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Record(QualityEvent{Type: "search", Project: "p", Query: "keep me local"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(home.QualityLogPath())
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewAnonKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AnonymizeExport(home, k); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(home.QualityLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the export modified the local log; it must only read it")
	}
}

func jsonLines(evs []QualityEvent) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for i := range evs {
		if err := enc.Encode(evs[i]); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}
