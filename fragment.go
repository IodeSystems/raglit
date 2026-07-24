package raglit

import "strings"

// Deterministic text fragmenter — overlapping windows snapped to line/paragraph
// boundaries. Text needs no model to be fragmented; this is the path for ALL
// documents a VLM did NOT transcribe (see pipeline.go's per-document choice).
//
// Overlap structurally solves the "a short hit loses its surrounding context"
// problem the LLM size-floor worried about, without paying a model to judge
// boundaries. Every fragment carries a half-open [Start,End) span into the source
// text, so get_document reassembles the document exactly (overlapping fragments
// share text — a plain join would repeat every overlap region) and same-doc hits
// are dedupable by span.

const (
	// Fragmenter defaults (chars), inherited from the old Assembler floor/ceiling
	// (~500/~1500 words). Tunable via config (Config.FragWindow/Stride/Floor) and
	// capped by the embed model's input limit. window > stride, so consecutive
	// fragments overlap by window-stride characters.
	defaultFragWindow = 9000 // ceiling: ~1500 words
	defaultFragStride = 6000 // 33% overlap
	defaultFragFloor  = 3000 // ~500 words; a tail shorter than this is folded, not emitted
)

// OffsetFrag is one windowed fragment plus its half-open [Start,End) span into
// the source text it was cut from.
type OffsetFrag struct {
	Start, End int
	Text       string
}

// ResolveFragParams applies defaults for non-positive values, caps the window by
// the embed model's input limit when known (embedLimit > 0), and enforces the
// windower's invariants: stride ≤ ¾·window (so a boundary-snapped window can't
// leave a coverage gap before the next window's start) and floor ≤ window.
func ResolveFragParams(window, stride, floor, embedLimit int) (int, int, int) {
	if window <= 0 {
		window = defaultFragWindow
	}
	if stride <= 0 {
		stride = defaultFragStride
	}
	if floor <= 0 {
		floor = defaultFragFloor
	}
	if embedLimit > 0 && window > embedLimit {
		window = embedLimit
	}
	if window < 1 {
		window = 1
	}
	if max := window * 3 / 4; stride > max {
		stride = max
	}
	if stride < 1 {
		stride = 1
	}
	if floor > window {
		floor = window
	}
	return window, stride, floor
}

// OverlapFragments cuts text into overlapping windows. window/stride/floor must
// already be resolved (ResolveFragParams). A document at or under one window is a
// single fragment with no overlap; a tail shorter than floor is folded into the
// final window rather than emitted as a near-duplicate ("barely-over document").
func OverlapFragments(text string, window, stride, floor int) []OffsetFrag {
	n := len(text)
	if n == 0 {
		return nil
	}
	if n <= window {
		return []OffsetFrag{{Start: 0, End: n, Text: text}}
	}
	var out []OffsetFrag
	start := 0
	for {
		end := start + window
		if end >= n {
			end = n
		} else {
			end = snapEnd(text, start, end)
		}
		out = append(out, OffsetFrag{Start: start, End: end, Text: text[start:end]})
		if end >= n {
			break
		}
		next := snapStart(text, start+stride)
		if next <= start {
			next = end // guarantee forward progress
		}
		// Degenerate tail: what remains from next is under the floor. The window
		// just emitted already covers to `end`; extend it to n rather than emit a
		// tiny near-duplicate window.
		if n-next < floor {
			last := &out[len(out)-1]
			last.End = n
			last.Text = text[last.Start:n]
			break
		}
		start = next
	}
	return out
}

// snapEnd pulls a window's end back to a paragraph (then line) boundary near
// hardEnd, but never past ¾ of the window — so the cut stays clean without
// collapsing the window or opening a gap before the next window's start. Returns
// an index just AFTER the separator (the newline stays with the closing fragment).
func snapEnd(text string, start, hardEnd int) int {
	if hardEnd >= len(text) {
		return len(text)
	}
	minEnd := start + (hardEnd-start)*3/4
	if i := lastSep(text, minEnd, hardEnd, "\n\n"); i > 0 {
		return i
	}
	if i := lastSep(text, minEnd, hardEnd, "\n"); i > 0 {
		return i
	}
	return hardEnd
}

// snapStart moves a window's start back to the nearest line/paragraph boundary
// within a short look-back, so a fragment never opens mid-line. Backing up only
// widens the overlap (offsets stay exact), so it is always safe.
func snapStart(text string, pos int) int {
	n := len(text)
	if pos <= 0 {
		return 0
	}
	if pos >= n {
		return n
	}
	const lookback = 512
	lo := max(pos-lookback, 0)
	if i := lastSep(text, lo, pos, "\n\n"); i > 0 {
		return i
	}
	if i := lastSep(text, lo, pos, "\n"); i > 0 {
		return i
	}
	return pos
}

// lastSep returns the index just after the last occurrence of sep in text[lo:hi],
// or 0 when there is none.
func lastSep(text string, lo, hi int, sep string) int {
	if lo < 0 {
		lo = 0
	}
	if hi > len(text) {
		hi = len(text)
	}
	if lo >= hi {
		return 0
	}
	idx := strings.LastIndex(text[lo:hi], sep)
	if idx < 0 {
		return 0
	}
	return lo + idx + len(sep)
}
