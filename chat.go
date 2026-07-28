package raglit

import (
	"context"
	"fmt"

	"github.com/iodesystems/agentkit/agent"
	"github.com/iodesystems/agentkit/llm"
)

// The model seam for the two VERBATIM-RE-EMISSION stages — OCR transcription and
// llm-seg fragmentation. Both hand the model content and ask for it back
// faithfully, which is the shape most prone to a degenerate loop: the output is
// supposed to look like the input, so a model that starts repeating a clause is
// doing something that locally resembles the task.
//
// Measured, on a legal property description: ONE call produced 162,000 tokens
// from a 4,112-token unit — 40x the input, ~30 minutes of GPU, re-emitting an
// 85-character fragment. Two things had to be true for that to be possible, and
// this file removes both:
//
//   - It ran NON-STREAMING (llm.Client.Chat), so nothing could see the
//     repetition until the whole body arrived. Streaming gives agentkit's
//     RepetitionGuard the per-token view it needs to cut the stream — closing
//     the body disconnects, and the server abandons the generation.
//   - It passed NO ChatOpts, so max_tokens was whatever the server chose. A
//     faithful re-emission is bounded by its INPUT; anything far past that is
//     not transcription, whatever it looks like. See maxTokensFor.
//
// The two bounds are independent on purpose. The guard catches exact periodic
// repetition; the cap catches everything else that runs long (drift that never
// repeats byte-for-byte, a model that will not stop). Neither subsumes the other.

// Chatter is the sliver of *llm.Client the OCR and segmentation paths need: one
// streaming completion. An interface so the model can be stubbed in tests.
//
// STREAMING, not Chat: see the file doc. A non-streaming client cannot be
// protected from a runaway generation, because by the time it returns, the
// tokens are already paid for.
type Chatter interface {
	ChatStream(ctx context.Context, messages []llm.Message, tools []llm.ToolDef,
		opts *llm.ChatOpts) (<-chan llm.StreamChunk, error)
}

// collectStream drains a streaming completion into one string.
//
// It returns rep non-nil when the transport CUT the generation because it had
// collapsed into repetition. That is not an error: the text collected up to the
// cut is real, and each caller decides what a partial result means for it — the
// segmenter retries the unit, the OCR path refuses to index a partial page.
//
// The channel is always drained to completion, even after a cut. agentkit's
// reader goroutine sends on a buffered channel; abandoning it mid-stream would
// park that goroutine forever.
func collectStream(ctx context.Context, c Chatter, msgs []llm.Message,
	opts *llm.ChatOpts) (text string, rep *llm.RepetitionInfo, err error) {
	ch, err := c.ChatStream(ctx, msgs, nil, opts)
	if err != nil {
		return "", nil, err
	}
	var b []byte
	var streamErr string
	for chunk := range ch {
		if chunk.Error != "" && streamErr == "" {
			streamErr = chunk.Error
		}
		if chunk.Content != "" {
			b = append(b, chunk.Content...)
		}
		if chunk.StopReason == llm.StopReasonRepetition && rep == nil {
			rep = chunk.Repetition
		}
	}
	if streamErr != "" {
		return string(b), rep, fmt.Errorf("%s", streamErr)
	}
	return string(b), rep, nil
}

// maxTokensFor bounds a re-emission of input at roughly twice its size.
//
// Twice, not once: the content comes back wrapped (JSON string escaping turns a
// newline into two characters, a quote into two) and the estimator is bytes/4,
// which overcounts. Measured against real units, a faithful answer lands near
// 1.0-1.3x; 2x plus a floor leaves honest work untouched while cutting the 30x
// and 40x runaways by an order of magnitude.
//
// Overshooting the cap is SAFE here by construction: a truncated answer fails
// the segmenter's schema validation and falls back to "the whole unit is one
// fragment", which is the behavior that predates llm-seg entirely.
func maxTokensFor(input string) int {
	n := 2*agent.Default().Estimate(input) + 512
	if n < minReEmitTokens {
		return minReEmitTokens
	}
	return n
}

// loopBreakSampling copies opts with sampling that gives the decoder a way OUT
// of a repeating basin, for the ONE retry after a cut.
//
// Retrying a cut generation UNCHANGED is pointless, and we measured exactly
// that: the same survey page was cut three times in 34 seconds (15:58:27,
// 15:58:44, 15:59:01) with byte-identical output, because the server runs
// --temp 0 and a greedy decoder cannot leave the basin it is in. The context
// was the same, the weights were the same, so the output was the same.
//
// So the retry changes the SAMPLER, not the prompt. The temperature bump is
// deliberately small: this is transcription, and a hot decoder invents text
// that looks exactly like the survey it is supposed to be reading. Enough
// entropy to break a tie, not enough to start composing.
func loopBreakSampling(o *llm.ChatOpts) *llm.ChatOpts {
	temp, freq := loopBreakTemp, loopBreakFreqPenalty
	cp := llm.ChatOpts{}
	if o != nil {
		cp = *o
	}
	cp.Temperature = &temp
	cp.FrequencyPenalty = &freq
	return &cp
}

const (
	// loopBreakTemp / loopBreakFreqPenalty are the retry's sampling. Small on
	// purpose — see loopBreakSampling.
	loopBreakTemp        = 0.3
	loopBreakFreqPenalty = 0.5

	// minReEmitTokens floors maxTokensFor so a tiny unit still has room for the
	// JSON envelope and a fix-loop retry's preamble.
	minReEmitTokens = 1024

	// defaultOCRMaxTokens caps a page transcription. A dense page of prose is
	// ~1500 tokens; a page image cannot faithfully transcribe to eight thousand,
	// so past this the model is not transcribing any more. Unlike the segmenter
	// there is no input text to scale from — the input is an image.
	defaultOCRMaxTokens = 8192
)
