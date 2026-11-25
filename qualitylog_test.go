package raglit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestQualityLog_OffByDefault_WritesNothing: a zero Config has QualityLog.On
// false, and the daemon's registry (cmd/raglit) checks that gate before
// creating a writer. This test proves the contract at the config level: a
// zero config means no file is ever created.
func TestQualityLog_OffByDefault_WritesNothing(t *testing.T) {
	home := Home(t.TempDir())
	cfg, _, _ := LoadConfig(home)

	// Zero config: On is false.
	if cfg.QualityLog.On {
		t.Fatal("QualityLog.On must be false by default")
	}

	// The daemon checks On before calling NewQualityLogWriter. Simulate
	// that gate: if On is false, no writer is created, no file exists.
	if cfg.QualityLog.On {
		w, err := NewQualityLogWriter(home, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		_ = w.Record(QualityEvent{Type: "search", Project: "p", Query: "q"})
	}

	if _, err := os.Stat(home.QualityLogPath()); !os.IsNotExist(err) {
		t.Fatalf("quality-log.jsonl should not exist when log is off; stat err=%v", err)
	}
}

// TestQualityLog_SearchCall_LoggedWithHits: a search event with hits is
// written as one JSON line with query, mode, index, and each hit's doc_id,
// rank, score, origin, and char count.
func TestQualityLog_SearchCall_LoggedWithHits(t *testing.T) {
	home := Home(t.TempDir())
	if err := os.MkdirAll(string(home), 0o755); err != nil {
		t.Fatal(err)
	}

	w, err := NewQualityLogWriter(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ev := QualityEvent{
		Type:    "search",
		Project: "proj1",
		Query:   "what is the protocol",
		Mode:    "semantic",
		Index:   "proj1__main",
		Hits: []QualityHit{
			{DocID: "doc-a", Rank: 1, Score: 0.92, Origin: "body", Chars: 4500},
			{DocID: "doc-b", Rank: 2, Score: 0.81, Origin: "identity", Chars: 200},
			{DocID: "doc-c", Rank: 3, Score: 0.74, Origin: "generated question", Chars: 1200},
		},
	}
	if err := w.Record(ev); err != nil {
		t.Fatal(err)
	}

	// Read the file back and verify the JSON line.
	data, err := os.ReadFile(home.QualityLogPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	var got QualityEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "search" {
		t.Errorf("Type = %q, want search", got.Type)
	}
	if got.Query != "what is the protocol" {
		t.Errorf("Query = %q", got.Query)
	}
	if got.Mode != "semantic" {
		t.Errorf("Mode = %q", got.Mode)
	}
	if got.Index != "proj1__main" {
		t.Errorf("Index = %q", got.Index)
	}
	if len(got.Hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(got.Hits))
	}
	for i, want := range []QualityHit{
		{DocID: "doc-a", Rank: 1, Score: 0.92, Origin: "body", Chars: 4500},
		{DocID: "doc-b", Rank: 2, Score: 0.81, Origin: "identity", Chars: 200},
		{DocID: "doc-c", Rank: 3, Score: 0.74, Origin: "generated question", Chars: 1200},
	} {
		g := got.Hits[i]
		if g.DocID != want.DocID || g.Rank != want.Rank || g.Score != want.Score ||
			g.Origin != want.Origin || g.Chars != want.Chars {
			t.Errorf("hit[%d] = %+v, want %+v", i, g, want)
		}
	}
}

// TestQualityLog_RotatesAtSizeCap: a writer with a small cap rotates when the
// file would exceed it. After rotation the current file is small, a .1
// generation exists, and the total on disk is bounded by ~3× cap.
func TestQualityLog_RotatesAtSizeCap(t *testing.T) {
	home := Home(t.TempDir())
	if err := os.MkdirAll(string(home), 0o755); err != nil {
		t.Fatal(err)
	}

	const cap = 256 // bytes — small enough that a few events overflow it
	w, err := NewQualityLogWriter(home, cap)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Write enough events to force at least one rotation.
	for i := 0; i < 20; i++ {
		ev := QualityEvent{
			Type:    "search",
			Project: "rot",
			Query:   strings.Repeat("x", 60),
			Hits:    []QualityHit{{DocID: "d", Rank: 1, Score: 0.5, Origin: "body", Chars: 100}},
		}
		if err := w.Record(ev); err != nil {
			t.Fatal(err)
		}
	}

	path := home.QualityLogPath()

	// The current file must be at most cap bytes.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > int64(cap) {
		t.Errorf("current file is %d bytes, cap is %d", fi.Size(), cap)
	}

	// A .1 generation must exist (at least one rotation happened).
	if _, err := os.Stat(path + ".1"); os.IsNotExist(err) {
		t.Error("expected quality-log.jsonl.1 after rotation")
	}

	// Total on disk is bounded: at most 3 files of at most cap each.
	total := fi.Size()
	for n := 1; n <= 3; n++ {
		if fi2, err := os.Stat(filepath.Join(string(home), "quality-log.jsonl."+string(rune('0'+n)))); err == nil {
			total += fi2.Size()
		}
	}
	// Bound: current file (up to cap) + .1 + .2 + .3, each up to cap = 4×cap.
	if total > 4*int64(cap) {
		t.Errorf("total on disk is %d bytes, bound is %d (4×%d)", total, 4*cap, cap)
	}

	// The current file is valid JSONL: every line parses.
	lines := readLines(t, path)
	for i, line := range lines {
		var ev QualityEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

// TestQualityLog_PerProject_OnlyLogsConfiguredProject: two projects share a
// daemon. Project A has the log on; project B has it off (the default).
// Searching in A writes events; searching in B writes nothing.
func TestQualityLog_PerProject_OnlyLogsConfiguredProject(t *testing.T) {
	// Project A: log on.
	homeA := Home(t.TempDir())
	if err := os.MkdirAll(string(homeA), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgA, _, _ := LoadConfig(homeA)
	cfgA.QualityLog.On = true
	cfgA.QualityLog.MaxBytes = 1024
	if err := SaveConfig(homeA, cfgA); err != nil {
		t.Fatal(err)
	}

	// Project B: log off (zero config).
	homeB := Home(t.TempDir())
	if err := os.MkdirAll(string(homeB), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgB, _, _ := LoadConfig(homeB)
	// cfgB.QualityLog.On is false (zero value) — do not change it.

	// Simulate the daemon's gate for each project: the registry checks
	// On before creating a writer.
	var wA, wB *QualityLogWriter
	if cfgA.QualityLog.On {
		var err error
		wA, err = NewQualityLogWriter(homeA, cfgA.QualityLog.MaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		defer wA.Close()
	}
	if cfgB.QualityLog.On {
		var err error
		wB, err = NewQualityLogWriter(homeB, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer wB.Close()
	}

	// Search in project A.
	if err := wA.Record(QualityEvent{Type: "search", Project: "A", Query: "alpha"}); err != nil {
		t.Fatal(err)
	}

	// Search in project B (wB is nil — the gate blocked it).
	if wB != nil {
		t.Fatal("project B should not have a writer when log is off")
	}

	// Project A has a file with the event.
	dataA, err := os.ReadFile(homeA.QualityLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dataA), "alpha") {
		t.Error("project A's log should contain the query")
	}

	// Project B has no file.
	if _, err := os.Stat(homeB.QualityLogPath()); !os.IsNotExist(err) {
		t.Errorf("project B should have no quality log file; stat err=%v", err)
	}
}

// TestQualityLog_FetchJoinsToOriginatingSearch: a search event is written,
// then some of its hits are fetched via get_document in the same session.
// JoinFetchesToSearch resolves each hit's fate: fetched hits are marked
// "fetched", the rest "never_touched".
func TestQualityLog_FetchJoinsToOriginatingSearch(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)

	search := QualityEvent{
		Type:    "search",
		Project: "proj",
		Time:    base,
		Session: "sess-1",
		Query:   "purchase agreement",
		Mode:    "semantic",
		Index:   "proj__main",
		Hits: []QualityHit{
			{DocID: "agreement.md", Rank: 1, Score: 0.92, Origin: "body", Chars: 4500},
			{DocID: "notes.md", Rank: 2, Score: 0.81, Origin: "identity", Chars: 200},
			{DocID: "summary.md", Rank: 3, Score: 0.74, Origin: "generated question", Chars: 1200},
		},
	}

	// Two of the three hits are fetched in the same session, after the search.
	fetch1 := QualityEvent{
		Type:    "get_document",
		Project: "proj",
		Time:    base.Add(30 * time.Second),
		Session: "sess-1",
		Path:    "agreement.md",
		DocID:   "agreement.md",
		Chars:   4500,
	}
	fetch2 := QualityEvent{
		Type:    "get_document",
		Project: "proj",
		Time:    base.Add(60 * time.Second),
		Session: "sess-1",
		Path:    "summary.md",
		DocID:   "summary.md",
		Chars:   1200,
	}

	// A fetch in a DIFFERENT session must not join.
	fetchOther := QualityEvent{
		Type:    "get_document",
		Project: "proj",
		Time:    base.Add(45 * time.Second),
		Session: "sess-other",
		Path:    "notes.md",
		DocID:   "notes.md",
		Chars:   200,
	}

	// A fetch BEFORE the search must not join.
	fetchEarly := QualityEvent{
		Type:    "get_document",
		Project: "proj",
		Time:    base.Add(-10 * time.Second),
		Session: "sess-1",
		Path:    "notes.md",
		DocID:   "notes.md",
		Chars:   200,
	}

	events := []QualityEvent{search, fetch1, fetch2, fetchOther, fetchEarly}
	joins := JoinFetchesToSearch(events)

	// One search event → one join.
	if len(joins) != 1 {
		t.Fatalf("want 1 search join, got %d", len(joins))
	}
	j := joins[0]
	if len(j.Hits) != 3 {
		t.Fatalf("want 3 hits in join, got %d", len(j.Hits))
	}

	// agreement.md was fetched in the same session after the search → "fetched".
	if j.Hits[0].Fate != "fetched" {
		t.Errorf("agreement.md fate = %q, want fetched", j.Hits[0].Fate)
	}
	// notes.md was fetched in a different session and before the search → "never_touched".
	if j.Hits[1].Fate != "never_touched" {
		t.Errorf("notes.md fate = %q, want never_touched", j.Hits[1].Fate)
	}
	// summary.md was fetched in the same session after the search → "fetched".
	if j.Hits[2].Fate != "fetched" {
		t.Errorf("summary.md fate = %q, want fetched", j.Hits[2].Fate)
	}

	// Hit metadata is preserved through the join.
	if j.Hits[0].DocID != "agreement.md" || j.Hits[0].Rank != 1 || j.Hits[0].Score != 0.92 {
		t.Errorf("hit[0] metadata = %+v", j.Hits[0])
	}
	if j.Hits[1].DocID != "notes.md" || j.Hits[1].Origin != "identity" {
		t.Errorf("hit[1] metadata = %+v", j.Hits[1])
	}

	// Empty/absence case: a search with no fetches at all → all never_touched.
	alone := QualityEvent{
		Type:    "search",
		Project: "proj",
		Time:    base,
		Session: "sess-2",
		Query:   "lonely query",
		Hits:    []QualityHit{{DocID: "x.md", Rank: 1, Score: 0.5, Origin: "body", Chars: 10}},
	}
	joins2 := JoinFetchesToSearch([]QualityEvent{alone})
	if len(joins2) != 1 {
		t.Fatalf("want 1 join for lone search, got %d", len(joins2))
	}
	if joins2[0].Hits[0].Fate != "never_touched" {
		t.Errorf("lone search hit fate = %q, want never_touched", joins2[0].Hits[0].Fate)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}
