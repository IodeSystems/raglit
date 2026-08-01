package raglit

import (
	"context"
	"strconv"
	"time"
)

// What the CHAT endpoint will accept as one request.
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
// Discovered rather than configured for the same reason as the embed limit: it
// is a fact about the endpoint, and a number somebody typed goes stale the first
// time the server is restarted with different flags.

// ContextProber is the part of *llm.Client this needs. An interface so a test
// can answer without an endpoint, and so raglit does not take a dependency on
// the client type to ask one question.
type ContextProber interface {
	DiscoverContext(ctx context.Context) (int, error)
}

// chatInputLimitKey names the stored probe for one model. Keyed by model,
// because the limit belongs to the model and a number probed for one is a guess
// about another.
func chatInputLimitKey(model string) string { return "chat_input_limit_chars:" + model }

// ChatInputLimitChars returns the largest single request this index's chat model
// accepts, in characters, probing once and remembering the answer.
//
// `configured` wins when set: an operator who knows the number should not wait
// for a probe, and an endpoint that accepts anything cannot be probed for a
// boundary that is not there.
//
// Zero means "unknown, do not cap" — the same reading the fragment ceiling gives
// it. An invented limit would cut pages that were fine, and the failure it would
// be guarding against announces itself in the endpoint's own words.
func (s *Store) ChatInputLimitChars(ctx context.Context, p ContextProber, model string, configured int) int {
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
	tokens, err := p.DiscoverContext(ctx)
	if err != nil || tokens <= 0 {
		return 0
	}
	// Tokens to characters at the WORST case, which is what makes the answer a
	// bound rather than an average. The probe counts in whole words; a request of
	// punctuation and digits tokenizes far denser, and a cap that holds only for
	// prose is not a cap on a corpus of court documents.
	n := TokensToChars(tokens)
	_ = s.SetMeta(key, strconv.Itoa(n), time.Now().Unix())
	return n
}
