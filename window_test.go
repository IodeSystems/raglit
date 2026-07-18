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
	small := windowCharsFor(4096)
	big := windowCharsFor(32768)
	if small <= 0 || big <= small {
		t.Fatalf("window sizing: small=%d big=%d", small, big)
	}
}

type fakeDiscoverer struct {
	n     int
	err   error
	calls int
}

func (f *fakeDiscoverer) DiscoverContext(_ context.Context) (int, error) {
	f.calls++
	return f.n, f.err
}

func TestResolveWindowChars_CachesAndFallsBack(t *testing.T) {
	ctx := context.Background()

	// Cached: config already has ContextTokens → no probe.
	home := Home(t.TempDir() + "/cached")
	if err := SaveConfig(home, Config{ContextTokens: 8192}); err != nil {
		t.Fatal(err)
	}
	d := &fakeDiscoverer{n: 999999}
	if got := ResolveWindowChars(ctx, d, home); got != windowCharsFor(8192) || d.calls != 0 {
		t.Fatalf("cached should not probe: got=%d calls=%d", got, d.calls)
	}

	// Uncached: probes once and caches the result.
	home2 := Home(t.TempDir() + "/fresh")
	d2 := &fakeDiscoverer{n: 16384}
	if got := ResolveWindowChars(ctx, d2, home2); got != windowCharsFor(16384) || d2.calls != 1 {
		t.Fatalf("uncached should probe once: got=%d calls=%d", got, d2.calls)
	}
	if cfg, _, _ := LoadConfig(home2); cfg.ContextTokens != 16384 {
		t.Fatalf("probe result not cached: %+v", cfg)
	}

	// Probe error → default window, no crash.
	home3 := Home(t.TempDir() + "/err")
	d3 := &fakeDiscoverer{err: context.DeadlineExceeded}
	if got := ResolveWindowChars(ctx, d3, home3); got != defaultWindowChars {
		t.Fatalf("probe failure should fall back to default, got %d", got)
	}
}

func TestIngestText_WindowsAndSegments(t *testing.T) {
	s := openMem(t)
	sg := NewSegmenter(&scriptChatter{replies: []string{
		`{"continues_previous":false,"fragments":[{"text":"first window content AAAA"}]}`,
		`{"continues_previous":false,"fragments":[{"text":"second window content BBBB"}]}`,
	}})
	// maxChars=6 → two windows ("AAAA\n", "BBBB\n").
	n, err := s.ingestText(context.Background(), sg, "code.go", "Code", "AAAA\nBBBB\n", 6)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 fragments (one per window), got %d", n)
	}
	if h, _ := s.Search("AAAA", 5); len(h) == 0 {
		t.Fatal("first window not searchable")
	}
	if h, _ := s.Search("BBBB", 5); len(h) == 0 {
		t.Fatal("second window not searchable")
	}
}
