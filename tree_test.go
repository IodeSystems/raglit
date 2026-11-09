package raglit

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func treeStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	return s
}

func drainStore(t *testing.T, s *Store) {
	t.Helper()
	if _, err := (&Worker{Store: s}).Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func indexTree(t *testing.T, s *Store, root string, o TreeOptions) TreeResult {
	t.Helper()
	res, err := IndexTree(context.Background(), s, root, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Errors {
		t.Errorf("per-file error: %v", e)
	}
	return res
}

func wantCounts(t *testing.T, step string, got TreeResult, q, c, r, f, s int) {
	t.Helper()
	if got.Queued != q || got.Current != c || got.Removed != r || got.Failed != f || got.Skipped != s {
		t.Fatalf("%s: got queued=%d current=%d removed=%d failed=%d skipped=%d, want %d/%d/%d/%d/%d",
			step, got.Queued, got.Current, got.Removed, got.Failed, got.Skipped, q, c, r, f, s)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// One tree, every class: new, unchanged, changed, removed, oversize, binary,
// minified, and (in a git repo) gitignored.
func TestIndexTreeCounts(t *testing.T) {
	for _, useGit := range []bool{false, true} {
		name := "walk"
		if useGit {
			name = "git"
		}
		t.Run(name, func(t *testing.T) {
			if useGit {
				if _, err := exec.LookPath("git"); err != nil {
					t.Skip("git not on PATH")
				}
			}
			root := t.TempDir()
			writeFiles(t, root, map[string]string{
				"a.md":         "alpha token text",
				"src/b.md":     "bravo token text",
				"big.md":       strings.Repeat("x", 300),
				"min.js":       strings.Repeat("y", minifiedLineBytes+10),
				".hidden/c.md": "hidden",
			})
			must(t, os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x7f, 'E', 'L', 'F', 0, 0, 1, 2, 3, 0, 0, 0}, 0o644))
			if useGit {
				writeFiles(t, root, map[string]string{".gitignore": "build/\n.hidden/\n", "build/out.md": "built"})
				git(t, root, "init", "-q")
			}
			s := treeStore(t)
			o := TreeOptions{Rules: TreeRules{MaxBytes: 200}}

			wantCounts(t, "first", indexTree(t, s, root, o), 2, 0, 0, 0, 3)
			drainStore(t, s)
			wantCounts(t, "unchanged", indexTree(t, s, root, o), 0, 2, 0, 0, 3)

			writeFiles(t, root, map[string]string{"a.md": "alpha token changed", "d.md": "delta"})
			must(t, os.Remove(filepath.Join(root, "src/b.md")))
			wantCounts(t, "edit", indexTree(t, s, root, o), 2, 0, 1, 0, 3)
			drainStore(t, s)
			if h, _ := s.DocumentHash(filepath.Join(root, "src/b.md")); h != "" {
				t.Errorf("removed document still has hash %q", h)
			}
			wantCounts(t, "settled", indexTree(t, s, root, o), 0, 2, 0, 0, 3)
		})
	}
}

// A failed file is not queued again until its bytes change.
func TestIndexTreeFailedNotRequeued(t *testing.T) {
	root := t.TempDir()
	// A .pdf that is not a PDF fails extraction.
	writeFiles(t, root, map[string]string{"bad.pdf": "this is not a pdf", "ok.md": "fine token"})
	s := treeStore(t)
	wantCounts(t, "first", indexTree(t, s, root, TreeOptions{}), 2, 0, 0, 0, 0)
	drainStore(t, s)
	if !s.failedOn(sha256hex([]byte("this is not a pdf")), filepath.Join(root, "bad.pdf")) {
		jobs, _ := s.Jobs("", 10)
		t.Fatalf("no failure recorded; jobs %+v", jobs)
	}
	wantCounts(t, "again", indexTree(t, s, root, TreeOptions{}), 0, 1, 0, 1, 0)
	// A touch is not a change.
	must(t, os.Chtimes(filepath.Join(root, "bad.pdf"), time.Now(), time.Now()))
	wantCounts(t, "touched", indexTree(t, s, root, TreeOptions{}), 0, 1, 0, 1, 0)
	writeFiles(t, root, map[string]string{"bad.pdf": "still not a pdf, other bytes"})
	wantCounts(t, "changed", indexTree(t, s, root, TreeOptions{}), 1, 1, 0, 0, 0)
}

// Paths checks only the paths named; a path that is gone is Removed.
func TestIndexTreePathsSubset(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.md": "a", "b.md": "b", "sub/c.md": "c", "sub/d.md": "d"})
	s := treeStore(t)
	indexTree(t, s, root, TreeOptions{})
	drainStore(t, s)

	writeFiles(t, root, map[string]string{"a.md": "a2", "b.md": "b2"})
	wantCounts(t, "only a", indexTree(t, s, root, TreeOptions{Paths: []string{filepath.Join(root, "a.md")}}), 1, 0, 0, 0, 0)
	must(t, os.RemoveAll(filepath.Join(root, "sub")))
	wantCounts(t, "gone dir", indexTree(t, s, root, TreeOptions{Paths: []string{"sub"}}), 0, 0, 2, 0, 0)
	must(t, os.Remove(filepath.Join(root, "a.md")))
	wantCounts(t, "gone file", indexTree(t, s, root, TreeOptions{Paths: []string{filepath.Join(root, "a.md")}}), 0, 0, 1, 0, 0)
	wantCounts(t, "outside", indexTree(t, s, root, TreeOptions{Paths: []string{"/elsewhere/x.md"}}), 0, 0, 0, 0, 0)
	// b.md changed but was never named, so it is still queued only now.
	wantCounts(t, "whole", indexTree(t, s, root, TreeOptions{}), 1, 0, 0, 0, 0)
}

// Documents stored as file:// urls are matched, and new files follow them.
func TestIndexTreeFileURLs(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.md": "a", "b.md": "b"})
	s := treeStore(t)
	drainPaths(t, s, "file://"+filepath.Join(root, "a.md"), "file://"+filepath.Join(root, "b.md"))
	writeFiles(t, root, map[string]string{"c.md": "c"})
	must(t, os.Remove(filepath.Join(root, "b.md")))
	wantCounts(t, "urls", indexTree(t, s, root, TreeOptions{}), 1, 1, 1, 0, 0)
	jobs, _ := s.Jobs("pending", 10)
	if len(jobs) != 1 || jobs[0].URL != "file://"+filepath.Join(root, "c.md") {
		t.Errorf("pending %+v, want the file:// url of c.md", jobs)
	}
}

// IndexTrees routes files to indexes and moves a file whose route changes.
func TestIndexTreesRoute(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.md": "a", "b.go": "package b"})
	code, docs := treeStore(t), treeStore(t)
	route := func(p string) string {
		if strings.HasSuffix(p, ".md") {
			return "docs"
		}
		return "code"
	}
	m, err := IndexTrees(context.Background(), map[string]*Store{"code": code, "docs": docs}, root, TreeOptions{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	wantCounts(t, "code", m["code"], 1, 0, 0, 0, 0)
	wantCounts(t, "docs", m["docs"], 1, 0, 0, 0, 0)
	if _, err := IndexTrees(context.Background(), map[string]*Store{"code": code, "docs": docs}, root, TreeOptions{}); err == nil {
		t.Error("two stores and no Route: want an error")
	}
}

// On a worktree branch, files identical to the main checkout are Current
// through the parent, and a file deleted in the worktree is tombstoned.
func TestIndexTreeWorktreeBranch(t *testing.T) {
	f := newWorktreeFixture(t)
	b := f.open(t, "wt", f.wtRoot)
	b.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	wantCounts(t, "identical", indexTree(t, b, f.wtRoot, TreeOptions{}), 0, 3, 0, 0, 0)
	writeFiles(t, f.wtRoot, map[string]string{"src/c.md": "charlie token new text"})
	must(t, os.Remove(f.wt("src/b.md")))
	wantCounts(t, "edit", indexTree(t, b, f.wtRoot, TreeOptions{}), 1, 1, 1, 0, 0)
	if h, _ := b.DocumentHash(f.wt("src/b.md")); h != "" {
		t.Errorf("deleted worktree file still shows the parent's hash")
	}
}

// failed_sources is created in an index made before it existed.
func TestFailedSourcesMigratesOldIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	must(t, s.Close())
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE failed_sources`); err != nil {
		t.Fatal(err)
	}
	must(t, db.Close())
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.recordFailedSource("/x.md", "h", "boom")
	if !s.failedOn("h", "/x.md") {
		t.Error("failure not recorded after migration")
	}
}

// A document whose file grew past MaxBytes stays; a negative MaxBytes is no
// limit.
func TestIndexTreeOversizeKeepsDocument(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.md": "alpha token text"})
	s := treeStore(t)
	indexTree(t, s, root, TreeOptions{})
	drainStore(t, s)
	writeFiles(t, root, map[string]string{"a.md": strings.Repeat("alpha token text\n", 3<<20/17+1)})
	wantCounts(t, "grown", indexTree(t, s, root, TreeOptions{}), 0, 0, 0, 0, 1)
	if h, _ := s.DocumentHash(filepath.Join(root, "a.md")); h == "" {
		t.Fatal("document of the oversize file was removed")
	}
	wantCounts(t, "no limit", indexTree(t, s, root, TreeOptions{Rules: TreeRules{MaxBytes: -1}}), 1, 0, 0, 0, 0)
}

// A failure is retried once its backoff passes, and the backoff doubles with
// each failure on the same bytes: a transient outage does not lose the file.
func TestIndexTreeFailedRetriedAfterBackoff(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"bad.pdf": "this is not a pdf"})
	s := treeStore(t)
	p := filepath.Join(root, "bad.pdf")
	indexTree(t, s, root, TreeOptions{})
	drainStore(t, s)
	wantCounts(t, "fresh failure", indexTree(t, s, root, TreeOptions{}), 0, 0, 0, 1, 0)
	age := func(d time.Duration) {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE failed_sources SET at=? WHERE path=?`, time.Now().Add(-d).UnixNano(), p); err != nil {
			t.Fatal(err)
		}
	}
	age(failRetryBase + time.Minute)
	wantCounts(t, "backoff passed", indexTree(t, s, root, TreeOptions{}), 1, 0, 0, 0, 0)
	drainStore(t, s)
	var attempts int
	if err := s.db.QueryRow(`SELECT attempts FROM failed_sources WHERE path=?`, p).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v, want 2", attempts, err)
	}
	age(failRetryBase + time.Minute) // past one backoff, inside the doubled one
	wantCounts(t, "doubled", indexTree(t, s, root, TreeOptions{}), 0, 0, 0, 1, 0)
	if failBackoff(1) != failRetryBase || failBackoff(2) != 2*failRetryBase || failBackoff(100) != failRetryMax {
		t.Errorf("backoff 1=%v 2=%v 100=%v", failBackoff(1), failBackoff(2), failBackoff(100))
	}
}

// failed_sources made before attempts existed gains the column.
func TestFailedSourcesMigratesAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	must(t, s.Close())
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE failed_sources`,
		`CREATE TABLE failed_sources (path TEXT PRIMARY KEY, hash TEXT NOT NULL, at INTEGER NOT NULL, error TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO failed_sources(path, hash, at, error) VALUES ('/x.md', 'h', 0, 'boom')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	must(t, db.Close())
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.failedOn("h", "/x.md") {
		t.Error("an old failure from 1970 is still inside its backoff")
	}
	s.recordFailedSource("/x.md", "h", "boom")
	var attempts int
	if err := s.db.QueryRow(`SELECT attempts FROM failed_sources WHERE path='/x.md'`).Scan(&attempts); err != nil || attempts != 2 {
		t.Errorf("attempts=%d err=%v, want 2", attempts, err)
	}
	if !s.failedOn("h", "/x.md") {
		t.Error("failure not recorded after migration")
	}
}

// Keep spares a document the rules do not select; Admits is the path rules.
func TestIndexTreeKeep(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.md": "a", "docs/x.txt": "x"})
	s := treeStore(t)
	drainPaths(t, s, filepath.Join(root, "a.md"), filepath.Join(root, "docs/x.txt"))
	md := TreeRules{Include: []string{"*.md"}}
	txt := TreeRules{Include: []string{"*.txt"}}
	docs := filepath.Join(root, "docs")
	keep := func(p string) bool { return txt.Admits(docs, p) }
	wantCounts(t, "kept", indexTree(t, s, root, TreeOptions{Rules: md, Keep: keep}), 0, 1, 0, 0, 0)
	wantCounts(t, "no keep", indexTree(t, s, root, TreeOptions{Rules: md}), 0, 1, 1, 0, 0)
	if txt.Admits(docs, filepath.Join(root, "a.txt")) || txt.Admits(docs, docs) || md.Admits(root, filepath.Join(root, "node_modules/m.md")) {
		t.Error("Admits selected a path outside its root, the root, or a built-in ignore")
	}
}

// scanFile, which reads in pieces, agrees with the whole-file measures it
// replaces: the hash, IsMinified, and the head the sniffers read.
func TestScanFileMatchesWholeFile(t *testing.T) {
	dir := t.TempDir()
	short := strings.Repeat("prose line\n", 20000)                                  // many reads, short lines
	long := strings.Repeat("a\n", 10000) + strings.Repeat("y", 3*minifiedLineBytes) // a long line across read boundaries
	edge := strings.Repeat("z", minifiedLineBytes-1) + "\n"
	for name, body := range map[string]string{"short.txt": short, "long.txt": long, "edge.txt": edge, "empty.txt": ""} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sc, err := scanFile(p)
		if err != nil {
			t.Fatal(err)
		}
		data := []byte(body)
		if sc.hash != sha256hex(data) {
			t.Errorf("%s: hash differs from sha256hex", name)
		}
		if got, want := sc.longest >= minifiedLineBytes, IsMinified(data); got != want {
			t.Errorf("%s: minified %v, IsMinified %v", name, got, want)
		}
		if want := data[:min(len(data), fileScanHead)]; string(sc.head) != string(want) {
			t.Errorf("%s: head of %d bytes, want %d", name, len(sc.head), len(want))
		}
	}
}
