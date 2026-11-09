package raglit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Documents carries the content hash, equal to DocumentHash and to the sha256
// of the file's bytes.
func TestDocuments_HashIsSha256OfSourceBytes(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "a.md")
	data := []byte("# token refresh\nrotate the key\n")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	path := "file://" + src
	must(t, s.Ingest(ctx, Document{Path: path, Title: "A", Fragments: []Fragment{{Text: "token refresh"}}}))
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	must(t, s.SetDocumentHash(path, want))

	got, err := s.DocumentHash(path)
	must(t, err)
	if got != want {
		t.Fatalf("DocumentHash = %q, want sha256 of the bytes %q", got, want)
	}
	docs, err := s.Documents()
	must(t, err)
	if len(docs) != 1 || docs[0].Hash != want || docs[0].Hash != got {
		t.Fatalf("Documents = %+v, want one document with Hash %q", docs, want)
	}
}

// A removed document leaves no row behind in any per-document table. Tables
// are enumerated from the schema: a table keyed on documents(id) or on a
// document path that this test does not list fails it, so a new table gets a
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
	// each on purpose and not checked here: attestations, readings, withdrawals
	// (audit and ruling history), identity_jobs, ingest_jobs (queues), and the
	// tombstone the removal itself writes.

	// Every table with a doc_id column must be counted above.
	rows, err := s.db.Query(`SELECT m.name FROM sqlite_master m
		 WHERE m.type = 'table' AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) WHERE name = 'doc_id')`)
	must(t, err)
	var withDocID []string
	for rows.Next() {
		var n string
		must(t, rows.Scan(&n))
		withDocID = append(withDocID, n)
	}
	must(t, rows.Err())
	rows.Close()
	sort.Strings(withDocID)
	for _, n := range withDocID {
		if _, ok := counts[n]; !ok {
			t.Errorf("table %s has a doc_id column and is not covered by this test", n)
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
