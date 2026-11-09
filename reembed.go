package raglit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Which model an index's vectors came from, and putting them right after the
// model changes.
//
// A vector is only comparable with a query embedded by the SAME model. Vectors
// did not record their model, so a changed embed model left every index
// cosining queries against vectors from another space: wrong ranks, no error.
// index_meta now records it. When the configured model differs, the vectors are
// dropped and rebuilt from the stored text (USER 2026-10-22: warn, then queue a
// re-embed; keyword search keeps working meanwhile). The rebuild embeds
// whatever has no vector, so it resumes across restarts with nothing to track.

const (
	metaEmbedModel = "embed_model"
	// metaEmbedDim is the length of the vectors embed_model returns. The same
	// model id can come back with another dimension (a different quantisation
	// or a server that truncates, under one name), and a vector of another
	// length cannot be compared with the query at all.
	metaEmbedDim = "embed_dim"
)

// SyncEmbedModel records model and dim as the source of this index's vectors
// and reports whether existing vectors were dropped because they came from
// another model or have another length. An index with no model record adopts
// model as-is: it is an index from before the record existed, whose vectors
// are from the model configured when they were made, and re-embedding every
// index on upgrade would cost the whole corpus for nothing.
//
// dim 0 means the caller does not know it: the dimension is then neither
// compared nor recorded. With no dim record, the length of a stored vector
// stands in for one, so an index from before the record still drops vectors
// that the configured model can no longer match.
func (s *Store) SyncEmbedModel(model string, dim int) (dropped bool, err error) {
	if model == "" {
		return false, nil
	}
	s.vecMu.Lock()
	defer s.vecMu.Unlock()
	cur, _ := s.Meta(metaEmbedModel)
	metaDim, curDim := s.embedDims()
	modelChanged := cur != "" && cur != model
	dimChanged := dim > 0 && curDim > 0 && curDim != dim
	if cur == model && !dimChanged && (dim == 0 || dim == metaDim) {
		return false, nil
	}
	drop := modelChanged || dimChanged
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	if drop {
		if _, err := dropVectorsTx(tx); err != nil {
			return false, err
		}
	}
	set := func(key, value string) error {
		_, err := tx.Exec(
			`INSERT INTO index_meta (key, value, updated_at) VALUES (?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, now)
		return err
	}
	if err := set(metaEmbedModel, model); err != nil {
		return false, err
	}
	switch {
	case dim > 0:
		if err := set(metaEmbedDim, strconv.Itoa(dim)); err != nil {
			return false, err
		}
	case drop:
		// The recorded dim described the vectors just dropped. Kept, it would
		// drop the new model's vectors the first time a caller passes their dim.
		if _, err := tx.Exec(`DELETE FROM index_meta WHERE key = ?`, metaEmbedDim); err != nil {
			return false, err
		}
	}
	return drop, tx.Commit()
}

// embedDims returns the recorded vector length (0 when there is no record) and
// the length to compare against: the record, else the length of a stored
// fragment vector, else 0.
func (s *Store) embedDims() (recorded, current int) {
	if v, ok := s.Meta(metaEmbedDim); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n, n
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT dim FROM fragment_vectors LIMIT 1`).Scan(&n); err != nil {
		return 0, 0
	}
	return 0, n
}

// DropVectors deletes every vector the text embedder made, so a forced
// re-embed (BackfillVectors) rebuilds them all. Fragments, their FTS rows and
// the model and dim records are untouched. Returns the number of vectors
// deleted.
func (s *Store) DropVectors() (int, error) {
	s.vecMu.Lock()
	defer s.vecMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	n, err := dropVectorsTx(tx)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// dropVectorsTx deletes the fragment vectors, and the figure vectors that are
// in the text query's space: the description vectors came from the text
// model, and the aligned image vectors are only usable against that model's
// queries. Image-space vectors came from the image embedder, and stay.
func dropVectorsTx(tx *sql.Tx) (int, error) {
	var n int64
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM fragment_vectors`, nil},
		{`DELETE FROM media_vectors WHERE space IN (?, ?)`, []any{spaceText, spaceImageAligned}},
	} {
		r, err := tx.Exec(q.sql, q.args...)
		if err != nil {
			return 0, err
		}
		k, err := r.RowsAffected()
		if err != nil {
			return 0, err
		}
		n += k
	}
	return int(n), nil
}

// EmbedModel is the model this index's vectors are recorded as coming from, ""
// when no record exists yet.
func (s *Store) EmbedModel() string {
	m, _ := s.Meta(metaEmbedModel)
	return m
}

// MissingVectors counts fragments with no vector. Only the document's own
// words: identity and fields fragments are about a document and are never
// embedded (SQLOwnWordsF).
func (s *Store) MissingVectors() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM fragments f
		LEFT JOIN fragment_vectors fv ON fv.fragment_id = f.id
		WHERE fv.fragment_id IS NULL AND ` + SQLOwnWordsF).Scan(&n)
	return n, err
}

// FragmentCount is the number of fragments, for the cost of a re-embed.
func (s *Store) FragmentCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM fragments f WHERE ` + SQLOwnWordsF).Scan(&n)
	return n, err
}

// errEmbedderChanged is a backfill whose embedder SetEmbedder replaced while
// it embedded. Its vectors are discarded; a backfill with the new embedder
// fills the same fragments.
var errEmbedderChanged = errors.New("raglit: re-embed: the embedder changed during the batch; its vectors were discarded")

// BackfillVectors embeds up to batch fragments that have no vector, then figure
// descriptions with no text-space vector, and returns how many it gave a
// vector. A fragment whose text already has a vector in this model's space
// (another fragment's, here or in a branch's parent) gets a copy of it rather
// than an embed call (R12a, reuse.go). 0 means there is nothing left (or no
// embedder, or the index is paused).
func (s *Store) BackfillVectors(ctx context.Context, batch int) (int, error) {
	emb := s.textEmbedder()
	if emb == nil || s.Paused() {
		return 0, nil
	}
	n, err := s.backfill(ctx, emb, batch, true,
		`SELECT f.id, f.text FROM fragments f
		   LEFT JOIN fragment_vectors fv ON fv.fragment_id = f.id
		  WHERE fv.fragment_id IS NULL AND `+SQLOwnWordsF+` ORDER BY f.id LIMIT ?`,
		`INSERT OR REPLACE INTO fragment_vectors (fragment_id, dim, vec) VALUES (?, ?, ?)`)
	if err != nil || n > 0 {
		return n, err
	}
	return s.backfill(ctx, emb, batch, false,
		`SELECT m.id, m.description FROM media m
		   LEFT JOIN media_vectors mv ON mv.media_id = m.id
		  WHERE mv.media_id IS NULL AND TRIM(m.description) <> '' ORDER BY m.id LIMIT ?`,
		`INSERT OR REPLACE INTO media_vectors (media_id, dim, vec, space) VALUES (?, ?, ?, '`+spaceText+`')`)
}

// backfill gives up to batch rows of sel a vector and writes them with ins.
// reuse copies a stored vector for text that has one; it is for fragments only,
// whose vectors are the ones reuse reads.
func (s *Store) backfill(ctx context.Context, emb *Embedder, batch int, reuse bool, sel, ins string) (int, error) {
	rows, err := s.db.QueryContext(ctx, sel, batch)
	if err != nil {
		return 0, err
	}
	var ids []int64
	var texts []string
	for rows.Next() {
		var id int64
		var t string
		if err := rows.Scan(&id, &t); err != nil {
			rows.Close()
			return 0, err
		}
		ids, texts = append(ids, id), append(texts, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return 0, err
	}
	var vecs [][]float32
	if reuse {
		vecs, _, _, err = s.embedReusing(ctx, emb, texts)
	} else {
		vecs, err = emb.EmbedDocs(ctx, texts)
	}
	if err != nil {
		return 0, fmt.Errorf("raglit: re-embed: %w", err)
	}
	if len(vecs) != len(ids) {
		return 0, fmt.Errorf("raglit: re-embed: %d vectors for %d texts", len(vecs), len(ids))
	}
	for i, v := range vecs {
		if len(v) == 0 {
			return 0, fmt.Errorf("raglit: re-embed: no vector for text %d of %d", i+1, len(ids))
		}
	}
	current, unlock := s.vectorsCurrent(emb)
	defer unlock()
	if !current {
		return 0, errEmbedderChanged
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	for i, id := range ids {
		if _, err := tx.Exec(ins, id, len(vecs[i]), encodeVec(vecs[i])); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit()
}
