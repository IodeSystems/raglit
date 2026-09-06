package main

import (
	"testing"
	"time"
)

func TestSinceNanos(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if got := since(now, 0); got != "n/a" {
		t.Errorf("zero → %q, want n/a", got)
	}
	// 90s ago (in nanos) → "1m".
	if got := since(now, now.Add(-90*time.Second).UnixNano()); got != "1m" {
		t.Errorf("90s ago → %q, want 1m", got)
	}
	// 2 days ago → "2d".
	if got := since(now, now.Add(-48*time.Hour).UnixNano()); got != "2d" {
		t.Errorf("48h ago → %q, want 2d", got)
	}
}
