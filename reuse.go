package raglit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Vector reuse by text (R12a).
//
// A vector is a function of the text and the model: the same text given to the
// same model returns the same vector. A re-ingest used to embed every fragment
// again, although an edit changes a few of them. Before embedding, each
// fragment's text_key is looked up in this index (which holds this document's
// fragments until the commit replaces them, and every other document's) and,
// on a branch, in the parent. A hit is copied and costs no embed call. Only
// text with no stored vector is sent to the embedder.
//
// Reuse is only within one model's space. An index whose embed_model record
// names another model, or whose vectors have another length, gives nothing. An
// index with no record is taken to hold the configured model's vectors, the
// same assumption SyncEmbedModel makes when it adopts the model.
//
// A drop (DropVectors, SyncEmbedModel) bumps vecGen. Vectors looked up before
// it are not written after it (vectorsCurrent), and after a DropVectors a
// branch does not read its parent (metaVectorsDropped): a forced re-embed
// under the same model name must not get the old vectors back as copies.

// textKey is fragments.text_key: hex sha256 of the text the embedder is given,
// less its DocPrefix. The prefix is fixed per model, so it adds nothing to the
// key.
func textKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// reuseCompatible reports whether s's stored vectors are in emb's space, and
// the vector length they have (0 when unknown).
func (s *Store) reuseCompatible(emb *Embedder) (ok bool, dim int) {
	if rec := s.EmbedModel(); rec != "" && rec != emb.Model {
		return false, 0
	}
	_, dim = s.embedDims()
	return true, dim
}

// reuseChunk bounds the keys in one lookup query, inside SQLite's limit on
// bound parameters.
const reuseChunk = 500

// reusableVectors returns a stored vector for each of keys that has one in
// emb's space: in s, then in s's branch parent (text_key holds no path, so
// nothing is translated). Keys with no vector are absent from the map.
func (s *Store) reusableVectors(ctx context.Context, emb *Embedder, keys []string) (map[string][]float32, error) {
	out := map[string][]float32{}
	if emb == nil || len(keys) == 0 {
		return out, nil
	}
	if err := s.collectReusable(ctx, emb, keys, 0, out); err != nil {
		return nil, err
	}
	return out, nil
}

// collectReusable adds to out the vectors s holds for keys not yet in out.
// want is the vector length a reused vector must have, 0 for any; the first
// store with a known length sets it for the parents after it.
func (s *Store) collectReusable(ctx context.Context, emb *Embedder, keys []string, want int, out map[string][]float32) error {
	s.vecMu.RLock()
	ok, dim := s.reuseCompatible(emb)
	if ok && want > 0 && dim > 0 && dim != want {
		ok = false
	}
	if want == 0 {
		want = dim
	}
	var err error
	if ok {
		err = s.lookupVectors(ctx, keys, want, out)
	}
	_, dropped := s.Meta(metaVectorsDropped)
	s.vecMu.RUnlock()
	if err != nil {
		return err
	}
	if s.parent == nil || dropped {
		return nil
	}
	var rest []string
	for _, k := range keys {
		if _, hit := out[k]; !hit {
			rest = append(rest, k)
		}
	}
	if len(rest) == 0 {
		return nil
	}
	return s.parent.collectReusable(ctx, emb, rest, want, out)
}

// lookupVectors reads one vector per key from s's own fragments.
func (s *Store) lookupVectors(ctx context.Context, keys []string, want int, out map[string][]float32) error {
	for start := 0; start < len(keys); start += reuseChunk {
		chunk := keys[start:min(start+reuseChunk, len(keys))]
		args := make([]any, len(chunk))
		for i, k := range chunk {
			args[i] = k
		}
		rows, err := s.db.QueryContext(ctx, `SELECT f.text_key, fv.dim, fv.vec FROM fragments f
			JOIN fragment_vectors fv ON fv.fragment_id = f.id
			WHERE f.text_key IN (?`+strings.Repeat(",?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key string
			var dim int
			var blob []byte
			if err := rows.Scan(&key, &dim, &blob); err != nil {
				rows.Close()
				return err
			}
			if _, have := out[key]; have || (want > 0 && dim != want) {
				continue
			}
			if v := decodeVec(blob); len(v) == dim && dim > 0 {
				out[key] = v
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// embedReusing returns a vector for each of texts, in order: a stored one where
// the text has one (reusableVectors), else one from emb. Repeats among the
// misses are embedded once. It reports how many came from each.
func (s *Store) embedReusing(ctx context.Context, emb *Embedder, texts []string) (vecs [][]float32, reused, embedded int, err error) {
	have, err := s.reusableVectors(ctx, emb, textKeys(texts))
	if err != nil {
		return nil, 0, 0, err
	}
	return embedMisses(ctx, emb, texts, have)
}

func textKeys(texts []string) []string {
	keys := make([]string, len(texts))
	for i, t := range texts {
		keys[i] = textKey(t)
	}
	return keys
}

// embedMisses is embedReusing over vectors already looked up (have, by
// text_key). Store.Ingest looks them up before its transaction deletes the
// document's fragments, and embeds after it commits.
func embedMisses(ctx context.Context, emb *Embedder, texts []string, have map[string][]float32) (vecs [][]float32, reused, embedded int, err error) {
	keys := textKeys(texts)
	vecs = make([][]float32, len(texts))
	var miss []string
	missAt := map[string]int{}
	for i, k := range keys {
		if v, ok := have[k]; ok {
			vecs[i] = v
			reused++
			continue
		}
		if _, ok := missAt[k]; !ok {
			missAt[k] = len(miss)
			miss = append(miss, texts[i])
		}
	}
	if len(miss) == 0 {
		return vecs, reused, 0, nil
	}
	out, err := emb.EmbedDocs(ctx, miss)
	if err != nil {
		return nil, 0, 0, err
	}
	for i, k := range keys {
		if vecs[i] == nil {
			if j := missAt[k]; j < len(out) {
				vecs[i] = out[j]
			}
		}
	}
	return vecs, reused, len(miss), nil
}
