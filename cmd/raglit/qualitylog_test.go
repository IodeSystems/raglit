package main

import (
	"os"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// TestQualityLogRegistry_WriterGate drives the daemon's qualityLogRegistry
// with a real homeResolver and asserts the composition the specs promise:
//
//   - off project (the default): Writer returns nil, no file is created
//   - unregistered namespace: Writer returns nil
//   - empty namespace: Writer returns nil
//   - on project: Writer returns a real writer, events land in the file
//
// This is the test that discriminates: removing the `if !cfg.QualityLog.On`
// gate in qualitylog.go makes the "off" case return a writer and the test
// fails. The package-raglit tests cannot reach this gate (it is in package
// main), so they structurally cannot prove it.

func TestQualityLogRegistry_WriterGate(t *testing.T) {
	// --- Set up two project homes ---
	homeA := raglit.Home(t.TempDir())
	homeB := raglit.Home(t.TempDir())
	if err := homeA.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := homeB.Ensure(); err != nil {
		t.Fatal(err)
	}

	// Project A: log ON.
	cfgA, _, _ := raglit.LoadConfig(homeA)
	cfgA.Project = "alpha"
	cfgA.QualityLog.On = true
	cfgA.QualityLog.MaxBytes = 4096
	if err := raglit.SaveConfig(homeA, cfgA); err != nil {
		t.Fatal(err)
	}

	// Project B: log OFF (the default — zero config, no QualityLog key).
	cfgB, _, _ := raglit.LoadConfig(homeB)
	cfgB.Project = "beta"
	// cfgB.QualityLog.On is false (zero value). Do not change it.
	if err := raglit.SaveConfig(homeB, cfgB); err != nil {
		t.Fatal(err)
	}

	// --- Build the registry with a real homeResolver ---
	registry := newQualityLogRegistry()
	defer registry.Close()
	registry.setHomeResolver(func(ns string) (raglit.Home, bool) {
		switch ns {
		case "alpha":
			return homeA, true
		case "beta":
			return homeB, true
		}
		return "", false
	})

	// --- Case 1: empty namespace → nil ---
	if w := registry.Writer(""); w != nil {
		t.Error("Writer(\"\") should return nil")
	}

	// --- Case 2: unregistered namespace → nil ---
	if w := registry.Writer("gamma"); w != nil {
		t.Error("Writer(\"gamma\") should return nil for unregistered namespace")
	}

	// --- Case 3: off project (the default) → nil, no file created ---
	if w := registry.Writer("beta"); w != nil {
		t.Error("Writer(\"beta\") should return nil when QualityLog.On is false")
	}
	if _, err := os.Stat(homeB.QualityLogPath()); !os.IsNotExist(err) {
		t.Errorf("project B should have no quality log file when off; stat err=%v", err)
	}

	// --- Case 4: on project → real writer, event lands ---
	wA := registry.Writer("alpha")
	if wA == nil {
		t.Fatal("Writer(\"alpha\") should return a writer when QualityLog.On is true")
	}

	// Record a search event through the writer the registry returned.
	qh := []raglit.QualityHit{
		{DocID: "doc-a.md", Rank: 1, Score: 0.91, Origin: "body", Chars: 11},
		{DocID: "doc-b.md", Rank: 2, Score: 0.82, Origin: "identity", Chars: 5},
	}
	if err := wA.Record(raglit.QualityEvent{
		Type:    "search",
		Project: "alpha",
		Session: "sess1",
		Query:   "test query",
		Mode:    "semantic",
		Index:   "alpha__main",
		Hits:    qh,
	}); err != nil {
		t.Fatal(err)
	}

	// Read the file back.
	data, err := os.ReadFile(homeA.QualityLogPath())
	if err != nil {
		t.Fatalf("expected quality log file for project A: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 event line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "test query") {
		t.Errorf("event line should contain the query; got %s", lines[0])
	}
	if !strings.Contains(lines[0], "doc-a.md") {
		t.Errorf("event line should contain hit doc id; got %s", lines[0])
	}
}
