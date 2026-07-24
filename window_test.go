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

func TestIngestText_WindowsAndSegments(t *testing.T) {
	s := openMem(t)
	sc := &scriptChatter{replies: []string{
		`{"continues_previous":false,"fragments":[{"text":"first window content AAAA"}]}`,
		`{"continues_previous":false,"fragments":[{"text":"second window content BBBB"}]}`,
	}}
	sg := NewSegmenter(sc)
	// maxChars=6 → two windows ("AAAA\n", "BBBB\n"), so two segment calls.
	n, err := s.ingestText(context.Background(), sg, "code.go", "Code", "AAAA\nBBBB\n", 6, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sc.calls != 2 {
		t.Fatalf("expected 2 windows segmented, got %d calls", sc.calls)
	}
	// Both windows' tiny fragments are below the size floor → merged into one.
	if n != 1 {
		t.Fatalf("sub-floor windows should merge into 1 fragment, got %d", n)
	}
	// ...and the merged fragment still contains both windows' content.
	if h, _ := s.Search("AAAA", 5); len(h) == 0 {
		t.Fatal("first window content not searchable")
	}
	if h, _ := s.Search("BBBB", 5); len(h) == 0 {
		t.Fatal("second window content not searchable")
	}
}
