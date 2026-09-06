package raglit

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// A lane is decided from the URL at enqueue, because that is all an enqueue has.
func TestLaneFor_SplitsByResourceNotBySpeed(t *testing.T) {
	heavy := []string{
		"/corpus/survey.pdf", "file:///scan.PDF", "/x/exhibit.tiff", "/x/photo.jpg",
		"/x/page.png", "/x/scan.heic",
	}
	light := []string{
		// Not "fast" — a 24 MB mail archive is not fast. It belongs in light
		// because it needs no GPU slot, which is the thing that has to be serial.
		"/x/answer.docx", "/x/sheet.xlsx", "/x/thread.eml", "/x/notes.md",
		"/x/store.go", "/x/README", "/x/no-extension-at-all",
	}
	for _, u := range heavy {
		if got := LaneFor(u); got != LaneHeavy {
			t.Errorf("LaneFor(%q) = %q, want heavy", u, got)
		}
	}
	for _, u := range light {
		if got := LaneFor(u); got != LaneLight {
			t.Errorf("LaneFor(%q) = %q, want light", u, got)
		}
	}
}

// The claim is what makes the scheduler fair, so it is what has to be pinned: a
// lane must see ONLY its own work, and must never be handed another lane's.
//
// This is the whole bug in miniature. Before lanes there was one queue and one
// claimer, so a markdown file that needs three seconds of embedding waited
// behind a twenty-two page scan that needs forty minutes of vision. Here the
// scan is queued FIRST and the light lane must still get its job immediately.
func TestClaimNextInLane_LightIsNotStuckBehindHeavy(t *testing.T) {
	s := testStore(t)

	slow, err := s.Enqueue("file:///corpus/twenty-two-page-scan.pdf", "")
	if err != nil {
		t.Fatal(err)
	}
	quick, err := s.Enqueue("file:///corpus/notes.md", "")
	if err != nil {
		t.Fatal(err)
	}

	// The scan is older, so a single FIFO queue would hand it out first and the
	// markdown would wait for it to finish.
	got, err := s.ClaimNextInLane(LaneLight)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("light lane claimed nothing while a light job was pending")
	}
	if got.ID != quick {
		t.Fatalf("light lane claimed job %d, want the markdown (%d)", got.ID, quick)
	}

	// And it is still there for the lane that can afford it — claiming in one
	// lane must not consume another's work.
	h, err := s.ClaimNextInLane(LaneHeavy)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil || h.ID != slow {
		t.Fatalf("heavy lane did not get the scan: %+v", h)
	}

	// Both are claimed now; neither lane has anything left.
	for _, lane := range []Lane{LaneLight, LaneHeavy} {
		j, err := s.ClaimNextInLane(lane)
		if err != nil {
			t.Fatal(err)
		}
		if j != nil {
			t.Fatalf("%s lane claimed job %d twice", lane, j.ID)
		}
	}
}

// A row with no lane is claimed by NOBODY — each lane asks for its own by name —
// so an index that was queueing before lanes existed would have its whole
// backlog stranded, reporting `pending` forever with nothing wrong with it.
func TestBackfillLanes_RescuesJobsQueuedBeforeLanesExisted(t *testing.T) {
	s := testStore(t)

	pdf, err := s.Enqueue("file:///corpus/scan.pdf", "")
	if err != nil {
		t.Fatal(err)
	}
	md, err := s.Enqueue("file:///corpus/notes.md", "")
	if err != nil {
		t.Fatal(err)
	}
	// Exactly what an upgraded index looks like: rows that predate the column.
	if _, err := s.db.Exec(`UPDATE ingest_jobs SET lane = ''`); err != nil {
		t.Fatal(err)
	}
	for _, lane := range []Lane{LaneLight, LaneHeavy} {
		if j, _ := s.ClaimNextInLane(lane); j != nil {
			t.Fatalf("%s lane claimed an unlaned job %d — the test no longer reproduces the case", lane, j.ID)
		}
	}

	n, err := s.BackfillLanes()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("backfilled %d, want 2", n)
	}
	if j, _ := s.ClaimNextInLane(LaneHeavy); j == nil || j.ID != pdf {
		t.Fatalf("heavy lane did not get the rescued pdf: %+v", j)
	}
	if j, _ := s.ClaimNextInLane(LaneLight); j == nil || j.ID != md {
		t.Fatalf("light lane did not get the rescued markdown: %+v", j)
	}
}

// A job claimed and never started goes back, so a clean shutdown does not leave
// a row that says `running` with no process behind it.
func TestRequeueJob_OnlyRewindsAJobThatNeverStarted(t *testing.T) {
	s := testStore(t)
	id, err := s.Enqueue("file:///corpus/notes.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextInLane(LaneLight); err != nil {
		t.Fatal(err)
	}
	if err := s.RequeueJob(id); err != nil {
		t.Fatal(err)
	}
	j, err := s.ClaimNextInLane(LaneLight)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil || j.ID != id {
		t.Fatal("a requeued job was not claimable again")
	}

	// A finished job is not rewound by it.
	if err := s.completeJob(id, 3, "text-overlap"); err != nil {
		t.Fatal(err)
	}
	if err := s.RequeueJob(id); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Job(id); err != nil || got.State != string(JobDone) {
		t.Fatalf("RequeueJob rewound a completed job: state=%q err=%v", got.State, err)
	}
}

// TestMigrate_OpensAnIndexThatPredatesTheLaneColumn pins the crash that took the
// daemon down.
//
// The lane index was first written into schema.sql, which runs on EVERY open and
// before migrate(). On an existing database CREATE TABLE IF NOT EXISTS is a
// no-op, so the table still had no `lane` when that file was applied, and
// `CREATE INDEX ... (state, lane, id)` failed the whole schema apply:
//
//	raglit: schema: SQL logic error: no such column: lane (1)
//
// At open, so the daemon exited at startup and systemd restarted it into the
// same failure, forever. Every existing test creates a FRESH database, where
// schema.sql supplies the column itself and the ordering never shows — which is
// why nothing caught it. This one builds the old shape on purpose.
func TestMigrate_OpensAnIndexThatPredatesTheLaneColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.sqlite")

	// An ingest_jobs exactly as it was before lanes: no lane column, one queued
	// row, so the backfill has something to find.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE ingest_jobs (
		id INTEGER PRIMARY KEY, url TEXT NOT NULL, title TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '',
		fragments INTEGER NOT NULL DEFAULT 0, enqueued_at INTEGER NOT NULL,
		started_at INTEGER NOT NULL DEFAULT 0, finished_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(
		`INSERT INTO ingest_jobs(url, enqueued_at) VALUES('file:///corpus/scan.pdf', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening an index that predates the lane column: %v", err)
	}
	defer s.Close()

	// And the row that was already queued is not stranded.
	n, err := s.BackfillLanes()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("backfilled %d rows, want the 1 that was already queued", n)
	}
	if j, _ := s.ClaimNextInLane(LaneHeavy); j == nil {
		t.Fatal("the pre-existing scan was never claimable after upgrade")
	}
}

// A recording is the longest job raglit runs, so it belongs in the lane with one
// slot. In the light lane three hearings would hold all three slots for twenty
// minutes each and everything else would wait behind them.
func TestLaneFor_ARecordingIsHeavy(t *testing.T) {
	for _, name := range []string{"hearing.mp4", "hearing.opus", "call.mp3", "tape.wav"} {
		if got := LaneFor(name); got != LaneHeavy {
			t.Errorf("LaneFor(%q) = %q, want heavy", name, got)
		}
	}
	// Unchanged for everything else.
	if LaneFor("notes.md") != LaneLight || LaneFor("scan.pdf") != LaneHeavy {
		t.Error("existing lane assignments must not move")
	}
}
