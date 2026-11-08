package raglit

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/iodesystems/agentkit/llm"
)

// ModelRole is one model a worker calls: where it is served, the key for it,
// and the model id. Model empty → the role is off.
type ModelRole struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Models is everything NewWorker needs to know about the models and the
// ingest settings. Plain data: it does not read a Home, a config file or
// flags. raglit fills it from config.json and its flags; an embedder of the
// library fills it from its own config.
type Models struct {
	// Vision reads page images (OCR). Empty Model → scans and images are read by
	// the local engine alone (tesseract or OCR.CheapEngine), or fail as
	// unreadable when there is none.
	Vision ModelRole
	// Segment splits transcribed text into fragments. Empty Model → the vision
	// model; empty BaseURL → the vision endpoint.
	Segment ModelRole
	// Embed makes vectors. Here it is only probed for its input limit: the
	// embedder itself is set on the Store or Registry.
	Embed ModelRole
	// Audio is the speech-to-text model. Empty Model → audio jobs fail with a
	// clear message.
	Audio ModelRole
	// Identity captions each ingested document (identity.go). Empty Model →
	// identity is off. Empty BaseURL → the segment endpoint.
	Identity ModelRole
	// OCR selects and tunes the cheap first-pass engine, and its default
	// strategy supplies the resolution policy.
	OCR OCRConfig
	// Fragmenter parameters. Zero → the defaults (ResolveFragParams).
	FragWindow, FragStride, FragFloor int
	// Configured input limits. Zero → probed from the endpoint and cached on
	// the index.
	EmbedLimitChars, EmbedLimitTokens, SegmentInputLimitTokens int
	// Channels is the per-model admission registry (modelchan.go). Model
	// capacity is a property of the SERVER, so pass one registry for the
	// process, shared by every index's worker. nil → a fresh registry per
	// NewWorker call.
	Channels *Channels
}

// IngestRetry5xxAttempts rides out an upstream episode of roughly two minutes.
// With exponential backoff capped at 30s the schedule is about
// 1+2+4+8+16+30+30+30s; RetryBudget still stops it after that.
//
// Ingest is a BATCH, not a chat turn, and agentkit's default 5xx cap of five
// attempts is tuned for the latter. A 33-page document is ~33 requests, so
// one upstream blip outlasting ~15s of backoff kills the whole document —
// measured on a real corpus, nothing over 20 pages ever completed.
//
// The wall clock is still bounded by RetryBudget, which is the guard that
// should bind here. Raising attempts trades a longer wait on a sick upstream
// for finishing the document, which for a transcription backlog is the right
// trade: the alternative is not "fail fast", it is "re-read 30 pages".
const IngestRetry5xxAttempts = 9

// ingestClient is a model client with the batch retry policy above. Every
// client NewWorker builds uses it: a segmentation or a caption that gives up
// mid-document costs the same re-read a failed page does, and the embedder
// was once the one client built without it, which is why its failures logged
// a different retry cap than the OCR client and sent an investigation down the
// wrong path.
func ingestClient(r ModelRole) *llm.Client {
	c := llm.NewClient(r.BaseURL, r.APIKey, r.Model)
	c.Retry5xxAttempts = IngestRetry5xxAttempts
	return c
}

// gate wraps a chat client in its model's admission channel and routes the
// client's backpressure into the controller.
//
// Both halves in one call on purpose. A client gated without the 429 hook can
// only ever grow — it would widen until the server rejected it and never learn
// why — and a hook without the gate has nothing to act on. Any tally already on
// the client is chained, not replaced: what a job had to survive is what the
// health report reads.
func gate(chans *Channels, c *llm.Client, model string) Chatter {
	c.OnRetry = chans.Observe(model, c.OnRetry)
	return chans.Gate(c, model)
}

func (m Models) channels() *Channels {
	if m.Channels != nil {
		return m.Channels
	}
	return NewChannels()
}

// segmentRole is the segmenter's role with its fallbacks filled.
func (m Models) segmentRole() ModelRole {
	r := m.Segment
	if r.Model == "" {
		r.Model = m.Vision.Model
	}
	if r.BaseURL == "" {
		r.BaseURL, r.APIKey = m.Vision.BaseURL, m.Vision.APIKey
	}
	return r
}

// Identifier builds the document-identity model client, or returns nil when
// identity is off (no Identity model).
//
// Same batch retry policy as the rest of ingest: this call happens at the end of
// a document that may have cost thirty OCR requests, and losing the caption to a
// transient 502 wastes the one moment the whole transcript is in hand.
func (m Models) Identifier() *Identifier {
	r := m.Identity
	if r.Model == "" {
		return nil
	}
	if r.BaseURL == "" {
		seg := m.segmentRole()
		r.BaseURL, r.APIKey = seg.BaseURL, seg.APIKey
	}
	return NewIdentifier(gate(m.channels(), ingestClient(r), r.Model), r.Model)
}

// AttachCheapOCR enables the cascade's cheap first-pass engine on an OCR, with
// render as the resolution policy. An error is a misconfigured engine; the OCR
// is then left VLM-only, and the caller should report it rather than fail — a
// bad OCR knob must not break ingestion.
func AttachCheapOCR(ocr *OCR, oc OCRConfig, render RenderPolicy) error {
	if ocr == nil {
		return nil
	}
	ocr.DescribeFigures = oc.DescribeFigures
	// Set before the engine build and outside the eng != nil guard, because
	// BaseDPI governs even when nothing can measure glyph height — that is the
	// branch renderDPIFor takes with a nil engine.
	ocr.Render = render
	eng, err := BuildPageEngine(oc)
	if err != nil {
		return err
	}
	if eng != nil {
		ocr.Cheap = eng
		ocr.Gate = oc.Gibberish
		ocr.Assist = strings.EqualFold(strings.TrimSpace(oc.Mode), "assist")
	}
	return nil
}

// attachDefaultCheapOCR is AttachCheapOCR with the PROJECT default strategy's
// resolution policy, not a per-index one: a worker's OCR is built before the
// index a document belongs to is known here. Threading it is what the
// per-index `ocr_strategy` still needs to take effect on the ingest path.
func attachDefaultCheapOCR(ocr *OCR, oc OCRConfig) {
	st := Config{OCR: oc}.StrategyFor("")
	if err := AttachCheapOCR(ocr, oc, st.Render); err != nil {
		fmt.Fprintf(os.Stderr, "raglit: %v — OCR falling back to vision-only\n", err)
	}
}

// TesseractOnlyOCR is an OCR with no vision model: the configured cheap engine,
// else tesseract when it is on PATH. nil when neither exists. Every page is
// taken from the engine as read (OCR.PageAsSeen), and figures are not
// described, since describing one needs the vision model.
func TesseractOnlyOCR(oc OCRConfig) *OCR {
	ocr := NewOCR(nil)
	attachDefaultCheapOCR(ocr, oc)
	if ocr.Cheap == nil {
		t := NewTesseractEngine(oc.TesseractBin, oc.TesseractLang)
		if _, err := exec.LookPath(t.Bin); err != nil {
			return nil
		}
		ocr.Cheap = t
	}
	ocr.DescribeFigures, ocr.Assist = false, false
	return ocr
}

// NewWorker wires a Worker with OCR (PDF) + LLM segmentation (text/code) when a
// vision model is configured; text-window sizing is resolved lazily on the first
// text job (probing + caching the model's context). No model → offline
// blank-line text + PDF-fails-gracefully.
//
// It also sets the store's identifier. pool, when non-nil, enables the
// cross-index pool (daemon only).
func NewWorker(store *Store, m Models, pool *Pool) *Worker {
	if m.Channels == nil {
		m.Channels = NewChannels()
	}
	w := &Worker{Store: store}
	// Audio ingest turns on with the MODEL alone.
	if m.Audio.Model != "" {
		// Gated on the same channels as every other model. The daemon starts one
		// runner per lane slot and will offer several recordings at once; oidio
		// holds a single recogniser behind corrallm, and the width for it is
		// learned from its own 429s rather than assumed here.
		w.STT = &STT{BaseURL: m.Audio.BaseURL, APIKey: m.Audio.APIKey, Model: m.Audio.Model, Chan: m.Channels}
	}
	// Deterministic text fragmenter params (config-or-default), with the fragment
	// ceiling capped by the embed model's probed input limit.
	// The embed model's input limit is DISCOVERED, not configured: it is a fact
	// about the endpoint, and leaving it at zero means "no cap", which is how
	// fragments came to be sized by taste with nothing checking them.
	embedLimit := m.EmbedLimitChars
	embedLimitTokens, embedCounter := 0, TokenCounter(nil)
	if m.Embed.Model != "" {
		ec := ingestClient(m.Embed)
		embedLimit = store.EmbedLimitChars(context.Background(),
			NewEmbedder(ec, m.Embed.Model), m.EmbedLimitChars)
		// The character number above is a conversion; this is the limit itself.
		// A fragment inside the character ceiling can still be past the token one
		// — 16128 characters of a scanned brief is 10240 tokens against an 8192
		// window — and that refusal fails the document at the embed stage.
		embedLimitTokens = store.EmbedLimitTokens(context.Background(), ec,
			m.Embed.Model, m.EmbedLimitTokens)
		if ec.HasTokenizer(context.Background()) {
			embedCounter = ec
		}
		if embedLimitTokens > 0 {
			log.Printf("raglit: embed limit model=%s %d tokens (exact counting: %v)",
				m.Embed.Model, embedLimitTokens, embedCounter != nil)
		}
	}
	w.Frag = FragConfig{
		Window:            m.FragWindow,
		Stride:            m.FragStride,
		Floor:             m.FragFloor,
		EmbedLimit:        embedLimit,
		EmbedLimitTokens:  embedLimitTokens,
		EmbedTokenCounter: embedCounter,
		FigurePrompt:      FigurePromptVersion(),
	}
	cheapEngine := m.OCR.CheapEngine
	if m.Vision.Model != "" {
		client := ingestClient(m.Vision)
		// Every retry, recovery and give-up this client makes, attributed to the
		// job in flight. The client has always reported them; nothing was
		// listening, so a corpus took 244 upstream 5xx and 95 429s with no record
		// of any of it outside a sampled log.
		w.Retries = &RetryTally{}
		client.OnRetry = w.Retries.Observe
		// What the CHAT endpoint accepts as one request, discovered on the same
		// terms as the embed limit above and for the same reason: it is a fact
		// about the endpoint. Until this was probed, the fragments coming BACK
		// were capped by the embedder while the request carrying a page IN was
		// capped by nothing, and a page over the endpoint's physical batch failed
		// its whole document.
		w.Frag.SegmentInputLimit = store.ChatInputLimitTokens(
			context.Background(), client, m.Vision.Model, m.SegmentInputLimitTokens)
		// Counting is exact wherever the endpoint has a tokenizer. Without one,
		// sizes are estimated from a ratio learned per document — which is still
		// four times better than the constant it replaced, but is an estimate.
		if client.HasTokenizer(context.Background()) {
			w.Frag.TokenCounter = client
		}
		if w.Frag.SegmentInputLimit > 0 {
			log.Printf("raglit: chat input limit model=%s %d tokens (exact counting: %v)",
				m.Vision.Model, w.Frag.SegmentInputLimit, w.Frag.TokenCounter != nil)
		}
		// Printed once per index worker so the effective retry policy is visible
		// in the log. Without it, "5xx attempt 2/5" in a log is ambiguous between
		// "the cap was not raised" and "this is not the client you think it is".
		log.Printf("raglit: ocr client model=%s 5xx-attempts=%d", m.Vision.Model, client.Retry5xxAttempts)
		// Gated on the VISION model's own channel — and only it. This is the
		// change: transcription used to be serialised against segmentation and
		// embedding by one "the GPU admits one" slot in the scheduler, and those
		// are three different models on three different cards.
		w.OCR = NewOCR(gate(m.Channels, client, m.Vision.Model))
		// Recorded on every page this reads, so a transcription says which model
		// produced it (page_readings.model) rather than only "machine".
		w.OCR.Model = m.Vision.Model
		// What the corpus owner says about reading THIS collection, carried into
		// every prompt that reads it. Read once here rather than per page: it is
		// a property of the index, and the worker is per-index.
		w.OCR.Collection = store.IndexHint()
		attachDefaultCheapOCR(w.OCR, m.OCR)
		// Only used when a page escalates to the VLM (llm-seg); text never does.
		// Its own client, because the segmenter's job is a structured tool call
		// over TEXT and the vision model is chosen for reading PIXELS.
		seg := m.segmentRole()
		segClient := ingestClient(seg)
		// The tally is chained here too. It was only ever on the vision client,
		// so a segmentation that fought the endpoint for two minutes contributed
		// nothing to the job's retry record — and the 429s that must narrow the
		// segment model's channel were not being seen at all.
		segClient.OnRetry = w.Retries.Observe
		w.Segmenter = NewSegmenter(gate(m.Channels, segClient, seg.Model))
		w.Segmenter.Collection = store.IndexHint()
		if m.Segment.Model != "" && m.Segment.Model != m.Vision.Model {
			log.Printf("raglit: segmenter model=%s (vision model=%s)", m.Segment.Model, m.Vision.Model)
		}
	} else if ocr := TesseractOnlyOCR(m.OCR); ocr != nil {
		// No vision model (USER 2026-10-22: vision may be unavailable): scans and
		// images are read by the local engine alone. Without one, they fail as
		// unreadable, which is what w.OCR == nil already does.
		w.OCR = ocr
		cheapEngine = ocr.Cheap.Name()
		log.Printf("raglit: no vision model — scans and images are read by %s only", cheapEngine)
	}
	// What each ingested document IS, asked once per document (identity.go). Set
	// on the STORE rather than the worker because the identity call happens in
	// the commit path, which is the store's — and setting it here covers every
	// route into that path: each of the daemon's per-index workers.
	store.SetIdentifier(m.Identifier())
	// Cross-index pool (daemon only): key ingest work by (recipe, file). The
	// recipe is the models + config that shape the output — including the
	// fragmenter (§5) — so alt models OR a stride change reprocess.
	if pool != nil {
		w.Pool = pool
		fw, fs, ff := ResolveFragParams(m.FragWindow, m.FragStride, m.FragFloor, m.EmbedLimitChars)
		w.RecipeHash = HashHex([]byte(ingestRecipe(recipeInputs{
			VisionModel: m.Vision.Model, EmbedModel: m.Embed.Model,
			CheapEngine: cheapEngine,
			FragWindow:  fw, FragStride: fs, FragFloor: ff,
			IndexHint: store.IndexHint(),
		})))
	}
	return w
}

// recipeInputs is everything that shapes a document's INDEXED OUTPUT — the
// models, the fragmenter, and the corpus hint.
type recipeInputs struct {
	VisionModel string
	EmbedModel  string
	CheapEngine string
	FragWindow  int
	FragStride  int
	FragFloor   int
	// IndexHint is the corpus owner's account of how to read this collection.
	// It is IN the recipe because it changes the transcription and segmentation
	// prompts: a page read under one hint is not the same page read under
	// another. Left out, editing a hint would leave every already-pooled
	// document replaying the reading it got under the old one, and nothing
	// would say so — the pool would report a hit and the job would report done.
	IndexHint string
}

// ingestRecipe renders the pool key's inputs. Its own function so the thing
// that must not silently lose a term can be asserted on.
func ingestRecipe(in recipeInputs) string {
	return fmt.Sprintf("seg=%s|emb=%s|ocr=%s|frag=overlap,w=%d,s=%d,f=%d|fig=%d|hint=%s",
		in.VisionModel, in.EmbedModel, in.CheapEngine,
		in.FragWindow, in.FragStride, in.FragFloor,
		FigurePromptVersion(), HashHex([]byte(in.IndexHint)))
}
