package raglit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Stable fragment ids.
//
// Hit.ID is a rowid, and a real re-ingest deletes and re-inserts every fragment
// (commitDoc), so the rowid of unchanged text changes with it. A caller that
// keeps a reference to a fragment — the code index citing what it already
// embedded, a memory pointing at a passage — needs an id that survives that.
// content_id is it: the same while the document's path, the fragment's symbol
// and its text are the same, and different the moment any of them changes.

// contentIDBase is hex sha256 over (path, symbol, text), each field prefixed by
// its length as 8 bytes big-endian. Length prefixes rather than a separator,
// because a fragment's text can hold any byte, NUL included, and ("a\x00b", "")
// must not hash like ("a", "b").
func contentIDBase(path, symbol, text string) string {
	h := sha256.New()
	var n [8]byte
	for _, f := range []string{path, symbol, text} {
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// contentIDs numbers repeats within one document: the first (path, symbol, text)
// gets the bare hash, the second ":2", the third ":3". Boilerplate repeated in
// one file (a licence header per section, a recurring table row) would otherwise
// give two fragments one id. The order is the caller's, which is insertion order:
// a re-ingest of the same text inserts in the same order, so the numbers repeat.
type contentIDs struct {
	path string
	seen map[string]int
}

func newContentIDs(path string) *contentIDs {
	return &contentIDs{path: path, seen: map[string]int{}}
}

func (c *contentIDs) next(symbol, text string) string {
	base := contentIDBase(c.path, symbol, text)
	c.seen[base]++
	if n := c.seen[base]; n > 1 {
		return base + ":" + strconv.Itoa(n)
	}
	return base
}

// setContentIDs (re)computes content_id for every fragment of one document, in
// rowid order, inside the caller's transaction.
//
// Per document rather than per insert, because a repeat's occurrence number
// depends on every fragment before it, and fragments of one document are written
// by several paths (commitDoc, writeIdentity, replaceFieldsFragment). Each of
// them calls this before it commits. Only rows whose id changed are written.
func setContentIDs(ctx context.Context, tx *sql.Tx, docID int64) (int, error) {
	var path string
	if err := tx.QueryRowContext(ctx, `SELECT path FROM documents WHERE id = ?`, docID).Scan(&path); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil // deleted in between: nothing to number
		}
		return 0, err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id, symbol, text, content_id FROM fragments WHERE doc_id = ? ORDER BY id`, docID)
	if err != nil {
		return 0, err
	}
	type upd struct {
		id  int64
		cid string
	}
	var ups []upd
	ids := newContentIDs(path)
	for rows.Next() {
		var id int64
		var symbol, text, cur string
		if err := rows.Scan(&id, &symbol, &text, &cur); err != nil {
			rows.Close()
			return 0, err
		}
		if cid := ids.next(symbol, text); cid != cur {
			ups = append(ups, upd{id, cid})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, u := range ups {
		if _, err := tx.ExecContext(ctx, `UPDATE fragments SET content_id = ? WHERE id = ?`, u.cid, u.id); err != nil {
			return 0, fmt.Errorf("raglit: set content id: %w", err)
		}
	}
	return len(ups), nil
}

// backfillContentIDs gives the fragments of an index written before content_id
// existed their ids, at open (migrate). Eager rather than lazy, because
// FragmentExists and Hit.ContentID must not depend on whether a document has
// been touched since the upgrade.
//
// One transaction: an interrupted open leaves either every id or none. The
// lookup of documents still owing an id rides fragments_content, so an index
// with nothing owed pays one index probe per open. Measured 2026-11-08 on a
// synthetic index of 100,000 fragments of ~1,000 bytes over 1,000 documents
// (RAGLIT_BACKFILL_TIMING=1 go test -run TestContentIDBackfillTiming): 5.3s on
// the dev box. It says so on stderr when it starts and when it ends, so a slow
// disk is a reported wait and not a silent stall at open.
func backfillContentIDs(db *sql.DB) error {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT doc_id FROM fragments WHERE content_id = ''`)
	if err != nil {
		return err
	}
	var docs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		docs = append(docs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(docs) == 0 {
		return nil
	}
	start := time.Now()
	fmt.Fprintf(os.Stderr, "raglit: content ids: backfilling %d document(s) from before content ids\n", len(docs))
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	n := 0
	for _, d := range docs {
		k, err := setContentIDs(ctx, tx, d)
		if err != nil {
			return err
		}
		n += k
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "raglit: content ids: backfilled %d fragment(s) in %d document(s) in %s\n",
		n, len(docs), time.Since(start).Round(time.Millisecond))
	return nil
}

// FragmentExists reports whether a fragment with this content id is in the
// index. On a branch it overlays the parent the way search does: the parent's
// fragment counts unless its document is shadowed (rewritten or tombstoned in
// the branch).
func (s *Store) FragmentExists(contentID string) (bool, error) {
	if contentID == "" {
		return false, nil
	}
	paths, err := s.contentIDPaths(contentID)
	return len(paths) > 0, err
}

// contentIDPaths is the documents, overlaid, that hold a fragment with this id.
// A content id hashes its path, so this is one path in practice; a list keeps
// the overlay exact without leaning on that.
func (s *Store) contentIDPaths(contentID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT d.path FROM fragments f JOIN documents d ON d.id = f.doc_id WHERE f.content_id = ?`, contentID)
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if s.parent == nil {
		return paths, nil
	}
	s.touchAccess()
	shadow, err := s.shadowedPaths()
	if err != nil {
		return nil, err
	}
	ppaths, err := s.parent.contentIDPaths(contentID)
	if err != nil {
		return nil, err
	}
	for _, p := range ppaths {
		if !shadow[p] {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
