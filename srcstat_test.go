package raglit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A document's file size and modified time are recorded, backfilled for rows
// that predate them, and carried in the document list. A URL stays unknown.
func TestSourceStats(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	f := filepath.Join(dir, "a.md")
	if err := os.WriteFile(f, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(f, when, when); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, p := range []string{f, "https://example.com/x.html"} {
		if err := s.Ingest(ctx, Document{Path: p, Title: p, Fragments: []Fragment{{Text: "words"}}}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.BackfillSourceStats()
	if err != nil || n != 1 {
		t.Fatalf("backfilled %d, %v; want 1", n, err)
	}
	docs, err := s.Documents()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		switch d.Path {
		case f:
			if d.SrcSize != 11 || d.SrcMtime != when.Unix() {
				t.Errorf("file stat = %d bytes, %d; want 11, %d", d.SrcSize, d.SrcMtime, when.Unix())
			}
		default:
			if d.SrcSize != 0 || d.SrcMtime != 0 {
				t.Errorf("a URL got a file stat: %+v", d)
			}
		}
	}
	if n, _ := s.BackfillSourceStats(); n != 0 {
		t.Errorf("a second backfill revisited %d filled row(s)", n)
	}
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSourceStat("file://" + f); err != nil {
		t.Fatal(err)
	}
	docs, _ = s.Documents()
	for _, d := range docs {
		if d.Path == f && d.SrcSize != 5 {
			t.Errorf("RecordSourceStat via file:// did not update: %d", d.SrcSize)
		}
	}
}
