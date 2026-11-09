package raglit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Documents carries the content hash: the sha256 of the source file's raw
// bytes as the Worker reads them, not of the extracted text, and the same value
// as DocumentHash. HTML is used because its extracted text differs from its
// bytes, so a worker that hashed the text would fail here.
func TestDocuments_HashIsSha256OfSourceBytes(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "a.html")
	data := []byte("<html><body><h1>Token refresh</h1><p>rotate the key before it expires</p></body></html>")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(src, ""); err != nil {
		t.Fatal(err)
	}
	did, err := (&Worker{Store: s}).ProcessOne(ctx)
	if err != nil || !did {
		t.Fatalf("ProcessOne = %v, %v", did, err)
	}
	docs, err := s.Documents()
	must(t, err)
	if len(docs) != 1 {
		t.Fatalf("Documents = %+v, want one document", docs)
	}
	path := docs[0].Path
	text, err := s.DocText(path, 0, 0, 0)
	must(t, err)
	if text.Text == "" || text.Text == string(data) {
		t.Fatalf("extracted text %q: want it non-empty and different from the bytes", text.Text)
	}

	want := sha256hex(data)
	if got := docs[0].ContentHash; got != want {
		t.Fatalf("ContentHash = %q, want sha256 of the raw bytes %q", got, want)
	}
	if docs[0].ContentHash == sha256hex([]byte(text.Text)) {
		t.Fatal("ContentHash is the sha256 of the extracted text")
	}
	got, err := s.DocumentHash(path)
	must(t, err)
	if got != want {
		t.Fatalf("DocumentHash = %q, want %q", got, want)
	}
}

// A removed document leaves no row behind in any per-document table. Tables
// are enumerated from the schema: a table with a doc_id column, or with a
// column that holds a document path (doc, doc_path, path, url), that this test
// neither counts nor lists as kept on purpose fails it, so a new table gets a
// decision.
func TestRemoveDocument_LeavesNothingBehind(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	s.SetEmbedder(NewEmbedder(&fakeVecClient{}, "fake"))
	const path = "file:///gone.md"
	const keep = "file:///keep.md"
	must(t, s.Ingest(ctx, Document{Path: keep, Title: "K", Fragments: []Fragment{{Text: "deploy rollback"}}}))
	must(t, s.Ingest(ctx, Document{Path: path, Title: "G", Fragments: []Fragment{
		{Page: 1, Text: "token refresh"}, {Page: 2, Text: "billing invoice"}}}))
	var id, fragID int64
	must(t, s.db.QueryRow(`SELECT id FROM documents WHERE path = ?`, path).Scan(&id))
	must(t, s.db.QueryRow(`SELECT id FROM fragments WHERE doc_id = ? LIMIT 1`, id).Scan(&fragID))

	// What Ingest does not write, written straight in.
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO ocr_pages(doc_id, page, engine) VALUES(?, 1, 'vision')`, id)
	exec(`INSERT INTO media(id, doc_id, page, kind, fragment_id) VALUES(900, ?, 1, 'figure', ?)`, id, fragID)
	exec(`INSERT INTO media_vectors(media_id, dim, vec) VALUES(900, 1, x'00000000')`)
	exec(`INSERT INTO shingle_pages(doc_id, page, chars, total, sampled) VALUES(?, 1, 10, 3, 1)`, id)
	exec(`INSERT INTO shingle_index(hash, doc_id, page) VALUES(7, ?, 1)`, id)
	must(t, s.SetDocumentFields(ctx, path, DocFields{Type: "memo", Fields: json.RawMessage(`{"a":"b"}`), Source: "person"}))
	if _, err := s.AddNote(path, Note{Body: "a note"}); err != nil {
		t.Fatal(err)
	}
	must(t, s.AddPageReading(ctx, PageReading{Doc: path, Page: 1, Text: "token refresh", Source: "machine"}))

	// Per-document tables, and how to count a document's rows in each. FTS and
	// the two vector tables have no doc column: they hang off fragments / media.
	counts := map[string]string{
		"fragments":        `SELECT COUNT(*) FROM fragments WHERE doc_id = ?1`,
		"fragments_fts":    `SELECT COUNT(*) FROM fragments_fts WHERE rowid IN (SELECT id FROM fragments WHERE doc_id = ?1)`,
		"fragment_vectors": `SELECT COUNT(*) FROM fragment_vectors WHERE fragment_id IN (SELECT id FROM fragments WHERE doc_id = ?1)`,
		"media":            `SELECT COUNT(*) FROM media WHERE doc_id = ?1`,
		"media_vectors":    `SELECT COUNT(*) FROM media_vectors WHERE media_id IN (SELECT id FROM media WHERE doc_id = ?1)`,
		"ocr_pages":        `SELECT COUNT(*) FROM ocr_pages WHERE doc_id = ?1`,
		"shingle_pages":    `SELECT COUNT(*) FROM shingle_pages WHERE doc_id = ?1`,
		"shingle_index":    `SELECT COUNT(*) FROM shingle_index WHERE doc_id = ?1`,
		"doc_fields":       `SELECT COUNT(*) FROM doc_fields WHERE doc_id = ?1`,
		"document_notes":   `SELECT COUNT(*) FROM document_notes WHERE doc_id = ?1`,
		"documents":        `SELECT COUNT(*) FROM documents WHERE id = ?1`,
	}
	// page_readings counts by path.
	pathCounts := map[string]string{
		"page_readings": `SELECT COUNT(*) FROM page_readings WHERE doc = ?1`,
	}
	// Tables keyed on a document path that are not removed with the document,
	// each on purpose and not checked here: audit and ruling history, the
	// queues and the caption held for a queued ingest (supplied_identities, which
	// that ingest's commit consumes), and the tombstone the removal itself
	// writes.
	kept := map[string]bool{
		"supplied_identities": true,
		"attestations":        true,
		"readings":            true,
		"withdrawals":         true,
		"identity_jobs":       true,
		"ingest_jobs":         true,
		"tombstones":          true,
	}

	// Every table with a doc_id or a path column must be counted or kept above.
	tableRows, err := s.db.Query(`SELECT m.name, EXISTS (SELECT 1 FROM pragma_table_info(m.name) WHERE name = 'doc_id')
		  FROM sqlite_master m
		 WHERE m.type = 'table' AND EXISTS (SELECT 1 FROM pragma_table_info(m.name)
		       WHERE name IN ('doc_id', 'doc', 'doc_path', 'path', 'url'))`)
	must(t, err)
	type table struct {
		name  string
		docID bool
	}
	var found []table
	for tableRows.Next() {
		var tb table
		must(t, tableRows.Scan(&tb.name, &tb.docID))
		found = append(found, tb)
	}
	must(t, tableRows.Err())
	tableRows.Close()
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })
	for _, tb := range found {
		_, byID := counts[tb.name]
		_, byPath := pathCounts[tb.name]
		switch {
		case tb.docID && !byID:
			t.Errorf("table %s has a doc_id column and is not counted by this test", tb.name)
		case !tb.docID && !byID && !byPath && !kept[tb.name]:
			t.Errorf("table %s has a document path column and is neither counted nor kept on purpose", tb.name)
		}
	}

	check := func(label string, wantZero bool) {
		t.Helper()
		for tbl, q := range counts {
			var n int
			must(t, s.db.QueryRow(q, id).Scan(&n))
			if wantZero && n != 0 {
				t.Errorf("%s: table %s still has %d row(s) for the removed document", label, tbl, n)
			}
			if !wantZero && n == 0 {
				t.Errorf("%s: table %s was not populated, so the test proves nothing for it", label, tbl)
			}
		}
		for tbl, q := range pathCounts {
			var n int
			must(t, s.db.QueryRow(q, path).Scan(&n))
			if wantZero && n != 0 {
				t.Errorf("%s: table %s still has %d row(s) for the removed document", label, tbl, n)
			}
			if !wantZero && n == 0 {
				t.Errorf("%s: table %s was not populated", label, tbl)
			}
		}
	}
	check("before", false)

	must(t, s.RemoveDocument(path, false))
	check("after", true)

	// The other document is untouched.
	var n int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM fragments f JOIN documents d ON d.id = f.doc_id WHERE d.path = ?`, keep).Scan(&n))
	if n != 1 {
		t.Fatalf("the kept document has %d fragment(s), want 1", n)
	}
}
