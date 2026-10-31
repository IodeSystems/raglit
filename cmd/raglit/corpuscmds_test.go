package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// Commands that used to open the index file in-process now reach it through
// the daemon (USER 2026-10-30). These run each one against a test daemon, so
// a route that is missing, misnamed or wired to the wrong index fails here.

// indexWithTwoDocuments arranges <dir>/a.md and a byte-identical <dir>/b.md,
// both on disk (search leaves out a document whose file is gone).
func indexWithTwoDocuments(t *testing.T) (testDB, string) {
	t.Helper()
	db := newTestDB(t)
	dir := t.TempDir()
	st, err := raglit.Open(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for _, p := range []string{dir + "/a.md", dir + "/b.md"} {
		must(t, os.WriteFile(p, []byte("the walrus deed of trust, recorded"), 0o644))
		must(t, st.Ingest(ctx, raglit.Document{Path: p, Title: p,
			Fragments: []raglit.Fragment{{Page: 1, Text: "the walrus deed of trust, recorded"}}}))
		must(t, st.SetDocumentHash(p, "same-bytes"))
	}
	return db, dir
}

func run(t *testing.T, fn func([]string) error, args []string) string {
	t.Helper()
	return captureStdout(t, func() {
		if err := fn(args); err != nil {
			t.Fatal(err)
		}
	})
}

func TestForget_RemovesThroughTheDaemon(t *testing.T) {
	db, dir := indexWithTwoDocuments(t)
	out := run(t, runForget, db.with(dir+"/a.md"))
	if !strings.Contains(out, "forgot 1 document(s)") {
		t.Fatalf("forget:\n%s", out)
	}
	st, _ := raglit.Open(db.path)
	defer st.Close()
	docs, _ := st.Documents()
	if len(docs) != 1 || docs[0].Path != dir+"/b.md" {
		t.Fatalf("documents after forget = %+v, want only b.md", docs)
	}
}

func TestWithdraw_DryRunAndWithdrawnList(t *testing.T) {
	db, dir := indexWithTwoDocuments(t)
	if out := run(t, runWithdraw, db.with("--dry-run", dir+"/a.md")); !strings.Contains(out, "would withdraw 1") {
		t.Fatalf("withdraw --dry-run:\n%s", out)
	}
	st, _ := raglit.Open(db.path)
	must(t, st.Withdraw(raglit.Withdrawal{Path: dir + "/b.md", Reason: "a draft", By: "t"}))
	st.Close()
	if out := run(t, runWithdrawn, db.with()); !strings.Contains(out, "b.md") || !strings.Contains(out, "a draft") {
		t.Fatalf("withdrawn:\n%s", out)
	}
}

func TestHealth_ReportsThroughTheDaemon(t *testing.T) {
	db, _ := indexWithTwoDocuments(t)
	var err error
	out := captureStdout(t, func() { err = runHealth(db.with("--json")) })
	// Exits non-zero when something is wrong, by design; the report is the point.
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatalf("health --json is not a list (err %v):\n%s", err, out)
	}
}

func TestReadings_NoneRecorded(t *testing.T) {
	db, dir := indexWithTwoDocuments(t)
	if out := run(t, runReadings, db.with(dir+"/a.md")); !strings.Contains(out, "no page of a.md has a superseded reading") {
		t.Fatalf("readings:\n%s", out)
	}
}

func TestDiff_SeesIdenticalBytes(t *testing.T) {
	db, dir := indexWithTwoDocuments(t)
	if out := run(t, runDiff, db.with(dir+"/a.md", dir+"/b.md")); !strings.Contains(out, "IDENTICAL BYTES") {
		t.Fatalf("diff:\n%s", out)
	}
}

func TestSimilarStatus_NothingSketched(t *testing.T) {
	db, _ := indexWithTwoDocuments(t)
	if out := run(t, runSimilar, db.with("--status")); !strings.Contains(out, "documents:  2 (0 sketched") {
		t.Fatalf("similar --status:\n%s", out)
	}
}

func TestRetry_DryRunNamesTheIndex(t *testing.T) {
	db, _ := indexWithTwoDocuments(t)
	if out := run(t, runRetry, db.with("--dry-run")); !strings.Contains(out, "index: default") || !strings.Contains(out, "would requeue 0") {
		t.Fatalf("retry --dry-run:\n%s", out)
	}
}

func TestSearch_AMissingIndexSaysSoAndSuggests(t *testing.T) {
	db, _ := indexWithTwoDocuments(t)
	err := runSearch(db.with("--index", "defaul", "walrus"))
	if err == nil || !strings.Contains(err.Error(), `no index`) || !strings.Contains(err.Error(), "t:default") {
		t.Fatalf("search of a missing index: %v, want a 404 suggesting t:default", err)
	}
	if out := run(t, runSearch, db.with("--index", "default", "walrus")); !strings.Contains(out, "a.md") {
		t.Fatalf("search:\n%s", out)
	}
}
