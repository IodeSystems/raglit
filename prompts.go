package raglit

import (
	"fmt"
	"strings"
)

// Every prompt a reader is ever given, in one place.
//
// They were four constants and three suffix functions spread across two files,
// assembled by string concatenation inside a switch. That arrangement hid the
// thing that actually went wrong: the crop prompt and the root prompt shared one
// JSON field, `description`, whose own doc comment admitted it was two jobs — "a
// transcription for a text block, a summary for a drawing". Every tile of a
// survey came back kind:drawing, so every tile returned a summary. Six tiles
// covering the sheet at 43-46 tokens/in², above the readable baseline of 39,
// and not one of them transcribed a character. The recorder's stamp was seen and
// described rather than read.
//
// So the questions are named and separated here, and the two indexes they feed
// are stated in the prompts themselves — because a reader told what its output is
// FOR gets cases right that no list of prohibitions enumerates.

// PromptKind is the QUESTION being asked. Four, not one with options: a crop is
// asked to transcribe, a root to account for a sheet, a parent to decide
// geometry, and a plain page to transcribe with no JSON at all.
type PromptKind string

const (
	// PromptPlain is the whole-page read on the ingest path: transcription only,
	// no JSON, no proposals. It is the pass that scores 14/14 on stamped
	// recording numbers and it is deliberately the simplest thing here.
	PromptPlain PromptKind = "plain"
	// PromptRoot accounts for a whole sheet and says where to look closely. Its
	// description is the PRIMARY product — an area it fails to name is an area
	// no later pass will crop, so the account decides coverage.
	PromptRoot PromptKind = "root"
	// PromptCrop reads one region. Transcription is the primary product.
	PromptCrop PromptKind = "crop"
	// PromptEscalate asks a PARENT what to do about a child that cannot read
	// itself. It REPLACES the question rather than modifying it, and forbids
	// transcription outright: the parent is looking at the coarse view that the
	// close-up exists because it cannot resolve.
	PromptEscalate PromptKind = "escalate"
)

// indexPreamble states the consumers. Shared by root and crop so both readers
// know the same two things exist and which field feeds which.
const indexPreamble = `This image is being read to build two different indexes. Each field below feeds
a different one, and someone looking for this document will use one or the other.

`

// transcriptionField is the verbatim half. Purpose first: the rules that matter
// follow from knowing that an exact string typed by a person has to match.
const transcriptionField = `transcription_markdown — FOR FULL-TEXT SEARCH. Someone will type an exact
  string and expect this document back: a recording number, a surname, a lot
  number, a bearing, a phrase from a covenant. Your transcription is the only
  thing they can match against.
  So the characters must be the ones on the page. A "corrected" spelling, an
  expanded abbreviation, a tidied number — each one makes this document
  unfindable by what is actually printed on it.
  Markdown is for STRUCTURE, because a table whose rows stay together is
  searchable and a table flattened into prose is not. Use it for tables,
  headings and lists. Never let formatting enter a word: emphasis marks are
  indexed literally, and **200808180120** does not match a search for
  200808180120.
  Text inside a DRAWING counts. Bearings, distances, lot and block numbers,
  monument calls, street names, dimensions, stamps and dates are exactly what
  people search for. A drawing is not exempt because it is a drawing; its
  labels are text.
  Where you cannot read something, write [illegible]. A gap is visibly a gap
  and someone can go and look. A guessed character is indistinguishable from a
  correct one and will be trusted — that is the worse outcome, always.
  If nothing here is readable, use "".
`

// descriptionField is the "what is this" half, for the index that answers a
// searcher who does not know the words on the page.
const descriptionField = `description — FOR FINDING THIS BY DESCRIBING IT. Someone who does not know the
  words on the page will search for what it is: "the 1947 plat of the golf
  course tract", "a surveyor's certificate", "a recorder's stamp". Write what
  such a person would recognise — the kind of document, what it depicts, what
  it concerns. One or two sentences.
  Do not put the reading here. It is already indexed by the field above, for
  the search that wants exact words; repeating it only pollutes the search that
  wants a description.
`

// rootDescriptionField replaces descriptionField at the root, where the account
// is also the coverage decision.
const rootDescriptionField = `description — FOR FINDING THIS BY DESCRIBING IT, and for deciding where to look
  closely. Name every distinct thing on this sheet — each drawing, map, table,
  legend, title block, certificate, signature block, stamp and note — and say
  where it sits and what it shows. A later pass crops the areas you name, so
  ANYTHING YOU DO NOT MENTION WILL NOT BE READ AT ALL. Completeness matters
  more than brevity.
`

const rootTranscriptionField = `transcription_markdown — FOR FULL-TEXT SEARCH, same rules as a close-up: the
  characters as printed, markdown for structure only, no emphasis inside a
  word, [illegible] rather than a guess. But this is the WHOLE sheet at one
  scale, so much of it will be too small to resolve. Transcribe what is
  legible; the close-up passes read what you cannot.
`

const kindField = `kind: one of overview, text-block, table, drawing, legend, title-block.
`

// regionsField is unchanged from the original prompt — the descent's routing,
// margin and verdict behavior all parse out of it.
const regionsField = `regions: areas worth examining MORE CLOSELY than this view allows — dense
  annotation, small print, a table, a title block. Coordinates are fractions of
  THIS image (0..1). rotation is 0, 90, 180 or 270: what this area must be
  turned by to read upright. Return [] if nothing here needs a closer look, or
  if the whole image is already legible.
Do not propose an area that covers most of this image unless it needs a
different rotation.

If this image cannot be read AS FRAMED and more paper would not fix it — it is
upside down or mirrored, or it plainly shows something other than what you were
told is here — answer with "verdict":"escalate" and say why in "because". Do not
guess at it and do not propose regions: the area and the rotation were chosen
outside this view, and correcting them is not yours to do. If it is framed fine
but simply too coarse to resolve at any treatment, answer "verdict":"illegible".

IF TEXT IS CUT OFF AT THE EDGE OF THIS IMAGE — a word ending mid-letters against
the border, a line running off the side — say so by proposing THIS WHOLE IMAGE
(x:0,y:0,w:1,h:1) with "margin" set to the inches of extra paper you need, up to
2. That is not a request to look somewhere else; it is this same view with more
of the page around it. Use it only when you can see the cut. If instead the image
looks wrong in a way MORE PAPER WOULD NOT FIX — upside down, or showing something
other than what you were told is here — propose nothing and say so in
description, because that is not yours to correct.`

const jsonSkeleton = `{"transcription_markdown": "...", "description": "...", "kind": "...", ` +
	`"verdict": "", "because": "", "regions": [{"x":0,"y":0,"w":0,"h":0,"rotation":0,"margin":0,"kind":"...","reason":"..."}]}`

// Mod is a modifier: something true about the PIXELS this reader was handed, or
// about what the caller wants. Modifiers never change the question.
type Mod func(*strings.Builder)

// WithHint states what the caller is looking for.
//
// Appended rather than woven in: the instruction competes with the image for the
// model's attention, and a hint that rewrote the whole prompt would also change
// the parts that make the answer parseable.
func WithHint(hint string) Mod {
	return func(b *strings.Builder) {
		h := strings.TrimSpace(hint)
		if h == "" {
			return
		}
		b.WriteString("\n\nWHAT THE CALLER IS LOOKING FOR: " + h +
			"\nIf this image contains any of it, transcribe that FIRST and in full, and " +
			"propose regions covering wherever more of it appears.")
	}
}

// WithDamage reports what was MEASURED about the pixels, and what may be asked
// about it.
//
// Measured 2026-08-03: the variance of the laplacian identifies a blurred crop
// that the model itself diagnoses as "skew" with 0.9 confidence — and the deskew
// it then prescribes makes a blurred region worse. So the number is what
// notices, and the model is what decides, because only it can see whether the
// region is a faded fax or a drawing that is mostly white space.
func WithDamage(damage []string) Mod {
	return func(b *strings.Builder) {
		var what []string
		for _, d := range damage {
			switch d {
			case FlagBlurred:
				what = append(what, "its strokes measure as SMEARED (low edge energy)")
			case FlagFaded:
				what = append(what, "its tones measure as CRUSHED into a narrow band")
			}
		}
		if len(what) == 0 {
			return
		}
		b.WriteString("\n\nMEASURED ABOUT THIS IMAGE: " + strings.Join(what, ", and ") + "." +
			"\nThese are measurements of the pixels, not a judgement about the document." +
			"\nIf a repair would help, propose THIS SAME AREA (x:0,y:0,w:1,h:1) with a" +
			" \"filter\" of \"contrast\" or \"sharpen\". Propose nothing if the region reads" +
			" fine as it is — a repair that recovers nothing is discarded anyway.")
	}
}

// WithGrid tells a computed tile that it is one, and what that obliges it to
// leave alone.
//
// The 45% rule is asymmetric on purpose. Cells overlap by descentPadIn, so an
// item near a seam is partly visible from both sides; without a rule both guess.
// The two failure modes are not equal — a duplicated monument call is
// recoverable by anyone reading the transcript, a dropped one is invisible. At
// 45% nothing can be dropped by geometry: if this cell holds under 45% of an
// item, the neighbour necessarily holds over 55% and takes it.
func WithGrid(grid string) Mod {
	return func(b *strings.Builder) {
		if strings.TrimSpace(grid) == "" {
			return
		}
		b.WriteString("\n\nTHIS IMAGE IS ONE CELL OF A GRID over a larger sheet — " + grid + "." +
			"\nThe neighbouring cells overlap this one, so text at your edges also appears in them." +
			"\nTranscribe an item only if you can see AT LEAST 45% of it. If less than that is" +
			" visible, leave it out entirely rather than guessing at it: the neighbouring cell" +
			" holds the rest and will transcribe it whole." +
			"\nThis will occasionally put the same item in two cells. That is intended and" +
			" harmless. Inventing the hidden half of one is not.")
	}
}

// AcceptsMods reports whether modifiers mean anything for a kind.
//
// Plain takes none because it is not looking at a crop — a grid rule or a
// damage report about "this image" has no referent on a whole page. Escalate
// takes none because it forbids transcription, and every modifier here is about
// how to transcribe; appending one contradicts the question.
func (k PromptKind) AcceptsMods() bool { return k == PromptRoot || k == PromptCrop }

// Prompt assembles the instruction for one read.
//
// Modifiers on a kind that does not accept them are DROPPED, not silently
// appended: see AcceptsMods for why each would contradict its prompt.
func Prompt(k PromptKind, mods ...Mod) string {
	var b strings.Builder
	switch k {
	case PromptPlain:
		return plainPrompt
	case PromptEscalate:
		return "" // escalation carries its own question; see EscalatePrompt
	case PromptRoot:
		b.WriteString("Look at this whole sheet and answer with ONE JSON object, nothing else:\n\n")
		b.WriteString(jsonSkeleton + "\n\n")
		b.WriteString(indexPreamble)
		b.WriteString(rootDescriptionField + "\n" + rootTranscriptionField + "\n" + kindField + "\n" + regionsField)
	case PromptCrop:
		b.WriteString("Look at this image and answer with ONE JSON object, nothing else:\n\n")
		b.WriteString(jsonSkeleton + "\n\n")
		b.WriteString(indexPreamble)
		b.WriteString(transcriptionField + "\n" + descriptionField + "\n" + kindField + "\n" + regionsField)
	default:
		return ""
	}
	if k.AcceptsMods() {
		for _, m := range mods {
			if m != nil {
				m(&b)
			}
		}
	}
	return b.String()
}

// plainPrompt is the whole-page ingest read: a transcription and nothing else.
//
// No JSON and no proposals, because nothing downstream wants them and every
// field asked for is attention taken from the one that matters. This is the pass
// measured at 14/14 on stamped recording numbers across 1947-2024; it is left
// alone on purpose.
const plainPrompt = "Transcribe all text visible in this document page image exactly as it appears, " +
	"preserving reading order and line breaks. Output ONLY the transcription: no commentary, " +
	"no headings you add yourself, no markdown code fences."

// EscalatePrompt asks a parent to decide GEOMETRY, and forbids a transcription.
//
// Separate from Prompt because it takes an argument — the case being put — and
// because it replaces rather than composes. `keep` means the child's reading
// stands, never "here is a better one": anything the parent transcribed here
// would be the low-resolution invention this package exists to prevent.
func EscalatePrompt(question string) string {
	if strings.TrimSpace(question) == "" {
		return ""
	}
	return "\n\nSOMETHING WENT WRONG WITH A CLOSER LOOK AT PART OF THIS IMAGE.\n" + question +
		"\n\nAnswer with ONE JSON object, nothing else:\n" +
		`{"action": "...", "regions": [{"x":0,"y":0,"w":0,"h":0,"rotation":0,"margin":0,"filter":"","kind":"..."}], "because": "..."}` +
		"\naction is one of:" +
		"\n  retransform — the area was right but rendered wrong; give the SAME area with a different rotation, filter or margin." +
		"\n  repick      — the area was wrong; give the area that should have been looked at instead." +
		"\n  keep        — the closer look was mistaken and what it read stands." +
		"\n  abandon     — there is nothing readable there at any treatment." +
		"\nregions: exactly one area, in fractions of THIS image (0..1), for retransform or repick. Empty otherwise." +
		"\nDO NOT TRANSCRIBE ANYTHING. You are looking at this area at a scale that cannot resolve its small text —" +
		" that is why a closer look was taken. Decide where to look and how; the closer look does the reading."
}

// FlattenMarkdownForIndex turns a stored transcription into the plain lines the
// index should hold.
//
// The artifact keeps its markdown: a monument table read as a table is worth
// having, and a reader opening <doc>.raglit-transcription.md should see one.
// The INDEX wants neither the pipes nor the rules — FTS5 tokenises them away as
// separators, but they cost fragment budget, they carry into embeddings as
// noise, and a cell boundary inside a phrase is a boundary the searcher did not
// type.
//
// Cells become space-separated on one line, so a row stays one unit of text and
// a phrase spanning two columns still matches. Alignment rows (|---|:--:|) carry
// no content and are dropped entirely.
func FlattenMarkdownForIndex(md string) string {
	var out []string
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if isTableRule(t) {
			continue
		}
		if strings.HasPrefix(t, "|") {
			cells := strings.Split(strings.Trim(t, "|"), "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			var kept []string
			for _, c := range cells {
				if c != "" {
					kept = append(kept, c)
				}
			}
			out = append(out, strings.Join(kept, " "))
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// isTableRule matches a markdown alignment row — only pipes, dashes, colons and
// spaces, with at least one dash so a row of empty cells is not mistaken for one.
func isTableRule(s string) bool {
	if !strings.HasPrefix(s, "|") || !strings.Contains(s, "-") {
		return false
	}
	for _, r := range s {
		switch r {
		case '|', '-', ':', ' ', '\t':
		default:
			return false
		}
	}
	return true
}

var _ = fmt.Sprintf
