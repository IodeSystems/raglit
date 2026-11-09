package raglit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// worktreeFixture is a main checkout and a worktree with the same relative
// files, a parent index of the main checkout, and the branch's homes.
type worktreeFixture struct {
	mainRoot, wtRoot string
	parentHome       Home
	base             string
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(body), 0o644))
	}
}

// drainPaths enqueues each path and runs the worker until the queue is empty.
func drainPaths(t *testing.T, s *Store, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := s.Enqueue(p, ""); err != nil {
			t.Fatal(err)
		}
	}
	w := &Worker{Store: s}
	if _, err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

var worktreeFiles = map[string]string{
	"a.md":     "alpha token text",
	"src/b.md": "bravo token text",
	"src/c.md": "charlie token old text",
}

func newWorktreeFixture(t *testing.T) worktreeFixture {
	t.Helper()
	base := t.TempDir()
	f := worktreeFixture{
		mainRoot:   filepath.Join(base, "main"),
		wtRoot:     filepath.Join(base, "wt"),
		parentHome: Home(filepath.Join(base, "homes", "main")),
		base:       base,
	}
	writeFiles(t, f.mainRoot, worktreeFiles)
	writeFiles(t, f.wtRoot, worktreeFiles)
	p, err := OpenHome(f.parentHome)
	if err != nil {
		t.Fatal(err)
	}
	p.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	drainPaths(t, p, f.main("a.md"), f.main("src/b.md"), f.main("src/c.md"))
	if docs, _ := p.Documents(); len(docs) != 3 {
		t.Fatalf("parent indexed %d documents, want 3", len(docs))
	}
	must(t, p.Close())
	return f
}

func (f worktreeFixture) main(rel string) string { return filepath.Join(f.mainRoot, rel) }
func (f worktreeFixture) wt(rel string) string   { return filepath.Join(f.wtRoot, rel) }

func (f worktreeFixture) open(t *testing.T, name, root string) *Store {
	t.Helper()
	s, err := OpenBranch(f.parentHome, Home(filepath.Join(f.base, "homes", name)), root, f.mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	return s
}

func ownPaths(t *testing.T, s *Store) map[string]bool {
	t.Helper()
	paths, err := s.q.ListDocumentPaths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func TestWorktreeBranch_Overlay(t *testing.T) {
	f := newWorktreeFixture(t)
	br := f.open(t, "wt", f.wtRoot)
	ctx := context.Background()

	// The worktree changes c, deletes a, leaves b alone.
	writeFiles(t, f.wtRoot, map[string]string{"src/c.md": "charlie token new text"})
	must(t, os.Remove(f.wt("a.md")))

	if h, err := br.DocumentHash(f.wt("src/b.md")); err != nil || h == "" {
		t.Fatalf("DocumentHash(worktree b) = %q, %v; want the parent's", h, err)
	}
	drainPaths(t, br, f.wt("src/b.md"), f.wt("src/c.md"))
	must(t, br.RemoveDocument(f.wt("a.md"), false))

	var mode, state string
	must(t, br.db.QueryRow(`SELECT mode, state FROM ingest_jobs WHERE url = ?`, f.wt("src/b.md")).Scan(&mode, &state))
	if mode != "unchanged" || state != "done" {
		t.Errorf("worktree b job: mode %q state %q, want unchanged/done", mode, state)
	}
	own := ownPaths(t, br)
	if own[f.wt("src/b.md")] {
		t.Error("identical b was copied into the branch")
	}
	if !own[f.wt("src/c.md")] || len(own) != 1 {
		t.Errorf("branch's own documents = %v, want only worktree c", own)
	}

	parent, err := OpenHome(f.parentHome)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	ph, _ := parent.DocumentHash(f.main("src/b.md"))
	if h, _ := br.DocumentHash(f.wt("src/b.md")); h != ph || h == "" {
		t.Errorf("DocumentHash(worktree b) = %q, want parent's %q", h, ph)
	}
	if h, _ := br.DocumentHash(f.wt("a.md")); h != "" {
		t.Errorf("DocumentHash of tombstoned a = %q, want empty", h)
	}
	if h, _ := br.DocumentHash(f.main("src/b.md")); h != "" {
		t.Errorf("DocumentHash(main-checkout b) = %q, want empty: a worktree branch does not show main paths", h)
	}

	checkHits := func(name string, hs []Hit, want ...string) {
		t.Helper()
		got := map[string]string{}
		for _, h := range hs {
			if strings.HasPrefix(h.Path, f.mainRoot) {
				t.Errorf("%s: main-checkout path %s in branch results", name, h.Path)
			}
			got[h.Path] = h.Text
		}
		if len(got) != len(want) {
			t.Errorf("%s: paths %v, want %v", name, got, want)
		}
		for _, w := range want {
			if _, ok := got[w]; !ok {
				t.Errorf("%s: missing %s in %v", name, w, got)
			}
		}
		if txt, ok := got[f.wt("src/c.md")]; ok && strings.Contains(txt, "old") {
			t.Errorf("%s: c served from the parent: %q", name, txt)
		}
	}
	wantBC := []string{f.wt("src/b.md"), f.wt("src/c.md")}
	hs, err := br.Search("token", 10)
	must(t, err)
	checkHits("fts", hs, wantBC...)
	hs, err = br.VecSearch(ctx, "token", 10)
	must(t, err)
	checkHits("vec", hs, wantBC...)
	hs, err = br.HybridSearch(ctx, "token", 10)
	must(t, err)
	checkHits("hybrid", hs, wantBC...)

	for _, c := range []struct {
		prefix string
		want   []string
	}{
		{f.wtRoot + "/", wantBC},
		{f.wtRoot, wantBC},
		{f.wt("src/b"), []string{f.wt("src/b.md")}},
		{f.base + "/", wantBC},
		{"", wantBC},
		{f.mainRoot + "/", nil},
	} {
		flt := SearchFilter{PathPrefix: c.prefix}
		hs, err := br.SearchFiltered("token", flt, 10)
		must(t, err)
		checkHits("fts "+c.prefix, hs, c.want...)
		hs, err = br.VecSearchFiltered(ctx, "token", flt, 10)
		must(t, err)
		checkHits("vec "+c.prefix, hs, c.want...)
		hs, err = br.HybridSearchFiltered(ctx, "token", flt, 10)
		must(t, err)
		checkHits("hybrid "+c.prefix, hs, c.want...)
	}

	docs, err := br.Documents()
	must(t, err)
	seen := map[string]int{}
	for _, d := range docs {
		seen[d.Path]++
	}
	if len(seen) != 2 || seen[f.wt("src/b.md")] != 1 || seen[f.wt("src/c.md")] != 1 {
		t.Errorf("Documents = %v, want worktree b and c once each", seen)
	}
	paths, err := br.DocumentPaths()
	must(t, err)
	if len(paths) != 2 {
		t.Errorf("DocumentPaths = %v, want worktree b and c", paths)
	}

	m, err := br.MatchDocuments("b.md")
	must(t, err)
	if len(m) != 1 || m[0].Path != f.wt("src/b.md") {
		t.Errorf("MatchDocuments(b.md) = %+v, want worktree b", m)
	}
	m, err = br.MatchDocuments(f.wt("src/b.md"))
	must(t, err)
	if len(m) != 1 || m[0].Path != f.wt("src/b.md") {
		t.Errorf("MatchDocuments(worktree b) = %+v", m)
	}
	if m, _ := br.MatchDocuments(f.main("src/b.md")); len(m) != 0 {
		t.Errorf("a main-checkout path resolved through the worktree: %+v", m)
	}
	if m, _ := br.MatchDocuments("a.md"); len(m) != 0 {
		t.Errorf("tombstoned a matched: %+v", m)
	}

	dt, err := br.DocText(f.wt("src/b.md"), 0, 0, 0)
	if err != nil || dt.Path != f.wt("src/b.md") || !strings.Contains(dt.Text, "bravo") {
		t.Errorf("DocText(worktree b) = %+v, %v", dt, err)
	}
	if _, err := br.DocText(f.wt("a.md"), 0, 0, 0); err == nil {
		t.Error("DocText of tombstoned a succeeded")
	}
	if dt, err := br.DocText(f.wt("src/c.md"), 0, 0, 0); err != nil || !strings.Contains(dt.Text, "new") {
		t.Errorf("DocText(worktree c) = %+v, %v; want the branch's copy", dt, err)
	}

	// A main-checkout path is not a path the branch shows: point lookups agree
	// with the listings, for a parent-only (b), a tombstoned (a) and a changed
	// (c) document.
	for _, rel := range []string{"a.md", "src/b.md", "src/c.md"} {
		mp := f.main(rel)
		if dt, err := br.DocText(mp, 0, 0, 0); err == nil {
			t.Errorf("DocText(main %s) = %q, want no document", rel, dt.Text)
		}
		if h, _ := br.DocumentHash(mp); h != "" {
			t.Errorf("DocumentHash(main %s) = %q, want empty", rel, h)
		}
		if _, err := br.Tags(mp); err == nil {
			t.Errorf("Tags(main %s) succeeded", rel)
		}
		if title, pages, _ := br.DocReview(mp); title != "" || pages != nil {
			t.Errorf("DocReview(main %s) = %q, %d pages", rel, title, len(pages))
		}
		if img, _ := br.PageImagePath(mp, 1); img != "" {
			t.Errorf("PageImagePath(main %s) = %q", rel, img)
		}
		if _, err := br.PageLayoutFor(mp, 1); err == nil {
			t.Errorf("PageLayoutFor(main %s) succeeded", rel)
		}
		if err := br.SetDocumentFields(ctx, mp, DocFields{Type: "t"}); errors.Is(err, ErrParentOnly) {
			t.Errorf("SetDocumentFields(main %s) = ErrParentOnly, want no document", rel)
		}
	}

	// Tags through translation: the parent's tags show; the branch cannot set
	// them on a parent-only document.
	must(t, parent.SetTags(f.main("src/b.md"), []string{"kind:x"}))
	if tags, err := br.Tags(f.wt("src/b.md")); err != nil || len(tags) != 1 || tags[0] != "kind:x" {
		t.Errorf("Tags(worktree b) = %v, %v", tags, err)
	}
	if _, err := br.Tags(f.wt("a.md")); err == nil {
		t.Error("Tags of tombstoned a succeeded")
	}
	if err := br.SetTags(f.wt("src/b.md"), []string{"y"}); !errors.Is(err, ErrParentOnly) {
		t.Errorf("SetTags(worktree b) = %v, want ErrParentOnly", err)
	}
	if err := br.SetDocumentFields(ctx, f.wt("src/b.md"), DocFields{Type: "t", Fields: json.RawMessage(`{}`)}); !errors.Is(err, ErrParentOnly) {
		t.Errorf("SetDocumentFields(worktree b) = %v, want ErrParentOnly", err)
	}
	if err := br.SetDocumentIdentity(ctx, f.wt("src/b.md"), DocIdentity{}); !errors.Is(err, ErrParentOnly) {
		t.Errorf("SetDocumentIdentity(worktree b) = %v, want ErrParentOnly", err)
	}
	if err := br.SetDocumentFields(ctx, f.wt("nowhere.md"), DocFields{Type: "t"}); err == nil || errors.Is(err, ErrParentOnly) {
		t.Errorf("SetDocumentFields(unknown) = %v, want a plain no-document error", err)
	}
	must(t, br.SetTags(f.wt("src/c.md"), []string{"own"}))

	// FragmentExists accepts the parent's content id for b (it hashes the
	// parent's path) and not the parent's id for a or c, which are shadowed.
	cid := func(path string) string {
		var id string
		if err := parent.db.QueryRow(`SELECT f.content_id FROM fragments f JOIN documents d ON d.id=f.doc_id WHERE d.path=? LIMIT 1`, path).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if ok, err := br.FragmentExists(cid(f.main("src/b.md"))); err != nil || !ok {
		t.Errorf("FragmentExists(parent b) = %v, %v", ok, err)
	}
	for _, p := range []string{"a.md", "src/c.md"} {
		if ok, _ := br.FragmentExists(cid(f.main(p))); ok {
			t.Errorf("FragmentExists(parent %s) through the branch = true, want shadowed", p)
		}
	}
	paths, _ = br.contentIDPaths(cid(f.main("src/b.md")))
	if len(paths) != 1 || paths[0] != f.wt("src/b.md") {
		t.Errorf("contentIDPaths = %v, want worktree b", paths)
	}

	diff, err := br.BranchDiff()
	must(t, err)
	got := map[string]string{}
	for _, c := range diff {
		got[c.Path] = c.Change
	}
	want := map[string]string{f.wt("src/c.md"): "changed", f.wt("a.md"): "deleted"}
	if len(got) != len(want) {
		t.Errorf("BranchDiff = %+v, want %v", diff, want)
	}
	for p, c := range want {
		if got[p] != c {
			t.Errorf("BranchDiff %s = %q, want %q", p, got[p], c)
		}
	}

	// The parent is untouched.
	if pd, _ := parent.Documents(); len(pd) != 3 {
		t.Errorf("parent has %d documents, want 3", len(pd))
	}
}

func TestWorktreeBranch_TwoWorktreesIndependent(t *testing.T) {
	f := newWorktreeFixture(t)
	wt2 := filepath.Join(f.base, "wt2")
	writeFiles(t, wt2, worktreeFiles)
	b1 := f.open(t, "wt1", f.wtRoot)
	b2 := f.open(t, "wt2", wt2)

	writeFiles(t, f.wtRoot, map[string]string{"src/c.md": "charlie token one"})
	drainPaths(t, b1, f.wt("src/c.md"))
	must(t, b2.RemoveDocument(filepath.Join(wt2, "src/b.md"), false))

	d1, _ := b1.Documents()
	d2, _ := b2.Documents()
	p1, p2 := map[string]bool{}, map[string]bool{}
	for _, d := range d1 {
		p1[d.Path] = true
	}
	for _, d := range d2 {
		p2[d.Path] = true
	}
	if len(p1) != 3 || !p1[f.wt("src/b.md")] {
		t.Errorf("worktree 1 documents = %v", p1)
	}
	if len(p2) != 2 || p2[filepath.Join(wt2, "src/b.md")] || !p2[filepath.Join(wt2, "src/c.md")] {
		t.Errorf("worktree 2 documents = %v", p2)
	}
	if dt, _ := b2.DocText(filepath.Join(wt2, "src/c.md"), 0, 0, 0); !strings.Contains(dt.Text, "old") {
		t.Errorf("worktree 2 sees worktree 1's c: %q", dt.Text)
	}
	if d, _ := b2.BranchDiff(); len(d) != 1 || d[0].Change != "deleted" {
		t.Errorf("worktree 2 diff = %+v", d)
	}
}

func TestOpenBranch_IdempotentAndMismatch(t *testing.T) {
	f := newWorktreeFixture(t)
	home := Home(filepath.Join(f.base, "homes", "wt"))
	s, err := OpenBranch(f.parentHome, home, f.wtRoot, f.mainRoot)
	must(t, err)
	must(t, s.Ingest(context.Background(), Document{Path: f.wt("n.md"), Fragments: []Fragment{{Text: "new token"}}}))
	must(t, s.Close())

	s, err = OpenBranch(f.parentHome, home, f.wtRoot+"/", f.mainRoot)
	must(t, err)
	if own := ownPaths(t, s); !own[f.wt("n.md")] {
		t.Errorf("reopened branch lost its document: %v", own)
	}
	must(t, s.Close())
	meta, ok := readBranchMeta(home)
	if !ok || meta.Root != f.wtRoot || meta.ParentRoot != f.mainRoot {
		t.Errorf("branch.json = %+v", meta)
	}

	other := Home(filepath.Join(f.base, "homes", "other"))
	o, err := OpenHome(other)
	must(t, err)
	must(t, o.Close())
	if _, err := OpenBranch(other, home, f.wtRoot, f.mainRoot); err == nil {
		t.Error("OpenBranch with a different parent succeeded")
	}
	if _, err := OpenBranch(f.parentHome, home, filepath.Join(f.base, "elsewhere"), f.mainRoot); err == nil {
		t.Error("OpenBranch with a different root succeeded")
	}
	if _, err := OpenBranch(f.parentHome, home, f.wtRoot, ""); err == nil {
		t.Error("OpenBranch with one root succeeded")
	}
	if _, err := OpenBranch(f.parentHome, home, "rel/wt", "rel/main"); err == nil {
		t.Error("OpenBranch with relative roots succeeded")
	}
	if _, err := OpenBranch(f.parentHome, f.parentHome, f.wtRoot, f.mainRoot); err == nil {
		t.Error("OpenBranch of a home over itself succeeded")
	}
	if _, err := OpenBranch(Home(filepath.Join(f.base, "nope")), Home(filepath.Join(f.base, "homes", "x")), f.wtRoot, f.mainRoot); err == nil {
		t.Error("OpenBranch over a missing parent index succeeded")
	}
	// An existing non-branch index is not taken over.
	if _, err := OpenBranch(f.parentHome, other, f.wtRoot, f.mainRoot); err == nil {
		t.Error("OpenBranch took over a non-branch index")
	}
}

func TestWorktreeOf(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	must(t, os.MkdirAll(filepath.Join(repo, "sub"), 0o755))
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(repo, "init", "-q")
	writeFiles(t, repo, map[string]string{"sub/x.md": "x"})
	git(repo, "add", ".")
	git(repo, "commit", "-q", "-m", "init")
	wt := filepath.Join(base, "wt")
	git(repo, "worktree", "add", "-q", wt)

	real := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, dir := range []string{wt, filepath.Join(wt, "sub")} {
		got, ok := WorktreeOf(dir)
		if !ok || real(got) != real(repo) {
			t.Errorf("WorktreeOf(%s) = %q, %v; want %q", dir, got, ok, repo)
		}
		got, ok = worktreeFromGitFile(dir)
		if !ok || real(got) != real(repo) {
			t.Errorf("worktreeFromGitFile(%s) = %q, %v; want %q", dir, got, ok, repo)
		}
	}
	for _, dir := range []string{repo, filepath.Join(repo, "sub"), t.TempDir()} {
		if got, ok := WorktreeOf(dir); ok {
			t.Errorf("WorktreeOf(%s) = %q, true; want false", dir, got)
		}
	}
	if _, ok := worktreeFromGitFile(repo); ok {
		t.Error("worktreeFromGitFile(main checkout) = true")
	}
}

func TestGCBranches(t *testing.T) {
	f := newWorktreeFixture(t)
	dead := filepath.Join(f.base, "dead")
	writeFiles(t, dead, worktreeFiles)
	homes := map[string]Home{}
	for name, root := range map[string]string{"live": f.wtRoot, "dead": dead, "rootless": ""} {
		h := Home(filepath.Join(f.base, "homes", name))
		pr := f.mainRoot
		if root == "" {
			pr = ""
		}
		s, err := OpenBranch(f.parentHome, h, root, pr)
		must(t, err)
		must(t, s.Close())
		homes[name] = h
	}
	notBranch := Home(filepath.Join(f.base, "homes", "plain"))
	must(t, notBranch.Ensure())
	must(t, os.RemoveAll(dead))

	alive := func(root string) bool { _, err := os.Stat(root); return err == nil }
	n, err := GCBranches([]Home{homes["live"], homes["dead"], homes["rootless"], notBranch, f.parentHome}, alive)
	must(t, err)
	if n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	if _, err := os.Stat(string(homes["dead"])); !os.IsNotExist(err) {
		t.Error("dead branch home survived")
	}
	for _, h := range []Home{homes["live"], homes["rootless"], notBranch, f.parentHome} {
		if _, err := os.Stat(string(h)); err != nil {
			t.Errorf("%s removed: %v", h, err)
		}
	}
}

// Rootless branches keep the error, now matchable.
func TestErrParentOnly_RootlessBranch(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	must(t, err)
	defer reg.Close()
	ctx := context.Background()
	main, _ := reg.Get("main")
	must(t, main.Ingest(ctx, Document{Path: "file:///a.md", Fragments: []Fragment{{Text: "apple"}}}))
	must(t, reg.ForkBranch("feat", "main"))
	feat, _ := reg.Get("feat")
	if err := feat.SetTags("file:///a.md", []string{"x"}); !errors.Is(err, ErrParentOnly) {
		t.Errorf("SetTags = %v, want ErrParentOnly", err)
	}
	if err := feat.SetDocumentFields(ctx, "file:///a.md", DocFields{Type: "t"}); !errors.Is(err, ErrParentOnly) {
		t.Errorf("SetDocumentFields = %v, want ErrParentOnly", err)
	}
	// No read-through without a root: as before.
	if h, _ := feat.DocumentHash("file:///a.md"); h != "" {
		t.Errorf("rootless DocumentHash read through: %q", h)
	}
	if _, err := feat.Tags("file:///a.md"); err == nil {
		t.Error("rootless Tags read through")
	}
}

// file:// document paths translate the same way as bare ones, and keep their
// scheme.
func TestWorktreeBranch_FileURLPaths(t *testing.T) {
	base := t.TempDir()
	mainRoot, wtRoot := filepath.Join(base, "main"), filepath.Join(base, "wt")
	// Real files: a hashed document whose file is gone is dropped from search.
	writeFiles(t, mainRoot, map[string]string{"x.md": "x", "y.md": "y"})
	writeFiles(t, wtRoot, map[string]string{"x.md": "x"})
	ph := Home(filepath.Join(base, "homes", "main"))
	p, err := OpenHome(ph)
	must(t, err)
	ctx := context.Background()
	for _, rel := range []string{"x.md", "y.md"} {
		must(t, p.Ingest(ctx, Document{Path: "file://" + mainRoot + "/" + rel, Fragments: []Fragment{{Text: rel + " token"}}}))
		must(t, p.SetDocumentHash("file://"+mainRoot+"/"+rel, "h-"+rel))
	}
	must(t, p.Close())
	br, err := OpenBranch(ph, Home(filepath.Join(base, "homes", "wt")), wtRoot, mainRoot)
	must(t, err)
	defer br.Close()
	must(t, br.DeleteDocument("file://"+wtRoot+"/y.md"))
	if h, _ := br.DocumentHash("file://" + wtRoot + "/x.md"); h != "h-x.md" {
		t.Errorf("DocumentHash(file:// worktree x) = %q", h)
	}
	docs, _ := br.Documents()
	if len(docs) != 1 || docs[0].Path != "file://"+wtRoot+"/x.md" {
		t.Errorf("Documents = %+v", docs)
	}
	hs, _ := br.SearchFiltered("token", SearchFilter{PathPrefix: "file://" + wtRoot + "/"}, 10)
	if got := hitPaths(hs); len(got) != 1 || got[0] != "file://"+wtRoot+"/x.md" {
		t.Errorf("filtered search = %v", got)
	}
}

func TestRebaseAndPrefixes(t *testing.T) {
	s := &Store{parent: &Store{}, root: "/w/t", parentRoot: "/m"}
	for in, want := range map[string]string{
		"/w/t/a":        "/m/a",
		"/w/t":          "/m",
		"/w/tx/a":       "/w/tx/a",
		"file:///w/t/a": "file:///m/a",
		"/other":        "/other",
		"/mx/a":         "/mx/a",
		"/m/a":          "",
		"file:///m":     "",
	} {
		got, ok := s.toParent(in)
		if got != want || ok != (want != "") {
			t.Errorf("toParent(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
	if got, ok := s.fromParent("file:///m/a"); got != "file:///w/t/a" || !ok {
		t.Errorf("fromParent = %q, %v", got, ok)
	}
	// A worktree nested in the main checkout: the main index's copy of the
	// worktree's files is not shown, and a main path outside the worktree is
	// hidden from point lookups.
	nested := &Store{parent: &Store{}, root: "/m/.wt/x", parentRoot: "/m"}
	if got, ok := nested.fromParent("/m/.wt/x/a"); ok {
		t.Errorf("nested fromParent(main copy of worktree file) = %q, want dropped", got)
	}
	if got, ok := nested.fromParent("/m/a"); got != "/m/.wt/x/a" || !ok {
		t.Errorf("nested fromParent(/m/a) = %q, %v", got, ok)
	}
	if got, ok := nested.toParent("/m/.wt/x/a"); got != "/m/a" || !ok {
		t.Errorf("nested toParent(worktree a) = %q, %v", got, ok)
	}
	if _, ok := nested.toParent("/m/a"); ok {
		t.Error("nested toParent(main a) passed through")
	}
	for in, want := range map[string]string{
		"/w/t/s": "/m/s",
		"/w/":    "/w/|/m/",
		"":       "",
		"/z":     "/z",
	} {
		if got := strings.Join(s.parentPrefixes(in), "|"); got != want {
			t.Errorf("parentPrefixes(%q) = %q, want %q", in, got, want)
		}
	}
	rootless := &Store{parent: &Store{}}
	if p, ok := rootless.toParent("/m/a"); p != "/m/a" || !ok {
		t.Error("a rootless branch hid a path")
	}
	if p, _ := rootless.toParent("/w/t/a"); p != "/w/t/a" || strings.Join(rootless.parentPrefixes("/w/"), "|") != "/w/" {
		t.Error("a rootless branch translated")
	}
}

// TestWorktreeBranch_NestedRoot: a worktree inside the main checkout, with a
// worktree file the main index also holds. The branch shows each worktree file
// once, at its worktree path, and never a main path rebased a second time.
//
// The main index's copy has bytes no main file has: identical bytes are
// readings of one source, and the parent's search collapses them to one hit
// before the overlay sees them.
func TestWorktreeBranch_NestedRoot(t *testing.T) {
	base := t.TempDir()
	mainRoot := filepath.Join(base, "main")
	wtRoot := filepath.Join(mainRoot, ".wt", "x")
	writeFiles(t, mainRoot, worktreeFiles)
	writeFiles(t, wtRoot, worktreeFiles)
	writeFiles(t, wtRoot, map[string]string{"d.md": "delta token text"})
	ph := Home(filepath.Join(base, "homes", "main"))
	p, err := OpenHome(ph)
	must(t, err)
	p.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	all := []string{filepath.Join(wtRoot, "d.md")}
	for rel := range worktreeFiles {
		all = append(all, filepath.Join(mainRoot, rel))
	}
	drainPaths(t, p, all...)
	must(t, p.Close())

	br, err := OpenBranch(ph, Home(filepath.Join(base, "homes", "wt")), wtRoot, mainRoot)
	must(t, err)
	defer br.Close()
	br.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))

	want := map[string]bool{}
	for rel := range worktreeFiles {
		want[filepath.Join(wtRoot, rel)] = true
	}
	check := func(name string, got []string) {
		t.Helper()
		seen := map[string]int{}
		for _, g := range got {
			seen[g]++
			if !want[g] {
				t.Errorf("%s: path %s is not a worktree file", name, g)
			}
		}
		for w := range want {
			if seen[w] != 1 {
				t.Errorf("%s: %s seen %d times, want once", name, w, seen[w])
			}
		}
	}
	docs, err := br.Documents()
	must(t, err)
	var dp []string
	for _, d := range docs {
		dp = append(dp, d.Path)
	}
	check("Documents", dp)
	paths, err := br.DocumentPaths()
	must(t, err)
	check("DocumentPaths", paths)
	hs, err := br.Search("token", 20)
	must(t, err)
	check("Search", hitPaths(hs))
	hs, err = br.SearchFiltered("token", SearchFilter{PathPrefix: wtRoot + "/"}, 20)
	must(t, err)
	check("SearchFiltered", hitPaths(hs))
	if h, _ := br.DocumentHash(filepath.Join(wtRoot, "src/b.md")); h == "" {
		t.Error("DocumentHash(worktree b) is empty, want the parent's")
	}
	if h, _ := br.DocumentHash(filepath.Join(mainRoot, "src/b.md")); h != "" {
		t.Errorf("DocumentHash(main b) = %q, want empty", h)
	}
}
