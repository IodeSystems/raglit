package raglit

import (
	"context"
	"strconv"
	"time"
)

// What the CHAT endpoint will accept as one request, IN TOKENS.
//
// The embed endpoint's limit has been discovered and cached since a document
// failed with a 500 about batch sizes (embed.go). The chat endpoint has the same
// constraint, refuses in the same words, and had none of this: no probe, no
// cached answer, and nothing capping what the segmenter sent. The fragment
// ceiling was measured against the embedder while the REQUEST CARRYING IT was
// measured against nothing.
//
// What that cost, on one corpus: twenty ingest jobs across ten PDFs died on
//
//	input (14969 tokens) is too large to process.
//	increase the physical batch size (current batch size: 8192)
//
// and nine of those files had no row in the index at all — the summary-judgment
// brief, two declarations, the requests for admission and their sworn answers,
// two purchase and sale agreements. Search returned nothing from them and said
// nothing about it.
//
// TOKENS, not characters, because that is the unit of the message above and of
// every limit an endpoint states. The first version of this stored characters
// and converted at an assumed two characters per token. Measured against the
// model's own tokenizer, this corpus runs 4.66 characters per token on legal
// prose and 1.16 on a survey's metes and bounds — so the assumed bound was 4×
// too tight for one document and past the limit for the other, and only the
// second kind of error loses anything.

// ContextProber is the part of *llm.Client this needs. An interface so a test
// can answer without an endpoint, and so raglit does not take a dependency on
// the client type to ask two questions.
type ContextProber interface {
	// ContextWindow is the limit as the SERVER states it (llama.cpp /props),
	// ok=false when it does not state one.
	ContextWindow(ctx context.Context) (int, bool)
	// DiscoverContext measures it by overflowing, which costs O(log N) requests
	// and returns a lower bound. The fallback, not the first choice.
	DiscoverContext(ctx context.Context) (int, error)
}

// chatInputLimitKey names the stored answer for one model. Keyed by model,
// because the limit belongs to the model and a number found for one is a guess
// about another.
func chatInputLimitKey(model string) string { return "chat_input_limit_tokens:" + model }

// ChatInputLimitTokens returns the largest single request this index's chat
// model accepts, in tokens, asking once and remembering the answer.
//
// Asked before probed. A server that states its own n_ctx is one request for the
// real number; the probe is O(log N) requests for a lower bound, and on an
// endpoint that accepts anything it returns its own ceiling rather than a fact.
//
// `configured` wins over both: an operator who knows the number should not wait,
// and a proxy that accepts a prompt its backend will later refuse cannot be
// measured from outside at all.
//
// Zero means "unknown, do not cap" — the same reading the fragment ceiling gives
// it. An invented limit would cut pages that were fine, and the failure it would
// be guarding against announces itself in the endpoint's own words.
func (s *Store) ChatInputLimitTokens(ctx context.Context, p ContextProber, model string, configured int) int {
	if configured > 0 {
		return configured
	}
	if p == nil || model == "" {
		return 0
	}
	key := chatInputLimitKey(model)
	if v, ok := s.Meta(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	n, ok := p.ContextWindow(ctx)
	if !ok || n <= 0 {
		probed, err := p.DiscoverContext(ctx)
		if err != nil || probed <= 0 {
			return 0
		}
		n = probed
	}
	// Leave room for the answer. The segmenter re-emits its input as fragments,
	// so a request sized to the whole window has nowhere to put the reply, and
	// the failure — a generation cut mid-JSON — reads as the model misbehaving.
	n = n * chatInputShare / 100
	if n <= 0 {
		return 0
	}
	_ = s.SetMeta(key, strconv.Itoa(n), time.Now().Unix())
	return n
}

// chatInputShare is how much of the context window the INPUT may use.
//
// The segmenter's answer is its input re-emitted as JSON, so the reply is the
// same order of size as the request. Half leaves room for it with margin for the
// JSON overhead; the alternative is discovering the shortfall as a truncated
// generation, which the fix loop then spends three attempts on.
const chatInputShare = 50
