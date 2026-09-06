package raglit

import (
	"context"
	"strings"
	"testing"
)

func TestIngestText_DeterministicOverlap(t *testing.T) {
	s := openMem(t)
	// Long enough to span several small windows; small FragConfig forces overlap.
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = strings.Repeat("word ", 6)
	}
	text := strings.Join(lines, "\n")
	fc := FragConfig{Window: 120, Stride: 80, Floor: 30}

	n, mode, err := s.ingestText(context.Background(), "code.txt", "Code", text, fc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "text-overlap" {
		t.Fatalf("mode = %q, want text-overlap", mode)
	}
	if n < 2 {
		t.Fatalf("long text should split into multiple overlapping fragments, got %d", n)
	}

	// Fragments carry source offsets, and get_document reassembles the source
	// EXACTLY ONCE despite the overlap.
	doc, err := s.DocText("code.txt", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Text != text {
		t.Fatalf("reassembly not exact:\n got %q\nwant %q", doc.Text, text)
	}
}

// checkCoverage asserts the fragments' spans are valid, monotdonic, gap-free, and
// that their text matches the source slice — the invariants get_document relies on.
func checkCoverage(t *testing.T, src string, frags []OffsetFrag) {
	t.Helper()
	if len(frags) == 0 {
		if src != "" {
			t.Fatal("no fragments for non-empty source")
		}
		return
	}
	covered := 0
	for i, f := range frags {
		if f.Start < 0 || f.End > len(src) || f.Start >= f.End {
			t.Fatalf("frag %d bad span [%d,%d) of %d", i, f.Start, f.End, len(src))
		}
		if src[f.Start:f.End] != f.Text {
			t.Fatalf("frag %d text != source slice", i)
		}
		if f.Start > covered {
			t.Fatalf("frag %d opens a gap: start %d > covered %d", i, f.Start, covered)
		}
		if f.End > covered {
			covered = f.End
		}
	}
	if covered != len(src) {
		t.Fatalf("coverage %d != source length %d", covered, len(src))
	}
}

func TestOverlapFragments_ShortIsSingle(t *testing.T) {
	src := "a short document"
	w, s, f := ResolveFragParams(9000, 6000, 3000, 0)
	got := OverlapFragments(src, w, s, f)
	if len(got) != 1 || got[0].Start != 0 || got[0].End != len(src) {
		t.Fatalf("short doc should be one full fragment, got %+v", got)
	}
}

func TestOverlapFragments_OverlapAndCoverage(t *testing.T) {
	// Many lines → several overlapping windows.
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString(strings.Repeat("word ", 8))
		b.WriteByte('\n')
	}
	src := b.String()
	w, s, f := ResolveFragParams(300, 200, 80, 0)
	got := OverlapFragments(src, w, s, f)
	if len(got) < 3 {
		t.Fatalf("expected several windows, got %d", len(got))
	}
	checkCoverage(t, src, got)
	// Consecutive windows must actually overlap (stride < window).
	overlaps := 0
	for i := 1; i < len(got); i++ {
		if got[i].Start < got[i-1].End {
			overlaps++
		}
	}
	if overlaps == 0 {
		t.Fatal("no overlap between consecutive windows")
	}
}

func TestOverlapFragments_BarelyOverFoldsTail(t *testing.T) {
	// A document just past one window must NOT emit a tiny near-duplicate second
	// window; the tail folds into the first (still a single fragment).
	// After the first window [0,100), the next window would start at ~70, leaving a
	// ~37-char tail — below the floor (40) → folded into the first fragment.
	src := strings.Repeat("x", 100) + "\n" + strings.Repeat("y", 7)
	got := OverlapFragments(src, 100, 70, 40)
	if len(got) != 1 || got[0].End != len(src) {
		t.Fatalf("barely-over doc should fold into one fragment, got %d: %+v", len(got), got)
	}
}

func TestResolveFragParams_Invariants(t *testing.T) {
	// Defaults applied for non-positive values.
	w, s, f := ResolveFragParams(0, 0, 0, 0)
	if w != defaultFragWindow || s != defaultFragStride || f != defaultFragFloor {
		t.Fatalf("defaults not applied: %d/%d/%d", w, s, f)
	}
	// Embed limit caps the window (and stride is clamped to ≤¾ window → no gaps).
	w, s, _ = ResolveFragParams(9000, 6000, 3000, 4000)
	if w != 4000 || s > w*3/4 {
		t.Fatalf("embed cap/stride clamp failed: w=%d s=%d", w, s)
	}
	// Stride never exceeds the gap-safe bound even if asked to.
	w, s, _ = ResolveFragParams(100, 999, 10, 0)
	if s > w*3/4 {
		t.Fatalf("stride not clamped: w=%d s=%d", w, s)
	}
}
