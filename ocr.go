package raglit

import (
	"bytes"
	"image"
	"image/png"

	"context"
	"fmt"
	xdraw "golang.org/x/image/draw"
	"regexp"
	"strings"

	"github.com/iodesystems/agentkit/llm"
)

// defaultOCRPrompt asks for a faithful transcription and nothing else — no
// summary, no markdown fences — so the indexed text is the page's words, not
// the model's commentary.
const defaultOCRPrompt = "Transcribe all text visible in this document page image exactly as it appears, " +
	"preserving reading order and line breaks. Output ONLY the transcription: no commentary, no headings you add yourself, no markdown code fences."

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
	Prompt string          // transcription instruction; "" → defaultOCRPrompt
	Cheap  PageEngine      // optional cheap first pass; nil → VLM-only
	Gate   GibberishConfig // when the cheap pass escalates to the VLM (zero → defaults)
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
	assist := ""
	if o.Cheap != nil {
		if po, cerr := o.Cheap.OCRPage(ctx, img); cerr == nil {
			if o.Assist {
				assist = spellingAssist(po.Text)
			} else if gib, _ := o.Gate.IsGibberish(po); !gib {
				// Cascade mode: a non-gibberish result (including a legitimately
				// empty page) is trusted — do not pay the VLM for clean or blank
				// pages. The cheap engine reads the image as given; nothing was
				// shrunk.
				return strings.TrimSpace(po.Text), o.Cheap.Name(), 0, nil
			}
		}
		// cheap error or gibberish → fall through to the VLM.
	}
	t, n, verr := o.visionPage(ctx, img, assist)
	if verr != nil {
		return "", "", 0, verr
	}
	return t, "vision", n, nil
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
		prompt = defaultOCRPrompt
	}
	// While the VLM is transcribing, have it also DESCRIBE figures inline (§3a):
	// the description lands in the page text, flows into fragments, and indexes as
	// searchable text — no new infrastructure. A described diagram beats an
	// invisible one; this is why a page reaches the VLM at all.
	prompt += figureInstruction + assist
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
		// the loop — the prompt is unchanged on purpose, because re-anchoring a
		// transcription on a partial transcription is how a VLM starts inventing
		// survey text that reads exactly like the real thing.
		cut := unloopedLen(text, rep)
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
		if got := len(strings.TrimSpace(text)); cut > 0 && got < cut {
			return "", shrinks, fmt.Errorf(
				"raglit: ocr page %d: the loop-break retry returned %d chars where the cut pass had already transcribed %d "+
					"before it started repeating — a shorter retry means the page was dropped, not recovered; page NOT indexed",
				img.Page, got, cut)
		}
	}
	return strings.TrimSpace(text), shrinks, nil
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
