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
// The document is a REAL file on disk, not a synthetic path. It used to be
// "/a.md", which no index would ever contain and which the missing-file check
// correctly calls a problem — an index whose document is not where its row says
// is not a healthy index, and the fixture was quietly asserting otherwise.
func TestProblemsSilentOnAHealthyIndex(t *testing.T) {
	s := testStore(t)
	if err := s.Ingest(context.Background(), Document{Path: existingFile(t, "a.md"),
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

// raglit's own output, indexed as a document, is reported with the command that
// removes it. Eight of these sat in a live corpus, one of them captioned
// "Transcription of halvor-ROS-disputed.pdf".
func TestProblems_ReportsRaglitsOwnOutputAsIndexed(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	// Written straight in: Enqueue now refuses these, which is the fix — this
	// tests the report that finds the ones already there.
	for _, p := range []string{
		"/corpus/deed.pdf" + transcriptionSuffix,
		"/corpus/survey.pdf" + regionsSuffix,
	} {
		if err := s.Ingest(ctx, Document{Path: p, Title: "x",
			Fragments: []Fragment{{Page: 1, Ord: 0, Text: "generated text"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Ingest(ctx, Document{Path: "/corpus/deed.pdf", Title: "deed.pdf",
		Fragments: []Fragment{{Page: 1, Ord: 0, Text: "the real document"}}}); err != nil {
		t.Fatal(err)
	}
	ps, err := s.Problems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found []Problem
	for _, p := range ps {
		if p.Kind == ProblemGenerated {
			found = append(found, p)
		}
	}
	if len(found) != 2 {
		t.Fatalf("reported %d generated documents, want 2: %+v", len(found), ps)
	}
	for _, p := range found {
		if !IsGeneratedSidecar(p.Subject) {
			t.Errorf("reported a real document as generated: %s", p.Subject)
		}
		if !strings.HasPrefix(p.Fix, "raglit forget ") {
			t.Errorf("no usable fix on %s: %q", p.Subject, p.Fix)
		}
	}
}

// The failure nothing else noticed: a row whose file has moved.
//
// Search keeps working — the fragments are in SQLite — so the corpus looks fine
// from the one angle anybody checks. It breaks only when something goes back to
// the file, and each of those breaks far from anything that names the cause.
func TestProblemsFindsDocumentsWhoseFileIsGone(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	here := existingFile(t, "here.md")
	gone := filepath.Join(filepath.Dir(here), "moved-away.md")

	for _, p := range []string{here, gone} {
		if err := s.Ingest(ctx, Document{Path: p, Fragments: []Fragment{{Text: "text"}}}); err != nil {
			t.Fatal(err)
		}
	}

	var got []Problem
	for _, p := range mustProblems(t, s) {
		if p.Kind == ProblemMissingFile {
			got = append(got, p)
		}
	}
	if len(got) != 1 || got[0].Subject != gone {
		t.Fatalf("want only %s reported missing, got %+v", gone, got)
	}
	// The row is dead, but the DOCUMENT usually is not — the file has moved and
	// is still in the tree. A fix that says only "forget" would read as "this is
	// lost", so the detail has to say otherwise.
	if !strings.Contains(got[0].Detail, "MOVED") {
		t.Errorf("detail does not say the file may have moved: %q", got[0].Detail)
	}
	if got[0].Fix == "" {
		t.Error("a missing-file row carries no fix")
	}
}

// A slice names a PAGE RANGE of a parent, not a file, so stat can never succeed
// on one. This is the false positive anybody writing this check hits first: it
// reported five of them on the corpus that prompted the feature, before the
// check learned to skip them.
func TestProblemsDoesNotCallSlicesMissingFiles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	parent := existingFile(t, "bundle.pdf")
	if err := s.Ingest(ctx, Document{Path: parent, Fragments: []Fragment{{Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ingest(ctx, Document{Path: parent + "#p1-8",
		Fragments: []Fragment{{Text: "slice text"}}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range mustProblems(t, s) {
		if p.Kind == ProblemMissingFile {
			t.Fatalf("a slice was reported as a missing file: %+v", p)
		}
	}
}

// Withdrawn is absent ON PURPOSE and has its own kind. Reporting it twice — once
// as a decision and once as a fault — is the confusion withdrawal exists to end.
func TestProblemsDoesNotCallWithdrawnDocumentsMissing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	gone := filepath.Join(t.TempDir(), "withdrawn-and-deleted.md")
	if err := s.Ingest(ctx, Document{Path: gone, Fragments: []Fragment{{Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Withdraw(Withdrawal{Path: gone, Reason: "ruled out"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range mustProblems(t, s) {
		if p.Kind == ProblemMissingFile {
			t.Fatalf("a withdrawn document was also reported missing: %+v", p)
		}
	}
}

// A remote document cannot be stat'ed. Calling it missing because a server is
// unreachable would be a false alarm that arrives every time the network does.
func TestProblemsDoesNotCallRemoteDocumentsMissing(t *testing.T) {
	s := testStore(t)
	if err := s.Ingest(context.Background(), Document{Path: "https://example.com/a.pdf",
		Fragments: []Fragment{{Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range mustProblems(t, s) {
		if p.Kind == ProblemMissingFile {
			t.Fatalf("a remote document was reported as a missing file: %+v", p)
		}
	}
}

// A hole must be FINDABLE. The ingest salvage deliberately keeps a document that
// lost a page, which is only the right trade if something says so — a partial
// document nobody knows is partial is more dangerous than a failed ingest,
// because search returns it and a reader takes the absence for the record's.
func TestProblemsReportsUnreadPages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "scan.pdf", Fragments: []Fragment{{Text: "the pages that read"}}}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM documents WHERE path='scan.pdf'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		page   int
		engine string
	}{{1, "vision"}, {2, "failed"}, {3, "vision"}, {4, "failed"}} {
		if _, err := s.db.Exec(`INSERT INTO ocr_pages(doc_id,page,engine) VALUES(?,?,?)`,
			id, r.page, r.engine); err != nil {
			t.Fatal(err)
		}
	}
	ps, err := s.Problems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *Problem
	for i := range ps {
		if ps[i].Kind == ProblemUnreadPage {
			got = &ps[i]
		}
	}
	if got == nil {
		t.Fatal("a document with unread pages is not reported at all")
	}
	if got.Subject != "scan.pdf" {
		t.Errorf("subject = %q", got.Subject)
	}
	// One row per document, naming every hole — not one row per page.
	n := 0
	for _, p := range ps {
		if p.Kind == ProblemUnreadPage {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want one line for the document, got %d", n)
	}
	if !strings.Contains(got.Detail, "2") || !strings.Contains(got.Detail, "4") {
		t.Errorf("the detail must name WHICH pages are missing, got %q", got.Detail)
	}
	if got.Fix == "" {
		t.Error("a reported hole must come with the command that closes it")
	}
}

// A fully-read document is not a problem. A report that fires on healthy input
// trains people to ignore it.
func TestProblemsSilentWhenEveryPageRead(t *testing.T) {
	s := testStore(t)
	if err := s.Ingest(context.Background(), Document{Path: "clean.pdf", Fragments: []Fragment{{Text: "all read"}}}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM documents WHERE path='clean.pdf'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for p := 1; p <= 3; p++ {
		s.db.Exec(`INSERT INTO ocr_pages(doc_id,page,engine) VALUES(?,?,'vision')`, id, p)
	}
	ps, _ := s.Problems(context.Background())
	for _, p := range ps {
		if p.Kind == ProblemUnreadPage {
			t.Errorf("a fully-read document was reported as holed: %+v", p)
		}
	}
}

// The fix must address the stage that actually degraded. Offering `reread` for a
// SEGMENT failure sends an operator to re-OCR a document whose OCR was fine, and
// the segmenter then fails on identical text — the same defect `doctor` had when
// it pointed its fix at the wrong process.
func TestDegradedFixTargetsTheStageThatFailed(t *testing.T) {
	seg := Problem{Kind: ProblemDegraded, Subject: "/a.pdf", Stage: "segment"}
	other := Problem{Kind: ProblemDegraded, Subject: "/a.pdf", Stage: "ocr"}
	var q problemQuery
	for _, x := range problemQueries {
		if x.kind == ProblemDegraded {
			q = x
		}
	}
	if q.fix == nil {
		t.Fatal("the degraded problem offers no fix at all")
	}
	if got := q.fix(seg); strings.Contains(got, "reread") {
		t.Errorf("a segment failure must not recommend reread: %q", got)
	} else if !strings.Contains(got, "--fresh") {
		t.Errorf("a segment failure should re-run the pipeline off cached OCR: %q", got)
	}
	if got := q.fix(other); !strings.Contains(got, "reread") {
		t.Errorf("a non-segment degradation still wants a reread: %q", got)
	}
}

// A report that only grows is not a report. A degradation fixed by a later
// re-ingest is history: re-segmenting 29 documents once took the reported count
// from 33 to 37, because the old stage rows survived and the clean runs added
// nothing that could cancel them.
func TestDegradedOnlyReportsTheLatestRun(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "/d.pdf", Fragments: []Fragment{{Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Enqueue("/d.pdf", "")
	s.NewStageLog(old).Record("segment", "", "degraded", "would not return JSON")
	// A later run of the SAME document that segmented cleanly.
	newer, _ := s.Enqueue("/d.pdf", "")
	s.NewStageLog(newer).Record("segment", "", "done", "3 fragment(s)")

	ps, err := s.Problems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Kind == ProblemDegraded && p.JobID == old {
			t.Errorf("a degradation fixed by a later run is still reported: %+v", p)
		}
	}
	// And a document whose LATEST run degraded is still reported.
	if err := s.Ingest(ctx, Document{Path: "/e.pdf", Fragments: []Fragment{{Text: "t"}}}); err != nil {
		t.Fatal(err)
	}
	bad, _ := s.Enqueue("/e.pdf", "")
	s.NewStageLog(bad).Record("segment", "", "degraded", "would not return JSON")
	ps, _ = s.Problems(ctx)
	found := false
	for _, p := range ps {
		if p.Kind == ProblemDegraded && p.JobID == bad {
			found = true
		}
	}
	if !found {
		t.Error("a document whose latest run degraded must still be reported")
	}
}
