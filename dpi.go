package raglit

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// Render resolution is bounded three ways, and every bound is measurable. The
// old rule used only the first, which is why it could ask for a 158 megapixel
// render of a sheet whose every pixel above 16.8 million was going to be thrown
// away before the model saw it.
//
//	NEED     what the text requires: base * target glyph height / measured height
//	CAP      what the encoder will accept: a token is a 32x32 px block and the
//	         server caps an image at maxTokens, so a bigger sheet MUST be shown
//	         at lower resolution — dpi_cap = sqrt(1024 * maxTokens / area)
//	NATIVE   what the source actually holds. A 200 DPI scan rendered at 400 is
//	         interpolation: more pixels, more tokens, not one more glyph.
//
// The identity worth remembering, because it makes the numbers legible:
//
//	tokens/sq in = dpi^2 / 1024        and        200 DPI = 39.06 t/in^2
//
// 39 is regions.go's letterTokensPerSqIn, measured independently. "Readable
// baseline" and "200 DPI" are therefore the same statement, and a sheet shown
// below 39 t/in^2 is one the reader cannot be expected to transcribe.
const pxPerToken = 32 * 32

// DefaultMaxImageTokens matches --image-max-tokens as configured on this fleet.
// It belongs to the SERVER, not the document, so it is a policy input rather
// than a constant folded into the arithmetic.
const DefaultMaxImageTokens = 16384

// TokensPerSqInAt is the encoder's cost of one square inch rendered at dpi.
func TokensPerSqInAt(dpi int) float64 {
	return float64(dpi) * float64(dpi) / pxPerToken
}

// capHeadroom is the share of the token budget the IMAGE may claim.
//
// The prompt shares that budget. Sizing an image to exactly maxTokens puts the
// request over as soon as any instruction is added, and the server then halves
// the image — losing far more than the headroom would have cost. Measured on the
// bench's E-size survey: at the un-reserved cap of 130 DPI the sheet came to
// 16,351 tokens against a 16,384 limit, went over with the prompt, was halved,
// and the root read at 4.0 tokens/in² — the same as rendering it at 200 and
// wasting the pixels.
const capHeadroom = 0.85

// DPICapForArea is the highest resolution at which a sheet of areaSqIn can be
// shown WITHOUT the server downscaling it — the bound that falls as the canvas
// grows, and the reason a 27x36.7in sheet cannot be read whole at any setting.
func DPICapForArea(areaSqIn float64, maxTokens int) int {
	if areaSqIn <= 0 || maxTokens <= 0 {
		return 0
	}
	return int(math.Sqrt(pxPerToken * capHeadroom * float64(maxTokens) / areaSqIn))
}

// TilesNeeded is how many equal pieces a sheet must be cut into for every pixel
// to reach the model at dpi. Ceil, because a fractional tile still costs a call
// and a sheet 1.1x over the cap still loses a tenth of itself.
func TilesNeeded(areaSqIn float64, dpi, maxTokens int) int {
	if areaSqIn <= 0 || dpi <= 0 || maxTokens <= 0 {
		return 1
	}
	n := int(math.Ceil(areaSqIn * TokensPerSqInAt(dpi) / float64(maxTokens)))
	if n < 1 {
		return 1
	}
	return n
}

// DPIDecision is the chosen resolution and WHICH bound chose it. The reason is
// carried because the three failures look identical in a rendered page and are
// fixed differently: "need" means the text is fine, "cap" means tile it, and
// "native" means no rendering setting will ever help and the scan is the limit.
type DPIDecision struct {
	DPI       int
	Reason    string  // "need" | "cap" | "native" | "base"
	CapDPI    int     // what the token budget allowed
	NativeDPI int     // what the source holds (0 = unknown)
	NeedDPI   int     // what the glyph measurement asked for
	WantDPI   int     // the resolution asked for, BEFORE the cap
	Tiles     int     // canvases needed to deliver WantDPI
	Density   float64 // tokens/sq in delivered at DPI
}

// ChooseDPI applies all three bounds.
//
// nativeDPI of 0 means "unknown", and an unknown source resolution must NOT
// silently become a bound: a born-digital page has no native raster at all, and
// treating that as zero would refuse to render it.
func ChooseDPI(needDPI int, areaSqIn float64, nativeDPI, maxTokens int, p RenderPolicy) DPIDecision {
	rp := p.resolved()
	if maxTokens <= 0 {
		maxTokens = DefaultMaxImageTokens
	}
	d := DPIDecision{NeedDPI: needDPI, NativeDPI: nativeDPI, Reason: "base", DPI: rp.BaseDPI}
	switch {
	case needDPI > 0:
		d.DPI, d.Reason = needDPI, "need"
	case nativeDPI > 0:
		// NO GLYPH MEASUREMENT. The old fallback was BaseDPI, which made the
		// whole policy inert wherever no cheap engine is configured — measured
		// on this corpus, that is everywhere, so every page rendered at 200
		// whatever its scan held. But native and cap are known WITHOUT any OCR
		// engine, and both are better information than a constant: a 960 DPI
		// scan of a survey certificate read at 200 loses the certificate.
		d.DPI, d.Reason = nativeDPI, "native-fallback"
	}
	if d.DPI > rp.MaxDPI {
		d.DPI = rp.MaxDPI
	}
	// What the content asked for, before the encoder's ceiling is applied. Tiles
	// are counted against THIS, not against the capped render: the cap is what
	// one canvas can show, and tiles are how many canvases the wanted detail
	// needs. Counting them at the capped DPI always yields 1 — that is what the
	// cap means — which would say "no tiles needed" about the very sheet that
	// cannot be read whole.
	d.WantDPI = d.DPI
	d.CapDPI = DPICapForArea(areaSqIn, maxTokens)
	// APPLY the cap. It was recorded and not applied until 2026-08-06, on the
	// argument that tiling is the answer to an oversized sheet and clamping
	// would hand back a sub-baseline page. Measured, that was wrong: rendering
	// ABOVE the cap is not merely wasteful, it is WORSE. The server halves an
	// oversized image until it fits, and each pass costs detail — the same root
	// read measured 9 tokens/in² rendered at 200 and 4 rendered at 400.
	// Rendering past what the encoder accepts buys extra downscales, nothing
	// else. Tiling is still the answer for an oversized sheet; it just is not an
	// argument for rasterising past the ceiling first.
	if d.CapDPI > 0 && d.DPI > d.CapDPI {
		d.DPI, d.Reason = d.CapDPI, "cap"
	}
	if nativeDPI > 0 && d.DPI > nativeDPI {
		// Past the scan's own resolution there is nothing to render.
		// Interpolation costs tokens and delivers no glyphs.
		d.DPI, d.Reason = nativeDPI, "native"
	}
	d.Tiles = TilesNeeded(areaSqIn, d.WantDPI, maxTokens)
	d.Density = TokensPerSqInAt(d.DPI)
	return d
}

// NativeDPI reports the resolution of the largest image on a PDF page, or 0 when
// there is none (born-digital) or poppler is unavailable.
//
// Largest rather than first: a scanned sheet often carries a small logo or
// signature stamp alongside the page raster, and the smaller image's ppi says
// nothing about what the page holds.
func NativeDPI(ctx context.Context, pdfPath string, page int) int {
	out, err := exec.CommandContext(ctx, "pdfimages", "-list",
		"-f", strconv.Itoa(page), "-l", strconv.Itoa(page), pdfPath).Output()
	if err != nil {
		return 0
	}
	best, bestPx := 0, 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// page num type width height color comp bpc enc interp object ID x-ppi y-ppi size ratio
		if len(f) < 14 {
			continue
		}
		w, err1 := strconv.Atoi(f[3])
		h, err2 := strconv.Atoi(f[4])
		xp, err3 := strconv.ParseFloat(f[12], 64)
		if err1 != nil || err2 != nil || err3 != nil || xp <= 0 {
			continue
		}
		if px := w * h; px > bestPx {
			bestPx, best = px, int(math.Round(xp))
		}
	}
	return best
}

// String renders a decision for a log line or a trace record.
func (d DPIDecision) String() string {
	return fmt.Sprintf("dpi=%d (%s; want=%d need=%d cap=%d native=%d) %.1f t/in² tiles=%d",
		d.DPI, d.Reason, d.WantDPI, d.NeedDPI, d.CapDPI, d.NativeDPI, d.Density, d.Tiles)
}
