package raglit

import (
	"path/filepath"
	"testing"
)

// foreign_keys must be on for EVERY pooled connection, not just the one that
// happened to serve the pragma at Open. Forcing several connections open at once
// is the only way to see the difference — the bug is invisible at concurrency 1.
func TestForeignKeysOnEveryPooledConnection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// Hold several connections simultaneously so the pool has to open new ones.
	const n = 8
	var txs []interface{ Rollback() error }
	for range n {
		tx, err := s.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		var fk int
		if err := tx.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if fk != 1 {
			t.Fatalf("a pooled connection has foreign_keys=%d — the cascade will not fire on it", fk)
		}
		txs = append(txs, tx)
	}
	for _, tx := range txs {
		_ = tx.Rollback()
	}
}

// The cascade this protects: deleting a fragment must take its vector with it,
// or a reused rowid collides on the next insert.
func TestDeletingFragmentsCascadesToVectors(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	var docID int64
	if err := s.db.QueryRow(
		`INSERT INTO documents (path, title, added_at) VALUES ('a.pdf','a',0) RETURNING id`).Scan(&docID); err != nil {
		t.Fatal(err)
	}
	var fragID int64
	if err := s.db.QueryRow(
		`INSERT INTO fragments (doc_id, page, ord, text) VALUES (?,1,0,'x') RETURNING id`, docID).Scan(&fragID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO fragment_vectors (fragment_id, dim, vec) VALUES (?,1,x'00')`, fragID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM fragments WHERE id = ?`, fragID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.db.QueryRow(`SELECT count(*) FROM fragment_vectors WHERE fragment_id = ?`, fragID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d vector(s) survived their fragment — the next reused rowid will collide", left)
	}
}
