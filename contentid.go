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

	gen "github.com/iodesystems/raglit/internal/db"
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

// ContentIDs is the content id Store.Ingest gives each of frags, in order, for
// a document at path: the id a Hit carries and FragmentExists takes. A caller
// that keeps its own fragments (the code index) uses it to decide what it has
// already embedded without asking the index for every one.
//
// Exact for Store.Ingest, which replaces every fragment of the document and
// numbers them in the order given. A fragment Ingest skips (blank, or bytes
// that are not text) still gets an id here, one that no stored fragment has.
func ContentIDs(path string, frags []Fragment) []string {
	ids := newContentIDs(path)
	out := make([]string, len(frags))
	for i, f := range frags {
		out[i] = ids.next(f.Symbol, f.Text)
	}
	return out
}

// setContentIDs (re)computes content_id for every fragment of one document, in
// rowid order, inside the caller's transaction (a *sql.Tx, or a *sql.Conn
// that has begun one).
//
// Per document rather than per insert, because a repeat's occurrence number
// depends on every fragment before it, and fragments of one document are written
// by several paths (commitDoc, writeIdentity, replaceFieldsFragment). Each of
// them calls this before it commits. Only rows whose id changed are written.
func setContentIDs(ctx context.Context, tx gen.DBTX, docID int64) (int, error) {
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
// In batches of backfillBatch documents, each its own BEGIN IMMEDIATE
// transaction, for the reasons writeTx gives: setContentIDs reads before it
// writes, and a deferred BEGIN would fail with SQLITE_BUSY the moment another
// process committed between the two. Batches keep each hold on the write lock
// well inside busy_timeout on a large index. Each document's ids are its own,
// so an interrupted backfill leaves whole documents done and the next open
// resumes with the rest. The lookup of documents still owing an id rides
// fragments_content, so an index with nothing owed pays one index probe per
// open. Measured 2026-11-08 on a synthetic index of 100,000 fragments of
// ~1,000 bytes over 1,000 documents (RAGLIT_BACKFILL_TIMING=1 go test -run
// TestContentIDBackfillTiming): 5.3s on the dev box in one transaction. It says
// so on stderr when it starts and when it ends, so a slow disk is a reported
// wait and not a silent stall at open.
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
	n := 0
	for len(docs) > 0 {
		batch := docs[:min(backfillBatch, len(docs))]
		docs = docs[len(batch):]
		k, err := backfillBatchTx(ctx, db, batch)
		if err != nil {
			return err
		}
		n += k
	}
	fmt.Fprintf(os.Stderr, "raglit: content ids: backfilled %d fragment(s) in %s\n",
		n, time.Since(start).Round(time.Millisecond))
	return nil
}

// backfillBatch is the documents per backfill transaction. At the measured
// rate (1,000 documents of 100 fragments in 5.3s) a batch holds the write lock
// for about half a second.
const backfillBatch = 100

// backfillBatchTx numbers one batch of documents in one BEGIN IMMEDIATE
// transaction on one connection (see Store.writeTx, which this mirrors: migrate
// runs before there is a Store).
func backfillBatchTx(ctx context.Context, db *sql.DB, docs []int64) (int, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range docs {
		k, err := setContentIDs(ctx, conn, d)
		if err != nil {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
			return 0, err
		}
		n += k
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		return 0, err
	}
	return n, nil
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
		if bp, ok := s.fromParent(p); ok && !shadow[bp] {
			paths = append(paths, bp)
		}
	}
	return paths, nil
}
