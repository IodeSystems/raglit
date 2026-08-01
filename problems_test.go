package raglit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kinds(ps []Problem) map[ProblemKind]int {
	m := map[ProblemKind]int{}
	for _, p := range ps {
		m[p.Kind]++
	}
	return m
}

// The sharpest failure the report exists for: a document row that every count
// includes and no search can return. Nothing looks wrong until somebody searches
// for a phrase they know is in the file.
func TestProblemsFindsIndexedButUnsearchable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "/a.md", Fragments: []Fragment{{Text: "real text"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ingest(ctx, Document{Path: "/empty.md"}); err != nil {
		t.Fatal(err)
	}
	ps, err := s.Problems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		if p.Kind == ProblemNoFragments {
			got = append(got, p.Subject)
		}
	}
	if len(got) != 1 || got[0] != "/empty.md" {
		t.Fatalf("want only /empty.md reported unsearchable, got %v", got)
	}
	for _, p := range ps {
		if p.Kind == ProblemNoFragments && p.Fix == "" {
			t.Error("no fix offered — the reader is told something is wrong and not what to run")
		}
	}
}

// A slice child has no fragments until somebody materializes it. Reporting those
// buries the real ones, which is how a report stops being read.
func TestProblemsIgnoresUnmaterializedSlices(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "/bundle.pdf#p3-5", Title: "Exhibit A"}); err != nil {
		t.Fatal(err)
	}
	if n := kinds(mustProblems(t, s))[ProblemNoFragments]; n != 0 {
		t.Fatalf("an unmaterialized slice was reported as broken (%d)", n)
	}
}

// A failed job must name the STAGE it died in. "Failed" without one sends the
// reader to the wrong half of the pipeline.
func TestProblemsNamesTheFailingStage(t *testing.T) {
	s := testStore(t)
	// A real file: a failed job whose target is gone is a dead row, not a
	// problem, so a fake path would be filtered out before it could be checked.
	scan := existingFile(t, "scan.pdf")
	id, err := s.Enqueue(scan, "")
	if err != nil {
		t.Fatal(err)
	}
	sl := s.NewStageLog(id)
	sl.Done("fetch", "", "1 bytes")
	sl.Fail("embed", "", errString("input (10240 tokens) is too large to process"))
	if err := s.failJob(id, "input (10240 tokens) is too large to process"); err != nil {
		t.Fatal(err)
	}
	var found *Problem
	for _, p := range mustProblems(t, s) {
		if p.Kind == ProblemJobFailed {
			found = &p
			break
		}
	}
	if found == nil {
		t.Fatal("a failed job was not reported")
	}
	if found.Stage != "embed" {
		t.Errorf("stage = %q, want embed — the reader cannot tell fetch from embed", found.Stage)
	}
	// Verbatim: summarising is how "increase the physical batch size" became
	// "status 500" and cost a week.
	if !strings.Contains(found.Detail, "10240 tokens") {
		t.Errorf("the endpoint's own words were lost: %q", found.Detail)
	}
}

// A withdrawal is a decision, not a fault. It is reported so an absent document
// is never mistaken for a broken one, and it must not read as breakage.
func TestProblemsReportsWithdrawalsSeparately(t *testing.T) {
	s := testStore(t)
	if err := s.Withdraw(Withdrawal{Path: "/drafts/x.md", Reason: "own advocacy, not evidence"}); err != nil {
		t.Fatal(err)
	}
	ps := mustProblems(t, s)
	k := kinds(ps)
	if k[ProblemWithdrawn] != 1 {
		t.Fatalf("withdrawal not reported: %v", k)
	}
	if k[ProblemNoFragments] != 0 {
		t.Error("a withdrawn document was ALSO reported as unsearchable — it has no row to be unsearchable")
	}
	for _, p := range ps {
		if p.Kind == ProblemWithdrawn && !strings.Contains(p.Detail, "advocacy") {
			t.Errorf("the grounds are missing from the report: %q", p.Detail)
		}
	}
}

// A clean index reports nothing. A view that always has rows is one nobody reads.
func TestProblemsSilentOnAHealthyIndex(t *testing.T) {
	s := testStore(t)
	if err := s.Ingest(context.Background(), Document{Path: "/a.md",
		Fragments: []Fragment{{Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	if ps := mustProblems(t, s); len(ps) != 0 {
		t.Fatalf("a healthy index reported %d problem(s): %+v", len(ps), ps)
	}
}

// existingFile is a real path on disk, so a job naming it is retryable work
// rather than a dead row.
func existingFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustProblems(t *testing.T, s *Store) []Problem {
	t.Helper()
	ps, err := s.Problems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

type errString string

func (e errString) Error() string { return string(e) }

// A failure whose document is now indexed is history, not a problem. Listing it
// is how a report stops being read — caught by this view against its own corpus,
// where nine real failures sat under thirty-two resolved ones.
func TestProblemsDropsFailuresThatWereLaterFixed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scan := existingFile(t, "scan.pdf")
	id, err := s.Enqueue(scan, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.failJob(id, "input too large"); err != nil {
		t.Fatal(err)
	}
	if n := kinds(mustProblems(t, s))[ProblemJobFailed]; n != 1 {
		t.Fatalf("a genuinely failed job was not reported (%d)", n)
	}
	// The document lands on a later run.
	if err := s.Ingest(ctx, Document{Path: scan, Fragments: []Fragment{{Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	if n := kinds(mustProblems(t, s))[ProblemJobFailed]; n != 0 {
		t.Fatalf("a resolved failure is still listed (%d) — the real ones get buried", n)
	}
}

// A withdrawn document's old failure is not a problem either: it is absent on
// purpose, and it is already reported under its own kind.
func TestProblemsDropsFailuresForWithdrawnDocuments(t *testing.T) {
	s := testStore(t)
	id, err := s.Enqueue("/drafts/x.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.failJob(id, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.Withdraw(Withdrawal{Path: "/drafts/x.md", Reason: "own advocacy, not evidence"}); err != nil {
		t.Fatal(err)
	}
	k := kinds(mustProblems(t, s))
	if k[ProblemJobFailed] != 0 {
		t.Errorf("a withdrawn document's failure is reported as breakage (%d)", k[ProblemJobFailed])
	}
	if k[ProblemWithdrawn] != 1 {
		t.Errorf("the withdrawal itself went missing (%d)", k[ProblemWithdrawn])
	}
}

// A rename outlives the row. `raglit retry` has always skipped jobs whose file
// is gone; the report has to agree, or it lists work nobody can do and an empty
// report stops being achievable.
func TestProblemsDropsFailuresWhoseFileIsGone(t *testing.T) {
	s := testStore(t)
	dir := t.TempDir()
	live := filepath.Join(dir, "here.pdf")
	if err := os.WriteFile(live, []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "renamed-away.pdf")

	for _, u := range []string{live, gone} {
		id, err := s.Enqueue(u, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.failJob(id, "boom"); err != nil {
			t.Fatal(err)
		}
	}
	var subjects []string
	for _, p := range mustProblems(t, s) {
		if p.Kind == ProblemJobFailed {
			subjects = append(subjects, p.Subject)
		}
	}
	if len(subjects) != 1 || subjects[0] != live {
		t.Fatalf("want only the file that still exists, got %v", subjects)
	}
}

// A relative path is unreachable by construction: it resolves against the
// working directory of whichever process opens it, which is not the one that
// wrote the row. Nothing can retry it, so nothing should be asked to.
func TestProblemsDropsFailuresOnRelativePaths(t *testing.T) {
	s := testStore(t)
	id, err := s.Enqueue("documents/evidence/x.pdf", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.failJob(id, "no such file or directory"); err != nil {
		t.Fatal(err)
	}
	if n := kinds(mustProblems(t, s))[ProblemJobFailed]; n != 0 {
		t.Fatalf("a relative-path row is reported as retryable work (%d)", n)
	}
}

// A remote URL cannot be stat'ed, and a fetch that failed against a server is a
// real failure until somebody says otherwise. Guessing "gone" would hide exactly
// the case this report is for.
func TestProblemsKeepsRemoteFailures(t *testing.T) {
	s := testStore(t)
	id, err := s.Enqueue("https://example.invalid/a.pdf", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.failJob(id, "dial tcp: no such host"); err != nil {
		t.Fatal(err)
	}
	if n := kinds(mustProblems(t, s))[ProblemJobFailed]; n != 1 {
		t.Fatalf("a remote failure was dropped as a dead row (%d)", n)
	}
}
