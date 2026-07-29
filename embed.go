package raglit

import (
	"context"
	"encoding/binary"
	"fmt"
	agent "github.com/iodesystems/agentkit/agent"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// defaultEmbedBatchChars bounds ONE embed request — for latency and memory, NOT
// for correctness.
//
// Correcting an earlier claim of mine, since the code was built on it: the
// endpoint's limit is PER INPUT, not per request. Measured directly — 16 inputs
// of ~3k tokens each (48k in total) embed fine, one input of 8.3k tokens does
// not. The error's wording is what misled me: llama.cpp says "input (35871
// tokens) is too large to process. increase the physical batch size (current
// batch size: 8192)", and "batch size" there is n_batch applied to a single
// sequence, not a sum over the request.
//
// So the 35,871-token failure was ONE fragment of about 67,000 characters — the
// largest that `raglit refragment` later found — and not sixteen ordinary ones
// added together. The fix that matters is the per-fragment ceiling; chunking a
// request is only about keeping any single call small.
const defaultEmbedBatchChars = 16384

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

// defaultEmbedLimitTokens is nomic-embed-text's NATIVE context, kept as the
// documented figure behind nativeEmbedTokens. Anything above
// it is RoPE-extended — supported by some builds, but the quality of an
// extended embedding is not the quality of a native one, and the endpoint here
// reports 8192 as its batch size outright.
//
// Stated rather than probed because the probe cannot express this honestly. It
// grows a filler string of "a ", which tokenizes at about TWO characters per
// token; real prose runs past four. Measured on the live endpoint: 16,500 chars
// of filler is 8,252 tokens and rejected, while 34,000 chars of legal prose fits
// inside the same 8192. A character limit calibrated on filler is therefore less
// than half the real budget — safe, but it splits fragments that were fine.
const defaultEmbedLimitTokens = 8192

// worstCaseCharsPerToken is the FLOOR, measured against the live endpoint, and
// it is what makes a character budget a guarantee rather than a hope.
//
// agentkit's estimator is len/4, which is right for prose and half the truth for
// anything denser. Measured: 16,500 characters of "a " is 8,252 tokens — two
// characters per token — while 35,700 characters of legal prose is about 8,900.
// A budget built on the prose ratio would let a dense fragment through at twice
// its assumed size, which is exactly the overflow this is supposed to prevent.
//
// The cost is real and worth stating: for ordinary prose this splits at ~16k
// characters where ~32k would have fitted. That is the price of never failing,
// paid in slightly smaller fragments, and it is the right way round — an
// oversized fragment does not degrade, it errors and takes its document with it.
const worstCaseCharsPerToken = 2

// EstimateTokens is the token count raglit assumes for a piece of text.
//
// Deliberately an OVER-estimate: it must never say a string is smaller than the
// tokenizer will. Shared so fragment sizing and batch sizing agree, because
// disagreeing about the unit is how a limit gets enforced in one place and
// ignored in another.
func EstimateTokens(s string) int {
	n := len(s)/worstCaseCharsPerToken + specialTokenAllowance
	if est := agent.Default().Estimate(s); est > n {
		n = est
	}
	return n
}

// specialTokenAllowance covers what the tokenizer adds beyond the text itself.
//
// Even the worst-case ratio is not quite worst enough: measured, 16,500
// characters of "a " is 8,252 tokens where len/2 predicts 8,250. Two tokens, from
// the BOS/EOS pair the encoder wraps every input in. That is a rounding error
// everywhere except exactly at the boundary, which is the one place it would
// ever be noticed — as an occasional rejection with no apparent pattern.
const specialTokenAllowance = 16

// TokensToChars converts a token budget to the character budget that cannot
// exceed it, whatever the text.
//
// The exact inverse of EstimateTokens, allowance included. Without subtracting
// it the budget overshot its own cap by the allowance — a budget that fails its
// own check is worse than no budget, because it looks like a guarantee.
func TokensToChars(tokens int) int {
	n := (tokens - specialTokenAllowance) * worstCaseCharsPerToken
	if n < 1 {
		return 1
	}
	return n
}

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

// embedLimitKey names the stored probe for one model. Keyed by model because
// the limit is a property of the model, and a number probed for one is a guess
// about another.
func embedLimitKey(model string) string { return "embed_limit_chars:" + model }

// Meta reads a per-index setting.
func (s *Store) Meta(key string) (string, bool) {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM index_meta WHERE key = ?`, key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

// SetMeta records a per-index setting.
func (s *Store) SetMeta(key, value string, now int64) error {
	_, err := s.db.Exec(
		`INSERT INTO index_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now)
	return err
}

// EmbedLimitChars returns the largest single input this index's embed model
// accepts, probing once and remembering the answer.
//
// Discovered rather than configured, because it is a fact about the endpoint. It
// was previously left at zero here — meaning "unknown", which ResolveFragParams
// reads as "no cap" — so fragments were sized by taste (9000 characters) with
// nothing checking them against what the model would take. The first symptom was
// a document failing with a 500 about batch sizes.
//
// `configured` wins when set: an operator who knows the number should not have
// to wait for a probe, and an endpoint that silently truncates cannot be probed
// at all.
func (s *Store) EmbedLimitChars(ctx context.Context, e *Embedder, configured int) int {
	if configured > 0 {
		return configured
	}
	if e == nil || e.Model == "" {
		return 0
	}
	key := embedLimitKey(e.Model)
	if v, ok := s.Meta(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	// A model whose native context is known needs no probe.
	if tok, ok := nativeEmbedTokens[e.Model]; ok {
		n := TokensToChars(tok)
		_ = s.SetMeta(key, strconv.Itoa(n), time.Now().Unix())
		return n
	}
	// Otherwise probe — and the probe is exactly right for this purpose, which
	// took a wrong turn to see. It grows a filler of "a ", which tokenizes at
	// two characters per token: the WORST case, and therefore the number that
	// holds for any text. Reading its answer as "too conservative for prose"
	// was the error; conservative is what a guarantee is made of.
	n, err := e.DiscoverEmbedLimit(ctx)
	if err != nil || n <= 0 {
		// Unprobeable endpoints exist — some truncate silently rather than
		// reporting. Record nothing and stay uncapped rather than inventing a
		// limit that would split good fragments for no reason.
		return 0
	}
	_ = s.SetMeta(key, strconv.Itoa(n), time.Now().Unix())
	return n
}

// nativeEmbedTokens is the NATIVE context of models whose limit is known.
//
// Native, not extended: a build may accept more through RoPE interpolation, but
// the quality of an extended embedding is not the quality of a native one, and
// this endpoint reports 8192 as its batch size outright.
var nativeEmbedTokens = map[string]int{
	"nomic-embed-text":      8192,
	"nomic-embed-text-v1":   8192,
	"nomic-embed-text-v1.5": 8192,
}

// OversizedDocs lists documents holding a fragment larger than limit, with the
// worst offender's size. These are the documents whose fragments were cut to a
// standard the embed model does not accept.
func (s *Store) OversizedDocs(limit int) (map[string]int, error) {
	out := map[string]int{}
	if limit <= 0 {
		return out, nil
	}
	rows, err := s.db.Query(
		`SELECT d.path, MAX(LENGTH(f.text)) AS worst
		   FROM fragments f JOIN documents d ON d.id = f.doc_id
		  GROUP BY d.id HAVING worst > ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out[p] = n
	}
	return out, rows.Err()
}

// MarkForReingest clears a document's content hash so the next ingest redoes it.
//
// The hash is the dedup lever, so clearing it is the least destructive way to
// force a re-read: the document row, its fragments and its OCR page cache all
// survive, and the pages already transcribed are not read again.
func (s *Store) MarkForReingest(path string) error {
	_, err := s.db.Exec(`UPDATE documents SET content_hash = '' WHERE path = ?`, path)
	return err
}

// perInputTokenLimit reads the endpoint's own limit out of a rejection.
//
// The authoritative number, from the only party that knows it: llama.cpp
// answers an over-long input with "input (8302 tokens) is too large to process.
// increase the physical batch size (current batch size: 8192)". The trailing
// figure is the real per-input ceiling, whatever a table or a probe believed.
//
// Note the wording is misleading and cost an hour: "batch size" is n_batch
// applied to ONE sequence. Measured, sixteen inputs totalling 48k tokens pass
// while a single 8.3k input fails.
var batchSizeRe = regexp.MustCompile(`(?i)batch size:\s*(\d+)`)

func perInputTokenLimit(errText string) (int, bool) {
	m := batchSizeRe.FindStringSubmatch(errText)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// LearnLimitFromError records a limit the endpoint stated in a rejection.
//
// Better than the table and better than the probe, because it is not an
// inference: the server named its own number. Called on an embed failure so the
// next ingest is sized correctly even if the model was unknown or was swapped
// under a stored value.
func (s *Store) LearnLimitFromError(model, errText string) (int, bool) {
	tok, ok := perInputTokenLimit(errText)
	if !ok || model == "" {
		return 0, false
	}
	chars := TokensToChars(tok)
	if err := s.SetMeta(embedLimitKey(model), strconv.Itoa(chars), time.Now().Unix()); err != nil {
		return 0, false
	}
	return chars, true
}
