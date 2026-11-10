package raglit

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A document's file size and modified time are recorded, backfilled for rows
// that predate them, and carried in the document list. A URL stays unknown.
func TestSourceStats(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	f := filepath.Join(dir, "a.md")
	if err := os.WriteFile(f, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(f, when, when); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, p := range []string{f, "https://example.com/x.html"} {
		if err := s.Ingest(ctx, Document{Path: p, Title: p, Fragments: []Fragment{{Text: "words"}}}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.BackfillSourceStats()
	if err != nil || n != 1 {
		t.Fatalf("backfilled %d, %v; want 1", n, err)
	}
	docs, err := s.Documents()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		switch d.Path {
		case f:
			if d.SrcSize != 11 || d.SrcMtime != when.Unix() {
				t.Errorf("file stat = %d bytes, %d; want 11, %d", d.SrcSize, d.SrcMtime, when.Unix())
			}
		default:
			if d.SrcSize != 0 || d.SrcMtime != 0 {
				t.Errorf("a URL got a file stat: %+v", d)
			}
		}
	}
	if n, _ := s.BackfillSourceStats(); n != 0 {
		t.Errorf("a second backfill revisited %d filled row(s)", n)
	}
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSourceStat("file://" + f); err != nil {
		t.Fatal(err)
	}
	docs, _ = s.Documents()
	for _, d := range docs {
		if d.Path == f && d.SrcSize != 5 {
			t.Errorf("RecordSourceStat via file:// did not update: %d", d.SrcSize)
		}
	}
}

// countScans counts the files IndexTree reads while the test runs.
func countScans(t *testing.T) *int {
	t.Helper()
	n := new(int)
	treeScanHook = func(string) { *n++ }
	t.Cleanup(func() { treeScanHook = nil })
	return n
}

// age sets the modified time of files under root to when.
func age(t *testing.T, when time.Time, files ...string) {
	t.Helper()
	for _, f := range files {
		must(t, os.Chtimes(f, when, when))
	}
}

// IndexTree reads no file whose size and mtime equal the stat recorded when
// its bytes were read. A touch hashes the file, stays Current and records the
// new stat. A change that keeps size and mtime is missed unless Verify.
func TestIndexTreeStatSkip(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.md": "alpha token text", "b.md": "bravo token text"})
	a, b := filepath.Join(root, "a.md"), filepath.Join(root, "b.md")
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second).Add(123456789)
	age(t, t0, a, b)
	s := treeStore(t)
	scans := countScans(t)
	wantCounts(t, "first", indexTree(t, s, root, TreeOptions{}), 2, 0, 0, 0, 0)
	drainStore(t, s)

	*scans = 0
	wantCounts(t, "unchanged", indexTree(t, s, root, TreeOptions{}), 0, 2, 0, 0, 0)
	if *scans != 0 {
		t.Fatalf("an unchanged tree read %d file(s), want 0", *scans)
	}

	t1 := t0.Add(time.Minute)
	age(t, t1, a)
	*scans = 0
	wantCounts(t, "touched", indexTree(t, s, root, TreeOptions{}), 0, 2, 0, 0, 0)
	if *scans != 1 {
		t.Fatalf("a touch read %d file(s), want 1", *scans)
	}
	if st, ok, err := s.readStat(a); err != nil || !ok || st.mtimeNS != t1.UnixNano() {
		t.Fatalf("stat after touch = %+v %v %v, want mtime %d", st, ok, err, t1.UnixNano())
	}
	*scans = 0
	wantCounts(t, "after touch", indexTree(t, s, root, TreeOptions{}), 0, 2, 0, 0, 0)
	if *scans != 0 {
		t.Fatalf("after the touch was recorded, read %d file(s), want 0", *scans)
	}

	// The trade-off: the same size and mtime with other bytes is missed.
	writeFiles(t, root, map[string]string{"a.md": "alpha token TEXT"})
	age(t, t1, a)
	wantCounts(t, "same stat, new bytes", indexTree(t, s, root, TreeOptions{}), 0, 2, 0, 0, 0)
	*scans = 0
	wantCounts(t, "verify", indexTree(t, s, root, TreeOptions{Verify: true}), 1, 1, 0, 0, 0)
	if *scans != 2 {
		t.Fatalf("Verify read %d file(s), want 2", *scans)
	}
}

// A file modified just before its stat was taken is not vouched for: a write
// in the same clock tick with the same size would leave the stat unchanged.
func TestIndexTreeStatRacy(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.md": "alpha token text"})
	s := treeStore(t)
	indexTree(t, s, root, TreeOptions{})
	drainStore(t, s)
	scans := countScans(t)
	wantCounts(t, "fresh file", indexTree(t, s, root, TreeOptions{}), 0, 1, 0, 0, 0)
	if *scans != 1 {
		t.Fatalf("a file modified just now read %d time(s), want 1 (hashed)", *scans)
	}
}

// A branch reads a file it shows from the parent once: the hash matches, and
// the worktree copy's stat goes to branch_stats, so the next pass reads none.
// The parent's row (the main checkout's copy, another mtime) vouches for
// nothing here. A parent whose bytes change makes the branch read again.
func TestIndexTreeStatSkipBranch(t *testing.T) {
	f := newWorktreeFixture(t)
	b := f.open(t, "wt", f.wtRoot)
	when := time.Now().Add(-time.Hour)
	for rel := range worktreeFiles {
		age(t, when, f.wt(rel))
	}
	scans := countScans(t)
	wantCounts(t, "first", indexTree(t, b, f.wtRoot, TreeOptions{}), 0, 3, 0, 0, 0)
	if *scans != 3 {
		t.Errorf("first pass read %d file(s), want 3", *scans)
	}
	if own := ownPaths(t, b); len(own) != 0 {
		t.Errorf("branch gained rows %v, want none", own)
	}
	*scans = 0
	wantCounts(t, "again", indexTree(t, b, f.wtRoot, TreeOptions{}), 0, 3, 0, 0, 0)
	if *scans != 0 {
		t.Errorf("second pass read %d file(s), want 0", *scans)
	}
	if _, err := b.parent.db.Exec(`UPDATE documents SET content_hash='other' WHERE path=?`, f.main("a.md")); err != nil {
		t.Fatal(err)
	}
	*scans = 0
	wantCounts(t, "parent changed", indexTree(t, b, f.wtRoot, TreeOptions{}), 1, 2, 0, 0, 0)
	if *scans != 1 {
		t.Errorf("after the parent changed, read %d file(s), want 1", *scans)
	}
}

// branch_stats is created in an index made before it existed.
func TestBranchStatsMigratesOldIndex(t *testing.T) {
	f := newWorktreeFixture(t)
	home := Home(filepath.Join(f.base, "homes", "wt"))
	b := f.open(t, "wt", f.wtRoot)
	must(t, b.Close())
	db, err := sql.Open("sqlite", home.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE branch_stats`); err != nil {
		t.Fatal(err)
	}
	must(t, db.Close())
	b = f.open(t, "wt", f.wtRoot)
	age(t, time.Now().Add(-time.Hour), f.wt("a.md"))
	fi, err := os.Stat(f.wt("a.md"))
	must(t, err)
	must(t, b.recordReadStat(f.wt("a.md"), statAt(fi, time.Now())))
	if st, ok, err := b.readStat(f.wt("a.md")); err != nil || !ok || st.size != fi.Size() {
		t.Errorf("stat after migration = %+v %v %v", st, ok, err)
	}
}

// documents made before src_mtime_ns existed gain it, at 0: hash the file.
func TestSourceStatMigratesNS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "/x.md", Title: "x", Fragments: []Fragment{{Text: "words"}}}))
	must(t, s.Close())
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE documents DROP COLUMN src_mtime_ns`); err != nil {
		t.Fatal(err)
	}
	must(t, db.Close())
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st, ok, err := s.readStat("/x.md"); err != nil || ok || st.mtimeNS != 0 {
		t.Errorf("old row stat = %+v %v %v, want none", st, ok, err)
	}
}
