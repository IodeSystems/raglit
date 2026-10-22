package raglit

import (
	"context"
	"fmt"
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

const metaEmbedModel = "embed_model"

// SyncEmbedModel records model as the source of this index's vectors and
// reports whether existing vectors were dropped because they came from another
// model. An index with no record adopts model as-is: it is an index from before
// the record existed, whose vectors are from the model configured when they
// were made, and re-embedding every index on upgrade would cost the whole
// corpus for nothing.
func (s *Store) SyncEmbedModel(model string) (dropped bool, err error) {
	if model == "" {
		return false, nil
	}
	cur, _ := s.Meta(metaEmbedModel)
	if cur == model {
		return false, nil
	}
	now := time.Now().Unix()
	if cur == "" {
		return false, s.SetMeta(metaEmbedModel, model, now)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM fragment_vectors`); err != nil {
		return false, err
	}
	// Figure vectors in the text space came from the same model. Image-space
	// ones did not, and stay.
	if _, err := tx.Exec(`DELETE FROM media_vectors WHERE space IN (?, ?)`, spaceText, spaceImageAligned); err != nil {
		return false, err
	}
	if _, err := tx.Exec(
		`INSERT INTO index_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		metaEmbedModel, model, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
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
		WHERE fv.fragment_id IS NULL AND `+SQLOwnWordsF).Scan(&n)
	return n, err
}

// FragmentCount is the number of fragments, for the cost of a re-embed.
func (s *Store) FragmentCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM fragments f WHERE ` + SQLOwnWordsF).Scan(&n)
	return n, err
}

// BackfillVectors embeds up to batch fragments that have no vector, then figure
// descriptions with no text-space vector, and returns how many it embedded. 0
// means there is nothing left (or no embedder).
func (s *Store) BackfillVectors(ctx context.Context, batch int) (int, error) {
	if s.embedder == nil {
		return 0, nil
	}
	n, err := s.backfill(ctx, batch,
		`SELECT f.id, f.text FROM fragments f
		   LEFT JOIN fragment_vectors fv ON fv.fragment_id = f.id
		  WHERE fv.fragment_id IS NULL AND `+SQLOwnWordsF+` ORDER BY f.id LIMIT ?`,
		`INSERT OR REPLACE INTO fragment_vectors (fragment_id, dim, vec) VALUES (?, ?, ?)`)
	if err != nil || n > 0 {
		return n, err
	}
	return s.backfill(ctx, batch,
		`SELECT m.id, m.description FROM media m
		   LEFT JOIN media_vectors mv ON mv.media_id = m.id
		  WHERE mv.media_id IS NULL AND TRIM(m.description) <> '' ORDER BY m.id LIMIT ?`,
		`INSERT OR REPLACE INTO media_vectors (media_id, dim, vec, space) VALUES (?, ?, ?, '`+spaceText+`')`)
}

func (s *Store) backfill(ctx context.Context, batch int, sel, ins string) (int, error) {
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
	vecs, err := s.embedder.EmbedDocs(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("raglit: re-embed: %w", err)
	}
	if len(vecs) != len(ids) {
		return 0, fmt.Errorf("raglit: re-embed: %d vectors for %d texts", len(vecs), len(ids))
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
