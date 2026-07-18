package raglit

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
)

// Semantic (vector) search — the opt-in tier above BM25.
//
// Embeddings come from an OpenAI-compatible /v1/embeddings endpoint (bonsai's
// nomic-embed-text). Vectors are L2-NORMALIZED at store time, so cosine
// similarity is just a dot product. They live in a plain sqlite BLOB column and
// search is a BRUTE-FORCE scan — no index. That is deliberate: for a local
// corpus (thousands of fragments) a linear scan is microseconds, and it keeps
// the pure-Go / single-binary property (modernc sqlite can't load a C vector
// extension like sqlite-vec). A custom NSW/HNSW sidecar is the escalation IF a
// scan ever gets slow — measured, not assumed.

// VectorClient is the sliver of *llm.Client the embedder needs. An interface so
// tests supply deterministic vectors without a network.
type VectorClient interface {
	Embed(ctx context.Context, model string, input []string) ([][]float32, error)
}

// Embedder turns text into normalized vectors. nomic-embed-text is ASYMMETRIC:
// documents and queries must carry different task prefixes or retrieval quality
// drops, so DocPrefix / QueryPrefix default to the nomic convention. Override
// them (to "") for a model that doesn't use prefixes.
type Embedder struct {
	Client      VectorClient
	Model       string
	DocPrefix   string
	QueryPrefix string
}

// NewEmbedder builds an Embedder with the nomic prefixes.
func NewEmbedder(c VectorClient, model string) *Embedder {
	return &Embedder{
		Client:      c,
		Model:       model,
		DocPrefix:   "search_document: ",
		QueryPrefix: "search_query: ",
	}
}

// EmbedDocs embeds document fragments (DocPrefix), normalized.
func (e *Embedder) EmbedDocs(ctx context.Context, texts []string) ([][]float32, error) {
	in := make([]string, len(texts))
	for i, t := range texts {
		in[i] = e.DocPrefix + t
	}
	vecs, err := e.Client.Embed(ctx, e.Model, in)
	if err != nil {
		return nil, err
	}
	for _, v := range vecs {
		normalize(v)
	}
	return vecs, nil
}

// EmbedQuery embeds a search query (QueryPrefix), normalized.
func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.Client.Embed(ctx, e.Model, []string{e.QueryPrefix + text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("raglit: embedder returned no vector")
	}
	normalize(vecs[0])
	return vecs[0], nil
}

// normalize scales v to unit L2 length in place (a zero vector is left as-is).
func normalize(v []float32) {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	n := float32(math.Sqrt(sum))
	if n == 0 {
		return
	}
	for i := range v {
		v[i] /= n
	}
}

// dot is the dot product; for unit vectors it equals cosine similarity.
func dot(a, b []float32) float32 {
	var s float32
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		s += a[i] * b[i]
	}
	return s
}

// encodeVec / decodeVec store a vector as little-endian float32 bytes.
func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
