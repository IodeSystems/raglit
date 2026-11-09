package raglit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Caller-owned tags: documents.tags.
//
// The only way a caller could tag a document was SupplyIdentity, and that
// writes a caption, which is a searchable fragment, so a tagged document could
// come back twice in the hits. gen_content_tags was no home for them either:
// the identity model rewrites it on every caption (writeIdentity). So the
// caller's tags have a column that only SetTags writes. They are filter data
// (SearchFilter.Tags), never text: no fragment, no caption, no identity job.

// SetTags replaces the caller's tags on the document at path. An empty slice
// clears them. Each tag is normalized the way the model's are
// (normalizeContentTag) and repeats are dropped, but the model-tag rules do not
// apply: no length or word limit, no count, no role vocabulary. A caller's tag
// is its own vocabulary, "kind:strategy" included.
//
// On a path with no document yet the tags are held, and the next ingest of that
// path to commit applies them, so "enqueue, then tag" works without waiting for
// the job. A later SetTags before that commit replaces the held ones.
//
// On a branch, a document that only the parent holds is an error: a branch
// cannot edit the parent's document until copy-up exists (the same rule as
// SetDocumentFields). The error wraps ErrParentOnly.
func (s *Store) SetTags(path string, tags []string) error {
	if path == "" {
		return fmt.Errorf("raglit: set tags: empty path")
	}
	joined := strings.Join(normalizeTagsForStorage(tags), ",")
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// The UPDATE takes the write lock, so no commit of this path can land
	// between "there is no document" and holding the tags for one.
	res, err := tx.ExecContext(ctx, `UPDATE documents SET tags = ? WHERE path = ?`, joined, path)
	if err != nil {
		return fmt.Errorf("raglit: set tags: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Read through tx: an in-memory store has one connection, and tx holds it.
		only, err := s.parentOnly(ctx, tx, path)
		if err != nil {
			return err
		}
		if only {
			return fmt.Errorf("raglit: set tags: %q: %w", path, ErrParentOnly)
		}
		if joined == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM pending_tags WHERE path = ?`, path)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO pending_tags(path, tags) VALUES(?, ?)
				ON CONFLICT(path) DO UPDATE SET tags = excluded.tags`, path, joined)
		}
		if err != nil {
			return fmt.Errorf("raglit: hold tags: %w", err)
		}
	}
	return tx.Commit()
}

// applyPendingTags moves the tags held for path (SetTags before the document
// existed) onto its document, inside the commit that creates it. Nothing held
// leaves the document's tags as they are, so a re-ingest keeps them.
func applyPendingTags(ctx context.Context, tx *sql.Tx, path string, docID int64) error {
	var tags string
	err := tx.QueryRowContext(ctx, `SELECT tags FROM pending_tags WHERE path = ?`, path).Scan(&tags)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET tags = ? WHERE id = ?`, tags, docID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM pending_tags WHERE path = ?`, path)
	return err
}

// Tags returns the caller's tags on the document at path, in the order SetTags
// stored them. A document with none returns nil. An unknown path is an error;
// tags still held for it are not reported.
//
// A branch with a root (OpenBranch) reads a parent-only document's tags through
// to the parent. A branch with no root does not.
func (s *Store) Tags(path string) ([]string, error) {
	var tags string
	err := s.db.QueryRow(`SELECT tags FROM documents WHERE path = ?`, path).Scan(&tags)
	if errors.Is(err, sql.ErrNoRows) {
		if s.worktree() {
			only, perr := s.parentOnly(context.Background(), s.db, path)
			if perr != nil {
				return nil, perr
			}
			if only {
				pp, _ := s.toParent(path) // parentOnly is false for a path the branch hides
				return s.parent.Tags(pp)
			}
		}
		return nil, fmt.Errorf("raglit: no document with path %q", path)
	}
	if err != nil {
		return nil, err
	}
	return splitTagList(tags), nil
}
