package raglit

import (
	"context"
	"strings"
	"testing"
)

func TestTextWindows(t *testing.T) {
	// Fits in one window.
	if w := textWindows("short", 100); len(w) != 1 || w[0] != "short" {
		t.Fatalf("small text: %v", w)
	}
	// Splits at line boundaries.
	w := textWindows("AAAA\nBBBB\n", 6)
	if len(w) != 2 || w[0] != "AAAA\n" || w[1] != "BBBB\n" {
		t.Fatalf("line-boundary split: %v", w)
	}
	// An over-long single line is emitted alone (no infinite loop).
	long := strings.Repeat("x", 50)
	w = textWindows(long+"\nnext\n", 10)
	if len(w) < 2 || !strings.HasPrefix(w[0], "x") {
		t.Fatalf("over-long line: %v", w)
	}
	// Reassembly is lossless.
	if strings.Join(textWindows("a\nb\nc\nd\n", 4), "") != "a\nb\nc\nd\n" {
		t.Fatal("windowing lost content")
	}
}

func TestWindowCharsFor(t *testing.T) {
	// Budgets for prompt + echoed output (÷2) + margin; grows with context.
	small := WindowCharsFor(4096)
	big := WindowCharsFor(32768)
	if small <= 0 || big <= small {
		t.Fatalf("window sizing: small=%d big=%d", small, big)
	}
}

func TestWindowCharsForHome_ConfigOrDefault(t *testing.T) {
	// Configured small context → window sized to it.
	home := Home(t.TempDir() + "/cfg")
	if err := SaveConfig(home, Config{ContextTokens: 8192}); err != nil {
		t.Fatal(err)
	}
	if got := WindowCharsForHome(home); got != WindowCharsFor(8192) {
		t.Fatalf("configured context ignored: %d", got)
	}

	// Unset → smart default (not a probe, not zero).
	home2 := Home(t.TempDir() + "/fresh")
	if got := WindowCharsForHome(home2); got != WindowCharsFor(defaultContextTokens) {
		t.Fatalf("unset should use smart default: %d", got)
	}
}

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
