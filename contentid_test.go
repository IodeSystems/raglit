package raglit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// contentIDsOf is every fragment's content id for a document, in rowid order.
func contentIDsOf(t *testing.T, s *Store, path string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT f.content_id FROM fragments f JOIN documents d ON d.id = f.doc_id
		WHERE d.path = ? ORDER BY f.id`, path)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// The text-overlap fragmenter's line span for a pageless text document is the
// lines its bytes cover in the file, half-open, and reaches the Hit.
func TestOverlapFragments_LinesFromOffsets(t *testing.T) {
	s := openMem(t)
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, fmt.Sprintf("line%d alpha beta gamma", i))
	}
	text := strings.Join(lines, "\n") + "\n"
	fc := FragConfig{Window: 120, Stride: 80, Floor: 30}
	if _, _, err := s.ingestText(context.Background(), "code.txt", "Code", text, fc, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT start_off, end_off, start_line, end_line FROM fragments ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var so, eo, sl, el int
		if err := rows.Scan(&so, &eo, &sl, &el); err != nil {
			t.Fatal(err)
		}
		wantS := strings.Count(text[:so], "\n") + 1
		wantE := strings.Count(text[:eo-1], "\n") + 2
		if sl != wantS || el != wantE {
			t.Fatalf("fragment [%d,%d): lines [%d,%d), want [%d,%d)", so, eo, sl, el, wantS, wantE)
		}
		if sl < 1 || el <= sl {
			t.Fatalf("fragment [%d,%d): bad line span [%d,%d)", so, eo, sl, el)
		}
		n++
	}
	if n < 2 {
		t.Fatalf("want several fragments, got %d", n)
	}
	// The first fragment starts on line 1, and a hit carries its span.
	hits, err := s.Search("line1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].StartLine != 1 || hits[0].EndLine < 2 {
		t.Fatalf("hit for line1 = %+v", hits)
	}
	if hits[0].ContentID == "" {
		t.Fatal("hit has no content id")
	}
}

// A paged document's joined source has no lines of its own: 0/0.
func TestOverlapFragments_PagedDocumentHasNoLines(t *testing.T) {
	var got []stagedFrag
	pages := []resolvedPage{{page: 1, text: "one\ntwo\nthree"}, {page: 2, text: "four\nfive"}}
	fragmentOverlap(pages, 2000, 1500, 1, func(f stagedFrag) { got = append(got, f) })
	for _, f := range got {
		if f.startLine != 0 || f.endLine != 0 {
			t.Fatalf("paged fragment has lines [%d,%d)", f.startLine, f.endLine)
		}
	}
}

// Lines and a symbol the caller sets on Store.Ingest come back on every search
// path.
func TestIngest_CallerLinesAndSymbolRoundTrip(t *testing.T) {
	s := openMem(t)
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "a.go", Fragments: []Fragment{
		{Text: "func Start walrus", StartLine: 10, EndLine: 25, Symbol: "Server.Start"},
	}}))
	check := func(name string, hits []Hit, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 {
			t.Fatalf("%s: %d hits", name, len(hits))
		}
		h := hits[0]
		if h.StartLine != 10 || h.EndLine != 25 || h.Symbol != "Server.Start" || h.ContentID == "" {
			t.Fatalf("%s: hit = lines [%d,%d) symbol %q id %q", name, h.StartLine, h.EndLine, h.Symbol, h.ContentID)
		}
	}
	h, err := s.Search("walrus", 5)
	check("fts", h, err)
	h, err = s.SearchFiltered("walrus", SearchFilter{PathPrefix: "a."}, 5)
	check("filtered", h, err)
	h, err = s.VecSearch(ctx, "walrus", 5)
	check("vector", h, err)
	h, err = s.HybridSearch(ctx, "walrus", 5)
	check("hybrid", h, err)
}

// The same text keeps its id across a re-ingest; changed text, symbol or path
// does not; a repeat in one document gets its own.
func TestContentID_StableUniqueAndSensitive(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	doc := func(texts ...string) Document {
		d := Document{Path: "a.md"}
		for _, x := range texts {
			d.Fragments = append(d.Fragments, Fragment{Text: x})
		}
		return d
	}
	must(t, s.Ingest(ctx, doc("alpha one", "beta two", "alpha one")))
	first := contentIDsOf(t, s, "a.md")
	if len(first) != 3 {
		t.Fatalf("ids = %v", first)
	}
	if first[0] == first[2] || first[2] != first[0]+":2" {
		t.Fatalf("repeat ids = %q, %q; want the second suffixed :2", first[0], first[2])
	}
	if first[0] != contentIDBase("a.md", "", "alpha one") {
		t.Fatalf("id %q is not sha256(path, symbol, text)", first[0])
	}

	must(t, s.Ingest(ctx, doc("alpha one", "beta two", "alpha one")))
	if again := contentIDsOf(t, s, "a.md"); strings.Join(again, ",") != strings.Join(first, ",") {
		t.Fatalf("re-ingest changed ids:\n%v\n%v", first, again)
	}

	must(t, s.Ingest(ctx, doc("alpha one", "beta CHANGED", "alpha one")))
	changed := contentIDsOf(t, s, "a.md")
	if changed[0] != first[0] || changed[2] != first[2] {
		t.Fatal("unchanged text changed id")
	}
	if changed[1] == first[1] {
		t.Fatal("changed text kept its id")
	}
	if contentIDBase("a.md", "S", "x") == contentIDBase("a.md", "", "x") ||
		contentIDBase("b.md", "", "x") == contentIDBase("a.md", "", "x") {
		t.Fatal("symbol or path does not reach the id")
	}
	// Length-prefixed: moving bytes between fields changes the id.
	if contentIDBase("a", "b\x00c", "") == contentIDBase("a", "b", "c") ||
		contentIDBase("ab", "", "c") == contentIDBase("a", "b", "c") {
		t.Fatal("field boundaries do not reach the id")
	}
	ok, err := s.FragmentExists(first[1])
	if err != nil || ok {
		t.Fatalf("replaced text still exists: %v %v", ok, err)
	}
	ok, err = s.FragmentExists(changed[1])
	if err != nil || !ok {
		t.Fatalf("current text not found: %v %v", ok, err)
	}
}

// The identity and fields fragments get ids as well, written by their own paths.
func TestContentID_SetOnIdentityFragment(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "a.md", Fragments: []Fragment{{Text: "walrus"}}}))
	must(t, s.SetDocumentIdentity(ctx, "a.md", DocIdentity{Name: "A thing", Summary: "about walruses", Source: "person"}))
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM fragments WHERE content_id = ''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM fragments`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total < 2 || n != 0 {
		t.Fatalf("%d of %d fragment(s) have no content id", n, total)
	}
}

// A branch sees the parent's fragment until it shadows or tombstones the
// document.
func TestFragmentExists_BranchOverlay(t *testing.T) {
	reg, err := OpenScopedRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	ctx := context.Background()
	main, _ := reg.Get("main")
	must(t, main.Ingest(ctx, Document{Path: "file:///a.md", Fragments: []Fragment{{Text: "apple zebra"}}}))
	must(t, main.Ingest(ctx, Document{Path: "file:///b.md", Fragments: []Fragment{{Text: "banana walrus"}}}))
	aID := contentIDBase("file:///a.md", "", "apple zebra")
	bID := contentIDBase("file:///b.md", "", "banana walrus")
	must(t, reg.ForkBranch("feat", "main"))
	feat, _ := reg.Get("feat")

	exists := func(s *Store, id string) bool {
		t.Helper()
		ok, err := s.FragmentExists(id)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !exists(feat, aID) || !exists(feat, bID) {
		t.Fatal("a fresh branch should see the parent's fragments")
	}
	if exists(feat, "nope") || exists(feat, "") {
		t.Fatal("unknown id exists")
	}
	// Shadow a.md with new text: the parent's id is hidden, the branch's shows.
	must(t, feat.Ingest(ctx, Document{Path: "file:///a.md", Fragments: []Fragment{{Text: "apricot phoenix"}}}))
	if exists(feat, aID) {
		t.Fatal("shadowed parent fragment still exists in the branch")
	}
	if !exists(feat, contentIDBase("file:///a.md", "", "apricot phoenix")) {
		t.Fatal("branch's own fragment missing")
	}
	// Tombstone b.md.
	must(t, feat.DeleteDocument("file:///b.md"))
	if exists(feat, bID) {
		t.Fatal("tombstoned parent fragment still exists in the branch")
	}
	if !exists(main, aID) || !exists(main, bID) {
		t.Fatal("parent lost its fragments")
	}
	// And a hit through the overlay carries the parent's content id.
	if err := main.Ingest(ctx, Document{Path: "file:///c.md", Fragments: []Fragment{{Text: "cherry yak", StartLine: 3, EndLine: 4}}}); err != nil {
		t.Fatal(err)
	}
	hits, err := feat.Search("yak", 5)
	if err != nil || len(hits) != 1 || hits[0].ContentID != contentIDBase("file:///c.md", "", "cherry yak") || hits[0].StartLine != 3 {
		t.Fatalf("overlay hit = %+v, %v", hits, err)
	}
}

// createPreR8Index writes an index file with the fragments table, FTS mirror
// and triggers as they were before line spans and content ids: nfrag fragments
// over ndoc documents, with one repeated text per document.
func createPreR8Index(t testing.TB, path string, ndoc, nfrag int) {
	t.Helper()
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	for _, q := range []string{
		`CREATE TABLE documents (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE,
			title TEXT NOT NULL DEFAULT '', added_at INTEGER NOT NULL)`,
		`CREATE TABLE fragments (id INTEGER PRIMARY KEY,
			doc_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			page INTEGER NOT NULL DEFAULT 0, ord INTEGER NOT NULL DEFAULT 0, text TEXT NOT NULL,
			start_off INTEGER NOT NULL DEFAULT 0, end_off INTEGER NOT NULL DEFAULT 0,
			page_spans TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL DEFAULT '')`,
		`CREATE VIRTUAL TABLE fragments_fts USING fts5(text, content='fragments', content_rowid='id', tokenize='porter unicode61')`,
		`CREATE TRIGGER fragments_ai AFTER INSERT ON fragments BEGIN
			INSERT INTO fragments_fts(rowid, text) VALUES (new.id, new.text); END`,
		`CREATE TRIGGER fragments_ad AFTER DELETE ON fragments BEGIN
			INSERT INTO fragments_fts(fragments_fts, rowid, text) VALUES ('delete', old.id, old.text); END`,
		`CREATE TRIGGER fragments_au AFTER UPDATE ON fragments BEGIN
			INSERT INTO fragments_fts(fragments_fts, rowid, text) VALUES ('delete', old.id, old.text);
			INSERT INTO fragments_fts(rowid, text) VALUES (new.id, new.text); END`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := old.Begin()
	if err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("lorem ipsum ", 80) // ~1,000 bytes a fragment
	per := nfrag / ndoc
	for d := 0; d < ndoc; d++ {
		if _, err := tx.Exec(`INSERT INTO documents(id, path, added_at) VALUES(?, ?, 1)`, d+1, "doc"+strconv.Itoa(d)+".md"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < per; i++ {
			text := fmt.Sprintf("walrus d%d f%d %s", d, i, pad)
			if i == per-1 {
				text = fmt.Sprintf("walrus d%d f%d %s", d, 0, pad) // a repeat of the first
			}
			if _, err := tx.Exec(`INSERT INTO fragments(doc_id, ord, text) VALUES(?, ?, ?)`, d+1, i, text); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// An index created before R8 opens, gains the columns and the narrower FTS
// trigger, and every fragment gets its content id; search still works.
func TestMigrate_OpensAnIndexThatPredatesContentIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	createPreR8Index(t, path, 3, 30)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening an index that predates content ids: %v", err)
	}
	defer s.Close()
	for _, c := range []string{"start_line", "end_line", "symbol", "content_id"} {
		if has, err := hasColumn(s.db, "fragments", c); err != nil || !has {
			t.Fatalf("fragments.%s after migrate: %v, %v", c, has, err)
		}
	}
	ids := contentIDsOf(t, s, "doc0.md")
	if len(ids) != 10 {
		t.Fatalf("ids = %v", ids)
	}
	if ids[0] == "" || ids[9] != ids[0]+":2" {
		t.Fatalf("backfilled ids: first %q, repeat %q", ids[0], ids[9])
	}
	var trig string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='fragments_au'`).Scan(&trig); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(trig, "UPDATE OF text") {
		t.Fatalf("trigger not replaced: %s", trig)
	}
	hits, err := s.Search("walrus", 50)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search after migrate: %d, %v", len(hits), err)
	}
	for _, h := range hits {
		if h.ContentID == "" || h.StartLine != 0 {
			t.Fatalf("migrated hit = %+v", h)
		}
	}
	// A second open owes nothing.
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if again := contentIDsOf(t, s2, "doc0.md"); strings.Join(again, ",") != strings.Join(ids, ",") {
		t.Fatal("reopen changed ids")
	}
}

// The backfill's cost at open. 100,000 fragments of ~1,000 bytes over 1,000
// documents with RAGLIT_BACKFILL_TIMING=1; a small index otherwise, so the
// suite stays fast.
func TestContentIDBackfillTiming(t *testing.T) {
	ndoc, nfrag := 20, 2000
	if os.Getenv("RAGLIT_BACKFILL_TIMING") != "" {
		ndoc, nfrag = 1000, 100000
	}
	path := filepath.Join(t.TempDir(), "index.sqlite")
	createPreR8Index(t, path, ndoc, nfrag)
	start := time.Now()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Logf("open+backfill of %d fragment(s): %s", nfrag, time.Since(start))
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM fragments WHERE content_id = ''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d fragment(s) left without an id", n)
	}
}
