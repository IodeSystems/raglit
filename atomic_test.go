package raglit

import (
	"context"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// failAfterChatter returns `ok` canned replies, then errors on every call after
// — to simulate the LLM going offline partway through a multi-unit document.
type failAfterChatter struct {
	replies []string
	n       int
}

func (c *failAfterChatter) Chat(_ context.Context, _ []llm.Message, _ []llm.ToolDef) (string, []llm.ToolCall, error) {
	if c.n < len(c.replies) {
		r := c.replies[c.n]
		c.n++
		return r, nil, nil
	}
	c.n++
	return "", nil, context.DeadlineExceeded // stand-in for "LLM unreachable"
}

// TestIngestUnits_FailedReingestKeepsPriorVersion is the torn-write regression:
// a reingest whose LLM call fails on a later unit must leave the PRIOR indexed
// document completely intact — not a half-updated / partially-cleared one.
func TestIngestUnits_FailedReingestKeepsPriorVersion(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()
	pad := " " + strings.Repeat("lorem ipsum dolor sit amet ", 200)

	// 1) Successful first ingest: two text pages (born-digital), one fragment each.
	ok := NewSegmenter(&failAfterChatter{replies: []string{
		`{"continues_previous":false,"fragments":[{"text":"alpha original` + pad + `"}]}`,
		`{"continues_previous":false,"fragments":[{"text":"beta original` + pad + `"}]}`,
	}})
	units := []ingestUnit{
		{page: 1, text: "alpha source"},
		{page: 2, text: "beta source"},
	}
	if n, err := s.ingestUnits(ctx, ok, nil, "doc.pdf", "Doc", units, nil); err != nil || n != 2 {
		t.Fatalf("first ingest: n=%d err=%v (want 2, nil)", n, err)
	}
	fragsBefore := countRows(t, s, `SELECT COUNT(*) FROM fragments`)
	vecsBefore := countRows(t, s, `SELECT COUNT(*) FROM fragment_vectors`)
	pagesBefore := countRows(t, s, `SELECT COUNT(*) FROM ocr_pages`)
	if fragsBefore != 2 || vecsBefore != 2 || pagesBefore != 2 {
		t.Fatalf("before: frags=%d vecs=%d pages=%d, want 2/2/2", fragsBefore, vecsBefore, pagesBefore)
	}

	// 2) Reingest the same doc, but the segmenter succeeds on page 1 and then
	//    fails (LLM offline) on page 2.
	dying := NewSegmenter(&failAfterChatter{replies: []string{
		`{"continues_previous":false,"fragments":[{"text":"alpha REPLACED` + pad + `"}]}`,
	}})
	if _, err := s.ingestUnits(ctx, dying, nil, "doc.pdf", "Doc", units, nil); err == nil {
		t.Fatal("reingest with a mid-way LLM failure should error")
	}

	// 3) The prior version must be fully intact — no torn write.
	if got := countRows(t, s, `SELECT COUNT(*) FROM fragments`); got != fragsBefore {
		t.Fatalf("fragments after failed reingest = %d, want %d (unchanged)", got, fragsBefore)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM fragment_vectors`); got != vecsBefore {
		t.Fatalf("vectors after failed reingest = %d, want %d (unchanged)", got, vecsBefore)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM ocr_pages`); got != pagesBefore {
		t.Fatalf("pages after failed reingest = %d, want %d (unchanged)", got, pagesBefore)
	}
	// The original text is still searchable; the half-written replacement is not.
	if h, _ := s.Search("alpha original", 3); len(h) == 0 {
		t.Fatal("original fragment lost after a failed reingest")
	}
	if h, _ := s.Search("REPLACED", 3); len(h) != 0 {
		t.Fatal("partial replacement leaked into the index (torn write)")
	}
}

func countRows(t *testing.T, s *Store, q string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
