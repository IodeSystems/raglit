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
	// Embed makes vectors. NewWorker probes it for its input limit; the
	// embedder itself is set on the Store or Registry, built by Models.Embedder.
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
	// Channels is the per-model admission registry (modelchan.go). Required:
	// NewWorker and Identifier panic on nil. Model capacity is a property of the
	// SERVER, so build one registry for the process with NewChannels, set its
	// Max (the per-model channel ceiling, config model_channel_max), and pass it
	// to every index's worker. A registry per worker would split one server's
	// capacity between controllers that each learn it alone.
	Channels *Channels
	// OnUsage, when set, is called once per completed model response with the
	// role ("vision", "segment", "identity", "embed"), that role's model id and
	// the tokens the endpoint reported, so a host can total or cap spend per
	// role. It runs on the calling goroutine of the request, possibly from many
	// at once: it must be safe for concurrent use and must not block. An
	// OnUsage already on a client is chained, not replaced. nil → no change.
	//
	// Coverage: the four roles above only. STT, image embed and the OCR engine
	// use plain HTTP clients and do not report usage. "embed" reports only
	// through an Embedder from Models.Embedder (or a client wrapped with
	// UsageVector): set that one on the Store or Registry. The embed limit
	// probes NewWorker makes do not report.
	//
	// "embed" is an ESTIMATE, not a reported count: agentkit's Client.Embed
	// discards the response's usage, so UsageVector reports ceil(bytes/4) of
	// input. Code often runs below four bytes per token, so the estimate can
	// be low.
	OnUsage func(role, model string, u llm.Usage)
	// Code cuts source files at their declarations and Markdown at its
	// headings (KindCode, codesegment.go). nil → both are read into windows,
	// as before. Its Runtime() is in the pool recipe.
	Code CodeSegmenter
}

// usageHook points c.OnUsage at m.OnUsage for one role, after any hook the
// client already has. No-op when m.OnUsage is nil.
func (m Models) usageHook(c *llm.Client, role, model string) {
	if m.OnUsage == nil {
		return
	}
	prev, on := c.OnUsage, m.OnUsage
	c.OnUsage = func(u llm.Usage) {
		if prev != nil {
			prev(u)
		}
		on(role, model, u)
	}
}

// UsageVector wraps an embedding client so each successful call reports to
// m.OnUsage as role "embed". Returns inner unchanged when OnUsage is nil.
//
// The token count is an estimate (see Models.OnUsage): ceil(bytes/4) over the
// inputs sent. The model id is the one the call names, else m.Embed.Model.
func (m Models) UsageVector(inner VectorClient) VectorClient {
	if inner == nil || m.OnUsage == nil {
		return inner
	}
	return &usageVector{inner: inner, on: m.OnUsage, model: m.Embed.Model}
}

type usageVector struct {
	inner VectorClient
	on    func(role, model string, u llm.Usage)
	model string
}

func (v *usageVector) Embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	out, err := v.inner.Embed(ctx, model, input)
	if err != nil {
		return out, err
	}
	bytes := 0
	for _, s := range input {
		bytes += len(s)
	}
	n := (bytes + 3) / 4
	if model == "" {
		model = v.model
	}
	v.on("embed", model, llm.Usage{PromptTokens: n, TotalTokens: n})
	return out, nil
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

// channels is the shared registry. No default: a fresh one here would be the
// split capacity the Channels field warns about, and it would also drop the
// caller's Max without a word.
func (m Models) channels() *Channels {
	if m.Channels == nil {
		panic("raglit: Models.Channels is nil; pass the process's shared registry (NewChannels)")
	}
	return m.Channels
}

// Embedder builds the embedder for ingest and search: the batch retry policy,
// the embed model's admission channel with its 429 hook, and the OnUsage
// report as role "embed". nil when no Embed model is set. m.Channels must be
// set. Set the result on the Store (SetEmbedder) or the Registry.
func (m Models) Embedder() *Embedder {
	if m.Embed.Model == "" {
		return nil
	}
	chans := m.channels()
	c := ingestClient(m.Embed)
	c.OnRetry = chans.Observe(m.Embed.Model, c.OnRetry)
	return NewEmbedder(m.UsageVector(chans.GateVector(c, m.Embed.Model)), m.Embed.Model)
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
// identity is off (no Identity model). m.Channels must be set.
//
// Same batch retry policy as the rest of ingest: this call happens at the end of
// a document that may have cost thirty OCR requests, and losing the caption to a
// transient 502 wastes the one moment the whole transcript is in hand.
func (m Models) Identifier() *Identifier {
	chans := m.channels()
	r := m.Identity
	if r.Model == "" {
		return nil
	}
	if r.BaseURL == "" {
		seg := m.segmentRole()
		r.BaseURL, r.APIKey = seg.BaseURL, seg.APIKey
	}
	c := ingestClient(r)
	m.usageHook(c, "identity", r.Model)
	return NewIdentifier(gate(chans, c, r.Model), r.Model)
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
// cross-index pool (daemon only). m.Channels must be set (Models.Channels).
func NewWorker(store *Store, m Models, pool *Pool) *Worker {
	m.Channels = m.channels()
	w := &Worker{Store: store, Code: m.Code}
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
		m.usageHook(client, "vision", m.Vision.Model)
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
		m.usageHook(segClient, "segment", seg.Model)
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
		segmenter := ""
		if m.Code != nil {
			segmenter = m.Code.Runtime()
		}
		w.RecipeHash = HashHex([]byte(ingestRecipe(recipeInputs{
			VisionModel: m.Vision.Model, EmbedModel: m.Embed.Model,
			CheapEngine: cheapEngine,
			FragWindow:  fw, FragStride: fs, FragFloor: ff,
			IndexHint: store.IndexHint(),
			Segmenter: segmenter,
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
	// Segmenter is Models.Code's Runtime(), empty with no segmenter. It
	// decides how code and Markdown are cut. Empty adds no term, so a worker
	// without one keeps the recipe it had before segmenters existed.
	Segmenter string
}

// ingestRecipe renders the pool key's inputs. Its own function so the thing
// that must not silently lose a term can be asserted on.
func ingestRecipe(in recipeInputs) string {
	r := fmt.Sprintf("seg=%s|emb=%s|ocr=%s|frag=overlap,w=%d,s=%d,f=%d|fig=%d|hint=%s",
		in.VisionModel, in.EmbedModel, in.CheapEngine,
		in.FragWindow, in.FragStride, in.FragFloor,
		FigurePromptVersion(), HashHex([]byte(in.IndexHint)))
	if in.Segmenter != "" {
		r += "|segmenter=" + in.Segmenter
	}
	return r
}
