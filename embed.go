package raglit

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
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
	// BatchLimitChars caps ONE embed request's total input. 0 → a conservative
	// default. The server's limit applies to the whole request, so this bounds
	// the batch, not the individual fragment.
	BatchLimitChars int
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

// defaultEmbedBatchChars bounds ONE embed request.
//
// The server applies its batch limit to the WHOLE request, not to each input.
// llama.cpp reports it as: "input (35871 tokens) is too large to process.
// increase the physical batch size (current batch size: 8192)" — and 35871 is
// sixteen ordinary fragments added together, not one big one. The caller was
// batching by ITEM COUNT, so a document with large fragments overflowed a limit
// nothing was measuring.
//
// Sized in characters because that is what the caller has without tokenizing:
// ~3 chars per token against an 8192-token batch, with headroom for the
// DocPrefix each input carries. Override with BatchLimitChars when the endpoint
// is known to allow more.
const defaultEmbedBatchChars = 24000

// batchLimitChars is the effective per-request character budget.
func (e *Embedder) batchLimitChars() int {
	if e.BatchLimitChars > 0 {
		return e.BatchLimitChars
	}
	return defaultEmbedBatchChars
}

// EmbedDocs embeds document fragments (DocPrefix), normalized.
//
// Splits into as many requests as the endpoint's batch budget needs. Chunking
// lives HERE rather than at the call sites so every caller is covered by
// construction — there are three, and the one that overflowed was the only one
// anybody would have thought to fix.
func (e *Embedder) EmbedDocs(ctx context.Context, texts []string) ([][]float32, error) {
	in := make([]string, len(texts))
	for i, t := range texts {
		in[i] = e.DocPrefix + t
	}
	budget := e.batchLimitChars()
	out := make([][]float32, 0, len(in))
	for start := 0; start < len(in); {
		end, n := start, 0
		for end < len(in) {
			// An input larger than the whole budget still goes, alone. It will
			// fail if the endpoint really cannot take it, and that is a genuine
			// error about that fragment rather than a batching artefact.
			if end > start && n+len(in[end]) > budget {
				break
			}
			n += len(in[end])
			end++
		}
		vecs, err := e.Client.Embed(ctx, e.Model, in[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
		start = end
	}
	for _, v := range out {
		normalize(v)
	}
	return out, nil
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

// maxEmbedProbeChars bounds DiscoverEmbedLimit so a tolerant endpoint (one that
// silently truncates instead of erroring) terminates rather than growing forever.
const maxEmbedProbeChars = 200000

// DiscoverEmbedLimit probes the largest single input (in characters) the embed
// endpoint accepts without error — the same shape as llm.DiscoverContext. It
// grows exponentially to find a length that FAILS, then binary-searches the
// boundary. Only works on endpoints that REJECT an over-long input; a tolerant
// endpoint that truncates returns maxEmbedProbeChars (treated as "effectively
// unbounded"). Store the result in Config.EmbedLimitChars to cap the fragment
// ceiling by the model, not by taste.
func (e *Embedder) DiscoverEmbedLimit(ctx context.Context) (int, error) {
	accepts := func(n int) bool {
		_, err := e.EmbedDocs(ctx, []string{strings.Repeat("a ", n/2+1)[:n]})
		return err == nil
	}
	const floor = 256
	if !accepts(floor) {
		return 0, fmt.Errorf("raglit: embed endpoint rejected even a %d-char input", floor)
	}
	lo := floor
	hi := 0
	for n := floor * 2; n <= maxEmbedProbeChars; n *= 2 {
		if accepts(n) {
			lo = n
			if n == maxEmbedProbeChars {
				return maxEmbedProbeChars, nil
			}
			continue
		}
		hi = n
		break
	}
	if hi == 0 {
		return maxEmbedProbeChars, nil // never failed under the cap
	}
	for hi-lo > floor {
		mid := (lo + hi) / 2
		if accepts(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo, nil
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
