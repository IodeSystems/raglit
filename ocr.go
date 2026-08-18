package raglit

import (
	"bytes"
	"image"
	"image/png"

	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	xdraw "golang.org/x/image/draw"
	"regexp"
	"strings"
	"unicode"

	"github.com/iodesystems/agentkit/llm"
	"io"
	"time"
)

// OCR transcribes page images to text. It runs a CASCADE: an optional cheap
// first-pass engine (tesseract / paddleocr), gated by a gibberish detector, and
// falls back to a vision-capable chat model (e.g. gemma-4-12b on bonsai, via
// corrallm) only when the cheap pass is missing, errors, or looks like garbage.
// With no Cheap engine it is VLM-only — the original behavior.
type OCR struct {
	Client Chatter
	// Model is the vision model's id, recorded on every page this OCR reads
	// (page_readings.model). Not used to call anything — the client already knows
	// which model it talks to — but a transcription whose author is unrecorded
	// cannot be told from one a different model produced, and a corpus outlives
	// several models.
	Model  string
	Prompt string // transcription instruction; "" → Prompt(PromptPlain)
	// Collection is what the corpus owner says about reading THIS collection
	// (Store.IndexHint) — appended to every reading prompt.
	//
	// It reaches the transcription rather than only the later asks because
	// "how to decode" is a property of the pixels: "RO" on a garage's paperwork
	// is a repair order, the second column of a carbon copy is the customer's,
	// and a model reading one page cannot infer either. It is part of the
	// READING RECIPE for the same reason — a changed hint changes what a page
	// says, and pooled work read under the old one must not be replayed under
	// the new.
	Collection string
	// Trace, when non-nil, receives one line per decision this OCR takes.
	//
	// It exists because nothing else reports what a call DID. `raglit doctor`
	// says what is configured; the page cache, the cheap tier, the assist, the
	// repetition guard and the tool list are all invisible at the call site, and
	// a reader of the output cannot tell a cheap-engine page from a VLM page, an
	// assisted prompt from a bare one, or a cut stream from a complete one.
	//
	// Written to be read by a person mid-investigation, not parsed.
	Trace io.Writer

	// Render is the automatic per-page resolution policy. Zero → the measured
	// package defaults, which is what every page got when these were constants.
	// Carried on OCR rather than passed down because OCR is already threaded to
	// every extract path, and a policy that has to be plumbed separately is one
	// that will be plumbed to some paths and not others.
	Render RenderPolicy

	// TraceJSONL, when non-nil, receives ONE JSON OBJECT PER LINE describing each
	// interaction and the transforms applied to it.
	//
	// Separate from Trace rather than a format flag on it, because the two answer
	// to different readers. Trace is prose for a person mid-investigation; this is
	// for diffing two runs, joining a failure to the exact bytes that produced it,
	// and counting what happened across a corpus — none of which survives being
	// parsed back out of a sentence.
	//
	// Every event carries img_sha (first 12 hex of the image bytes), and that is
	// what makes a line joinable: the same crop at the same scale has the same sha
	// across models and runs, so "these two models disagreed on THIS image" is a
	// join rather than a reconstruction.
	TraceJSONL io.Writer

	// TraceCtx is merged into every event this OCR emits. The region walk sets it
	// per call — doc, page, region, tokens/in² — so a line in the log says WHICH
	// crop it describes rather than only which image sha.
	//
	// A field rather than a parameter because the walk already swaps o.Prompt per
	// call and restores it after; carrying the context the same way keeps the two
	// in step, and there is no path where one is set and the other is not.
	TraceCtx map[string]any
	Cheap    PageEngine      // optional cheap first pass; nil → VLM-only
	Gate     GibberishConfig // when the cheap pass escalates to the VLM (zero → defaults)
	// DescribeFigures is the FIGURE gate (§3a): a born-digital PDF page carrying an
	// embedded image is rasterized to the VLM even when its text layer is clean, so
	// its diagrams get described. Orthogonal to the gibberish gate (which judges
	// TEXT quality); OFF by default because it flips such pages to the (costly)
	// vision path and thus to llm-seg.
	DescribeFigures bool

	// MaxTokens caps one page transcription; 0 → defaultOCRMaxTokens. See
	// chat.go for why an unbounded transcription is not a safe default.
	MaxTokens int

	// Assist changes what the cheap engine is FOR: not a substitute for the
	// vision model but a spelling reference handed to it.
	//
	// The cascade's bargain is "if the cheap read looks clean, keep it and skip
	// the model". That is a cost decision, and on a corpus of filed documents it
	// buys the wrong thing: measured here, tesseract reads a surveyor's
	// certificate as 20123164 with 86% confidence — clean-looking, wrong, and
	// the gibberish gate passes it.
	//
	// The two readers fail differently, which is what makes the assist work. The
	// vision model normalises proper names (HALVOR → HALVR) and tesseract does
	// not; tesseract mangles digits and the vision model, at sufficient
	// resolution, does not. So the model reads the page, with the cheap engine's
	// WORDS for spelling and its NUMBERS REMOVED — measured on the disputed
	// record of survey, that combination got all four checked facts right,
	// including one auditor's file number that no other configuration read
	// correctly. Handing over the numbers as well loses two of them: the model
	// copies them, whatever the instructions say.
	Assist bool
}

// NewOCR wraps a Chatter (an *llm.Client) as an OCR transcriber. The cheap tier
// is off by default; set OCR.Cheap (see BuildPageEngine) to enable the cascade.
func NewOCR(c Chatter) *OCR { return &OCR{Client: c} }

// Page transcribes one page image and returns the trimmed text.
func (o *OCR) Page(ctx context.Context, img PageImage) (string, error) {
	text, _, err := o.PageWithEngine(ctx, img)
	return text, err
}

// PageWithEngine transcribes one page and reports which engine produced it:
// the cheap engine's Name() when its result passed the gibberish gate, else
// "vision". The cascade never drops a page — a cheap-engine error or a
// gibberish verdict escalates to the VLM rather than returning the bad text.
func (o *OCR) PageWithEngine(ctx context.Context, img PageImage) (text, engine string, err error) {
	text, engine, _, err = o.PageAsSeen(ctx, img)
	return text, engine, err
}

// PageAsSeen is PageWithEngine plus the one thing a caller needs to reproduce
// what the model actually looked at: how many times the image was downscaled to
// fit the context before it was read.
//
// Separate rather than a wider PageWithEngine because only the region walk cares
// — it records the number per region so the crop can be re-rendered exactly, and
// every other caller wants the text and the engine and nothing else.
func (o *OCR) PageAsSeen(ctx context.Context, img PageImage) (text, engine string, shrinks int, err error) {
	started := time.Now()
	sha := imgSHA(img.Data)
	w, h := imgDims(img.Data)
	o.tracef("page %d: %d bytes of %s, model %q", img.Page, len(img.Data), img.Mime, o.Model)
	o.event("page.start", map[string]any{
		"page": img.Page, "bytes": len(img.Data), "mime": img.Mime, "img_sha": sha,
		"px_w": w, "px_h": h, "tokens_est": tokensFor(w, h),
	})
	assist := ""
	if o.Cheap == nil {
		o.tracef("cheap tier: none configured — every page goes to the VLM (ocr.cheap_engine)")
	}
	if o.Cheap != nil {
		if po, cerr := o.Cheap.OCRPage(ctx, img); cerr == nil {
			o.tracef("cheap tier: %s read %d chars, mean confidence %.2f, %d boxes, median glyph %dpx",
				o.Cheap.Name(), len(po.Text), po.MeanConfidence, po.BoxCount, po.MedianGlyphPx)
			o.event("cheap.read", map[string]any{
				"img_sha": sha, "engine": o.Cheap.Name(), "chars": len(po.Text),
				"mean_confidence": po.MeanConfidence, "boxes": po.BoxCount,
				"median_glyph_px": po.MedianGlyphPx,
			})
			if o.Assist {
				assist = spellingAssist(po.Text)
				o.tracef("assist: ON — %d chars of digit-masked words appended to the prompt", len(assist))
			} else if gib, _ := o.Gate.IsGibberish(po); !gib {
				o.tracef("cascade: cheap result accepted, VLM not called")
				o.event("cascade.accept", map[string]any{
					"img_sha": sha, "engine": o.Cheap.Name(), "chars": len(po.Text),
					"duration_ms": time.Since(started).Milliseconds(),
				})
				// Cascade mode: a non-gibberish result (including a legitimately
				// empty page) is trusted — do not pay the VLM for clean or blank
				// pages. The cheap engine reads the image as given; nothing was
				// shrunk.
				return strings.TrimSpace(po.Text), o.Cheap.Name(), 0, nil
			}
		} else {
			o.tracef("cheap tier: %s errored (%v) — falling through to the VLM", o.Cheap.Name(), cerr)
		}
		// cheap error or gibberish → fall through to the VLM.
	}
	t, n, verr := o.visionPage(ctx, img, assist)
	// A salvaged page is a READ page, not a failed one. The sentinel carries the
	// one thing the caller must not lose — that the tail was never seen — into
	// the engine, where every consumer of provenance already looks.
	if errors.Is(verr, errSalvagedTail) {
		o.tracef("vision: %d chars SALVAGED at a structural loop, %s total — tail of the page NOT read",
			len(t), time.Since(started).Round(time.Millisecond))
		o.event("vision.salvaged", map[string]any{
			"img_sha": sha, "chars": len(t), "downscales": n,
			"duration_ms": time.Since(started).Milliseconds(),
		})
		return t, enginePartialVision, n, nil
	}
	if verr != nil {
		o.tracef("vision: FAILED after %s: %v", time.Since(started).Round(time.Millisecond), verr)
		o.event("vision.error", map[string]any{
			"img_sha": sha, "err": verr.Error(),
			"duration_ms": time.Since(started).Milliseconds(),
		})
		return "", "", 0, verr
	}
	o.tracef("vision: %d chars, %d context downscale(s), %s total",
		len(t), n, time.Since(started).Round(time.Millisecond))
	// downscales is the TRANSFORM record for this path: how many times the image
	// was halved to fit the context before the model saw it. A read that
	// disagrees with another run usually differs here first.
	o.event("vision.read", map[string]any{
		"img_sha": sha, "chars": len(t), "downscales": n, "assist": assist != "",
		"duration_ms": time.Since(started).Milliseconds(),
	})
	return t, "vision", n, nil
}

// tracef writes one trace line when tracing is on, and is a no-op otherwise.
func (o *OCR) tracef(format string, a ...any) {
	if o.Trace == nil {
		return
	}
	fmt.Fprintf(o.Trace, "  ocr | "+format+"\n", a...)
}

// event writes one JSONL record. A no-op when TraceJSONL is nil, and it never
// returns an error: a trace that can fail an ingest is worse than no trace.
func (o *OCR) event(kind string, kv map[string]any) {
	if o.TraceJSONL == nil {
		return
	}
	if kv == nil {
		kv = map[string]any{}
	}
	for k, v := range o.TraceCtx {
		if _, taken := kv[k]; !taken {
			kv[k] = v
		}
	}
	kv["kind"] = kind
	kv["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	if _, ok := kv["model"]; !ok && o.Model != "" {
		kv["model"] = o.Model
	}
	b, err := json.Marshal(kv)
	if err != nil {
		return
	}
	o.TraceJSONL.Write(append(b, '\n'))
}

// imgDims reads pixel dimensions from the image HEADER only — no full decode,
// so it costs nothing worth measuring on a 15 MP page.
func imgDims(b []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// tokensFor is the image cost a Qwen-VL-family encoder charges: one token per
// 32x32 px block (patch 16, spatial merge 2). Recorded because it is the number
// that explains most read failures — a region holding 241 of a page's 14609
// tokens is not going to be read, and no prompt fixes that. Measured against
// llama.cpp within 1% at both 200 and 400 DPI.
func tokensFor(w, h int) int {
	if w <= 0 || h <= 0 {
		return 0
	}
	return (w * h) / (32 * 32)
}

// imgSHA identifies an image by its bytes. Twelve hex characters is enough to
// join a corpus run without making a line unreadable.
func imgSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

// visionPage is the VLM transcription: agentkit's multimodal llm.Message — a
// text instruction + the page as an inline image part.
//
// Reports the number of context downscales it had to apply, which is the only
// part of the image the model saw that a caller cannot reconstruct from what it
// passed in.
func (o *OCR) visionPage(ctx context.Context, img PageImage, assist string) (string, int, error) {
	if o.Client == nil {
		return "", 0, fmt.Errorf("raglit: ocr page %d needs the vision model but none is configured", img.Page)
	}
	prompt := o.Prompt
	if prompt == "" {
		prompt = Prompt(PromptPlain)
	}
	// While the VLM is transcribing, have it also DESCRIBE figures inline (§3a):
	// the description lands in the page text, flows into fragments, and indexes as
	// searchable text — no new infrastructure. A described diagram beats an
	// invisible one; this is why a page reaches the VLM at all.
	prompt += figureInstruction + assist
	// The tool list is the thing nobody could see. It is nil, always: this path
	// asks for one verbatim re-emission and offers the model nothing to call.
	o.tracef("request: %d chars of prompt, %d image bytes, %d tools offered",
		len(prompt), len(img.Data), 0)
	msg := llm.Message{Role: "user", Parts: []llm.ContentPart{
		llm.TextPart(prompt),
		llm.ImageData(img.Mime, img.Data),
	}}
	maxTok := o.MaxTokens
	if maxTok <= 0 {
		maxTok = defaultOCRMaxTokens
	}
	opts := &llm.ChatOpts{MaxTokens: maxTok}
	text, rep, err := collectStream(ctx, o.Client, []llm.Message{msg}, opts)
	// A page image can be too large for the model's context — observed at 180273
	// tokens against a 180224 limit, over by 49. Nothing about that is a document
	// problem and retrying it unchanged fails identically forever, so the page is
	// re-rendered smaller and re-sent. Downscaling loses detail, which is why it
	// is a fallback and not the default resolution.
	shrinks := 0
	for ; shrinks < maxContextShrinks && err != nil && isContextOverflow(err); shrinks++ {
		smaller, serr := downscalePNG(img.Data, contextShrinkFactor)
		if serr != nil {
			return "", shrinks, fmt.Errorf("raglit: ocr page %d: too large for the model context and cannot be downscaled: %w", img.Page, serr)
		}
		img.Data = smaller
		msg.Parts[1] = llm.ImageData(img.Mime, img.Data)
		text, rep, err = collectStream(ctx, o.Client, []llm.Message{msg}, opts)
	}
	if err != nil {
		return "", shrinks, fmt.Errorf("raglit: ocr page %d: %w", img.Page, err)
	}
	if rep != nil {
		// The page derailed. Retry ONCE with sampling that can actually escape
		// the loop — the prompt carries no transcribed text on purpose, because
		// re-anchoring a transcription on a partial transcription is how a VLM
		// starts inventing survey text that reads exactly like the real thing.
		cut := unloopedLen(text, rep)
		first, rep0 := text, rep
		// A STRUCTURAL loop is a different failure and needs a different retry.
		//
		// Sampling is the right lever when a model is stuck re-emitting REAL text:
		// it has lost its place in the page and randomness lets it move on. It is
		// the wrong lever for a mostly-empty grid, where the model has not lost its
		// place at all — it read every row that has content and is now dutifully
		// emitting the hundred blank cells that follow, one `<td></td>` at a time.
		// No temperature makes emptiness end sooner, which is why the loop-break
		// retry failed identically on both PL99-0479 sheets.
		//
		// So the retry TELLS IT TO SKIP THE BLANKS. That is an instruction, not
		// content: it adds nothing the model could mistake for text it already
		// read, so the re-anchoring failure above is not in play.
		//
		// Measured — the two Record of Ownership sheets, a 14-row grid holding one
		// filled row (Cartwright → McKinnon, 1972). Both looped on
		// `<td></td><td></td></tr><tr>`, both were dropped entirely, and the entry
		// was invisible to search until the grid was cropped away by hand.
		sparse := structuralRepetition(rep)
		if sparse {
			o.tracef("loop was STRUCTURAL (%q) — retrying with the sparse-table instruction", rep.Sample)
			msg.Parts[0] = llm.TextPart(prompt + sparseTableAssist)
		}
		text, rep, err = collectStream(ctx, o.Client, []llm.Message{msg}, loopBreakSampling(opts))
		if err != nil {
			return "", shrinks, fmt.Errorf("raglit: ocr page %d (loop-break retry): %w", img.Page, err)
		}
		// A CUT transcription is not a short transcription — it is the page's text
		// with an unknown amount missing. Indexing it would put a silently
		// incomplete page into the index and mark the document done, which is worse
		// than failing: nothing would ever revisit it. Fail loudly so the job
		// retries or a human looks.
		if rep != nil {
			// Instruction did not work either. Measured against chandra on both
			// PL99-0479 sheets: told in as many words not to emit table markup, it
			// emitted the identical 54-character block ten times over. A model that
			// will not stop describing emptiness cannot be talked out of it.
			//
			// So SALVAGE, but only for a structural loop, and the distinction is
			// the whole licence to do it. The refusal above rests on a premise —
			// "a cut transcription is the page's text with an UNKNOWN amount
			// missing" — and for empty markup that premise is false: what follows
			// the cut is blank cells, and the prefix holds every row that has
			// content. For a content loop the premise holds and the page is still
			// refused.
			//
			// It is not free, and the engine says so rather than the text
			// pretending otherwise. Anything BELOW the loop is lost — on these
			// sheets, the "Adjoining Property" heading under the blank remainder —
			// so the page is recorded as enginePartialVision: indexed, findable,
			// and marked as a page whose tail was never read.
			// A CONTENT loop on a whole page is the measured signature of a page
			// read SIDEWAYS, and the region walk already knows it. From its own
			// sweep of the survey (plan/hierarchical-regions.md, 2026-08-03):
			//
			//	whole sheet sideways  9,316 chars  89% of lines duplicated  2 bearings wrong
			//	whole sheet upright   2,187 chars   2% duplicated           correct
			//
			// "The wrong orientation does not lose text. It makes the model run
			// on." A plan sheet filed on its side hands the model a column of
			// rotated glyphs, it re-reads the same block, and the guard cuts it —
			// which is exactly how page 9 of the lisser exhibit failed, on a
			// Record of Survey whose notes name the lot certification at issue.
			//
			// So TURN THE PAGE. Only right angles, and the rotation is accepted
			// only when the reading is measurably less degenerate — the one
			// discriminator that sweep found with no overlap between the good and
			// bad renders. A rotation that does not clear the repetition is
			// discarded and the page still fails.
			if !structuralRepetition(rep) {
				if turned, deg, ok := o.readTurned(ctx, img, prompt, opts); ok {
					o.tracef("content loop cleared by rotating the page %d° — reading it upright", deg)
					o.event("vision.rotated", map[string]any{"img_sha": imgSHA(img.Data), "degrees": deg, "chars": len(turned)})
					return strings.TrimSpace(turned), shrinks, nil
				}
			}
			if structuralRepetition(rep) {
				best := unloopedPrefix(text, rep)
				if a, b := FlattenForIndex(best), FlattenForIndex(unloopedPrefix(first, rep0)); len(strings.TrimSpace(b)) > len(strings.TrimSpace(a)) {
					best = unloopedPrefix(first, rep0)
				}
				if strings.TrimSpace(FlattenForIndex(best)) != "" {
					o.tracef("both passes looped on empty markup — salvaging %d chars of content, tail NOT read", len(best))
					return strings.TrimSpace(best), shrinks, errSalvagedTail
				}
			}
			return "", shrinks, fmt.Errorf(
				"raglit: ocr page %d: the model %s, on the loop-break retry too — page NOT indexed",
				img.Page, rep)
		}
		// And a retry that comes back CLEAN is not automatically correct.
		//
		// This is the failure that made the check necessary. A recorded survey
		// looped on the first pass; the loop-break retry returned tidy,
		// well-formed prose — and it had silently dropped the entire legal
		// description, replaced it with a one-line figure caption, and invented
		// plausible auditor file numbers (A#200308270057 for AF#9308270057). It
		// read as a complete transcription of a page whose most important text
		// was simply absent, and the job reported success.
		//
		// The model that just derailed is the same model producing the retry, so a
		// retry that comes back with LESS than the first pass had already
		// transcribed is evidence it gave up on the page rather than recovered it.
		//
		// Measured against the first pass with the REPEATED BLOCK REMOVED, which
		// matters: a loop pads the cut output with the same span over and over, so
		// comparing raw lengths would flag every genuine recovery as a failure —
		// the successful retry is routinely shorter than the garbage it replaces.
		// On the sparse path the bar is CONTENT, not characters.
		//
		// The retry was told to omit the blank rows, so it is expected to come back
		// shorter by exactly the markup that caused the loop — comparing raw
		// lengths would reject every recovery this instruction exists to produce.
		// Comparing flattened text asks the question that actually matters, since
		// empty cells contribute no characters to it: did the retry come back with
		// less READING than the first pass had already got off the page?
		got, bar := len(strings.TrimSpace(text)), cut
		if sparse {
			got = len(strings.TrimSpace(FlattenForIndex(text)))
			bar = len(strings.TrimSpace(FlattenForIndex(unloopedPrefix(first, rep0))))
		}
		if bar > 0 && got < bar {
			return "", shrinks, fmt.Errorf(
				"raglit: ocr page %d: the loop-break retry returned %d chars where the cut pass had already transcribed %d "+
					"before it started repeating — a shorter retry means the page was dropped, not recovered; page NOT indexed",
				img.Page, got, bar)
		}
	}
	return strings.TrimSpace(text), shrinks, nil
}

// loopRotations are the right angles tried when a whole-page read loops on
// CONTENT, in the order they pay off: a sheet filed on its side is overwhelmingly
// a quarter turn away, either direction, and 180° is the rare upside-down scan.
var loopRotations = []int{270, 90, 180}

// readTurned re-reads a page at each right angle and returns the first reading
// that is not a repetition loop.
//
// The bar is degenerateRatio, borrowed from the region walk because that is
// where it was measured: correct renders of the survey duplicated 2-3% of their
// lines and the mis-rotated ones 89-94%, with nothing observed between. A
// rotation that comes back still repeating itself is not an improvement and is
// discarded — this turns a page, it does not lower the standard.
//
// The prompt is the ORIGINAL one. Nothing about the failed pass is fed back, for
// the reason the loop-break retry gives: re-anchoring a transcription on a
// partial transcription is how a VLM starts inventing text that reads like the
// real thing.
func (o *OCR) readTurned(ctx context.Context, img PageImage, prompt string, opts *llm.ChatOpts) (string, int, bool) {
	src, _, err := image.Decode(bytes.NewReader(img.Data))
	if err != nil {
		return "", 0, false
	}
	for _, deg := range loopRotations {
		var buf bytes.Buffer
		if err := png.Encode(&buf, rotateImage(src, deg)); err != nil {
			continue
		}
		msg := llm.Message{Role: "user", Parts: []llm.ContentPart{
			llm.TextPart(prompt), llm.ImageData("image/png", buf.Bytes()),
		}}
		text, rep, err := collectStream(ctx, o.Client, []llm.Message{msg}, opts)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		if rep != nil || degenerateRatio(text) >= degenerateLineRatio {
			o.tracef("rotation %d° still repeats itself — discarded", deg)
			continue
		}
		return text, deg, true
	}
	return "", 0, false
}

// errSalvagedTail reports a page read up to a structural loop and no further:
// every row with content is present, the blank remainder and anything below it
// is not. A sentinel rather than a bool so it cannot be ignored by a caller that
// only checks err — the page IS usable, and the caller must say so on the row.
var errSalvagedTail = errors.New("raglit: page salvaged at a structural loop — content read, tail not")

// enginePartialVision marks a page the vision model read only as far as a
// structural loop. A member of the "vision" family (see isVisionEngine) because
// a model did read it; distinct because part of the page was never seen.
const enginePartialVision = "vision-partial"

// isVisionEngine reports whether a page was read by the VLM, salvaged or whole.
// The family exists so a new engine value cannot silently fall out of the
// checks that decide model attribution, described-fraction scoring and what the
// review panel shows.
func isVisionEngine(e string) bool { return e == "vision" || e == enginePartialVision }

// sparseTableAssist is the retry instruction for a page whose table is mostly
// empty. An INSTRUCTION and nothing else — it carries no transcribed text, so it
// cannot re-anchor the model on its own partial output.
//
// It asks for the blank remainder to be SUMMARISED rather than dropped, because
// how many rows a form leaves blank is itself a fact about the record: a Record
// of Ownership with one entry in fourteen rows says one transfer was recorded,
// and a transcription that silently omits the empty rows loses that.
const sparseTableAssist = "\n\nIMPORTANT — this page contains a table whose rows are mostly EMPTY, " +
	"and a previous attempt to read it got stuck emitting blank cells forever.\n" +
	"For THIS page only, do NOT use table markup of any kind. No <table>, no <tr>, no <td>, no | pipes.\n" +
	"Instead: write the column headings as one plain line, then write ONE PLAIN LINE for each row that " +
	"CONTAINS DATA, with its values separated by a comma. Skip every empty row entirely. " +
	"Finish with one line naming how many rows were left blank, e.g. (11 further rows are blank)."

// structuralRepetition reports that what the model looped on carries NO CONTENT
// — it is empty markup, not text.
//
// This is the line between two failures that look identical in a log and are
// opposite in what they mean:
//
//	content    a model re-emitting an 85-character span of a legal description
//	           has LOST ITS PLACE. Its output is the page with an unknown amount
//	           missing, and indexing it would be indexing a lie.
//	structural a model emitting `<td></td><td></td></tr><tr>` has not lost its
//	           place. It read every row that has content and is working through
//	           the blank remainder of the grid.
//
// Any letter or digit OUTSIDE the markup makes it content, and the strict
// reading (refuse) still applies.
func structuralRepetition(rep *llm.RepetitionInfo) bool {
	if rep == nil || strings.TrimSpace(rep.Sample) == "" {
		return false
	}
	for _, r := range outsideMarkup(rep.Sample) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// outsideMarkup returns the characters of s that are not inside a tag.
//
// Hand-rolled rather than FlattenForIndex because the input is a SAMPLE — one
// period of a repeating block, sliced out of a stream wherever the loop happened
// to begin. It routinely starts and ends mid-tag:
//
//	><td></td><td></td><td></td></tr><tr><td></td><td></td
//
// A tag-stripping regexp leaves the dangling `<td` and the orphan `>`, and the
// `td` reads as two letters of content — which flips the verdict to "the model
// lost its place" and drops a page that was only ever stuttering on blanks.
// Scanning by depth has no such edge: an unterminated tag simply never closes.
func outsideMarkup(s string) string {
	// The sample is sliced at an arbitrary offset, so it can begin PART WAY
	// THROUGH a tag — the real cut on the Record of Ownership sheets began
	// `td><td></td>…`. Those two leading letters sit outside any `<`, so a plain
	// depth scan reports them as content and the page is dropped as "lost text".
	// A `>` before the first `<` can only be the tail of a tag that started
	// before the sample did, so everything up to it goes.
	if i := strings.IndexByte(s, '>'); i >= 0 {
		if j := strings.IndexByte(s, '<'); j < 0 || i < j {
			s = s[i+1:]
		}
	}
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// unloopedPrefix is a cut generation with the redundant copies of the repeated
// block trimmed off the end, leaving one intact copy — the text the model
// actually got off the page before it began stuttering.
func unloopedPrefix(text string, rep *llm.RepetitionInfo) string {
	if rep == nil || rep.Trailing <= 0 || rep.Trailing >= len(text) {
		return text
	}
	out := text[:len(text)-rep.Trailing]
	// The cut is a byte offset and lands wherever the period says, which is
	// routinely mid-tag — it left a bare `</td` at the end of a salvaged Record
	// of Ownership, indexed as if it were words. Drop a trailing tag that never
	// closes.
	if i := strings.LastIndexByte(out, '<'); i > strings.LastIndexByte(out, '>') {
		out = out[:i]
	}
	return out
}

// unloopedLen is how much of a cut generation was real transcription: its length
// with the repeated span discounted.
//
// A loop inflates the output by the same block N times, so raw length overstates
// what the model actually read off the page — and using it as the bar to clear
// would reject the recoveries this retry exists to produce. Returns 0 when the
// repetition accounts for everything, which correctly imposes no bar at all.
func unloopedLen(text string, rep *llm.RepetitionInfo) int {
	n := len(strings.TrimSpace(text))
	if rep == nil || rep.Period <= 0 || rep.Reps <= 1 {
		return n
	}
	if loop := rep.Period * (rep.Reps - 1); loop < n {
		return n - loop
	}
	return 0
}

// maxContextShrinks bounds the downscale loop. Two halvings take a 200-DPI page
// to about 100 DPI, below which a survey's lettering stops being legible and a
// "successful" transcription would be worse than a failure.
const maxContextShrinks = 2

// contextShrinkFactor is how much smaller each retry renders the page. 0.75 on
// each axis is ~44% of the pixels, which clears an overflow of a few dozen
// tokens with room to spare without throwing away detail unnecessarily.
const contextShrinkFactor = 0.75

// isContextOverflow reports whether the model refused because the request did
// not fit. Matched on the server's wording rather than a status code: a 400 has
// many causes and only this one is fixed by sending a smaller image.
func isContextOverflow(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "exceed_context_size") ||
		strings.Contains(s, "exceeds the available context")
}

// downscalePNG re-renders a PNG at `factor` of its dimensions.
func downscalePNG(data []byte, factor float64) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := int(float64(b.Dx())*factor), int(float64(b.Dy())*factor)
	if w < 1 || h < 1 {
		return nil, fmt.Errorf("page would scale to %dx%d", w, h)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// CatmullRom over a cheaper kernel on purpose: this runs on scanned text, and
	// the whole point of the smaller image is that it still has to be readable.
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// spellingAssist turns a cheap engine's page text into a spelling reference for
// the vision model — WITH EVERY NUMBER REMOVED.
//
// The removal is the point, and it was measured. Handed tesseract's full text,
// the model adopts its digits: a certificate number it had read correctly on its
// own came back as tesseract's misreading, and an instruction to prefer the
// image for numbers did not change that. Anchoring beats instruction. With the
// digits gone there is nothing wrong to copy, and what remains — the spelling of
// words and names — is the half tesseract is actually better at.
func spellingAssist(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// The block is a marker, not a mechanism: deleting the digits outright reads
	// identically well (measured — see plan/ocr-fixtures.md). It is kept because
	// an elision that shows itself is easier to reason about than a silent gap.
	masked := digitRun.ReplaceAllString(text, "\u2588")
	if strings.TrimSpace(strings.ReplaceAll(masked, "\u2588", "")) == "" {
		return "" // nothing but numbers; no spellings to offer
	}
	return "\n\nA character-level OCR engine read this same page and produced the text " +
		"below. EVERY NUMBER HAS BEEN REMOVED FROM IT deliberately: it is unreliable on " +
		"digits, so read every number in this document from the image yourself.\n\n" +
		"Use it ONLY for the spelling of words and proper names — where it spells a name " +
		"differently than you would, it is probably right and you are probably " +
		"normalising it.\n\n--- words seen (numbers removed) ---\n" + masked +
		"\n--- end ---"
}

// digitRun matches a number and whatever punctuation runs through it, so a
// recording number, a date and a dollar amount all leave nothing copyable.
var digitRun = regexp.MustCompile(`[0-9][0-9.,/:$-]*`)
