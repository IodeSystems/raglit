package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iodesystems/raglit"
	"github.com/iodesystems/raglit/attest"
)

// Reviewing a document that has no geometry.
//
// `raglit regions` reads a SHEET: it descends into pixels because the text is
// too small to survive one look, and every claim it makes is anchored to a crop.
// Most of a corpus is not that. A note, an email, a markdown transcript is text
// already, with no page, no bbox and nothing to rasterize — and until attest
// grew a Span locator there was nowhere to point a claim inside one, so those
// documents could not be reviewed at all.
//
// The asymmetry with regions is deliberate and worth stating, because it looks
// like an inconsistency. A region's evidence is an IMAGE, and showing a reviewer
// the crop is the whole discipline: the words are a machine's reading of pixels
// and the pixels are the ground truth. A text asset has no such gap. The bytes
// ARE the document, so the evidence for a span is the span — quoted back exactly,
// with surrounding context so a reviewer can see whether a sentence was cut in a
// way that changes it.
//
// That makes the review a different question, and a real one: not "does the
// image say these words" but "does this passage support what was drawn from it".

// textParagraphs splits a document into reviewable units at blank lines.
//
// Paragraph grain, chosen against both neighbours. Sentences would put thousands
// of units in front of a reviewer for one email thread and make the sweep
// meaningless. The whole file as one unit cannot be corrected in part, so a
// single bad line would force `unsupported` on a document that is mostly right.
// A paragraph is the unit people already argue about — it is what gets quoted in
// a packet and what a correction lands on.
//
// Offsets are BYTES into the file as it sits on disk, which is what
// attest.Span promises and what raglit.RegionSpan already records.
func textParagraphs(text string) []attest.Span {
	var out []attest.Span
	i, n := 0, len(text)
	for i < n {
		// Skip the run of blank space between paragraphs, so a span never opens
		// on a newline the reviewer cannot see.
		for i < n && isBlankByte(text[i]) {
			i++
		}
		if i >= n {
			break
		}
		start := i
		for i < n {
			if text[i] == '\n' && blankLineAt(text, i+1) {
				break
			}
			i++
		}
		end := i
		for end > start && isBlankByte(text[end-1]) {
			end--
		}
		if end > start {
			out = append(out, attest.Span{From: start, To: end})
		}
	}
	return out
}

func isBlankByte(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// blankLineAt reports whether the line beginning at i is empty or whitespace.
func blankLineAt(s string, i int) bool {
	for ; i < len(s); i++ {
		if s[i] == '\n' {
			return true
		}
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\r' {
			return false
		}
	}
	return true // end of file closes the paragraph
}

// readingFromText builds a reading over a text asset.
//
// Flat, with no parents. Regions nest because a descent genuinely reads a sheet,
// then a corner of it, and a reviewer needs to know which look produced which
// words. Paragraphs have no such ancestry — inventing one from heading depth
// would put a claim under a parent no machine ever read, and Unit.Parent is IN
// the content address, so a wrong parent orphans the verdict on every re-read.
func readingFromText(path string) (*attest.Reading, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("raglit: %s is not valid UTF-8; byte offsets into it would not mean what attest.Span says they mean", path)
	}
	text := string(raw)
	spans := textParagraphs(text)
	if len(spans) == 0 {
		return nil, fmt.Errorf("raglit: %s holds no text to review", path)
	}
	rd := &attest.Reading{
		Asset: attest.Asset{
			ID:     filepath.Base(path),
			Name:   filepath.Base(path),
			Kind:   attest.KindText,
			SHA256: attest.SHA256Hex(raw),
		},
		Producer: "raglit/text",
		Read:     time.Now().Format(time.RFC3339),
	}
	for _, sp := range spans {
		body := text[sp.From:sp.To]
		rd.Units = append(rd.Units, attest.Unit{
			Locator: attest.Locator{Span: &attest.Span{From: sp.From, To: sp.To}},
			Text:    body,
			// The digest is over the span's OWN bytes, not the file's. A unit
			// whose paragraph is untouched must keep its identity when a
			// paragraph elsewhere in the file is edited, or every verdict in a
			// document dies on the first typo fix.
			Evidence: attest.SHA256Hex([]byte(body)),
			Extra: extraJSON(map[string]any{
				"bytes": sp.To - sp.From,
				"lines": strings.Count(body, "\n") + 1,
			}),
		})
	}
	return rd, nil
}

// textEvidence quotes the span back, with context around it.
//
// The digest is over the span's bytes ALONE, while the body served carries
// context on either side. That split is the point: attest checks the digest to
// answer "is this the artifact the claim was read from", and the answer has to
// stay yes when the reviewer is shown more than the bare passage. Digesting the
// context too would make the check fail whenever a neighbouring paragraph
// changed, which is exactly the false alarm that trains people to ignore it.
type textEvidence struct{ root string }

// contextBytes is how much either side. About a paragraph: enough to see whether
// a sentence was cut mid-thought, short enough that the passage under review is
// still obviously the subject.
const contextBytes = 400

func (e textEvidence) Render(_ context.Context, a attest.Asset, u attest.Unit) (attest.Artifact, error) {
	sp := u.Locator.Span
	if sp == nil {
		return attest.Artifact{}, fmt.Errorf("raglit: unit %s is not a span of a text document", u.ID)
	}
	raw, err := os.ReadFile(filepath.Join(e.root, a.ID))
	if err != nil {
		return attest.Artifact{}, err
	}
	if sp.From < 0 || sp.To > len(raw) || sp.From > sp.To {
		return attest.Artifact{}, fmt.Errorf(
			"raglit: %s is %d bytes and unit %s points at %d-%d — the file changed since it was read",
			a.ID, len(raw), u.ID, sp.From, sp.To)
	}
	body := raw[sp.From:sp.To]
	before := raw[max0(sp.From-contextBytes):sp.From]
	after := raw[sp.To:min(len(raw), sp.To+contextBytes)]

	var b strings.Builder
	if len(before) > 0 {
		b.WriteString("…")
		b.Write(before)
		b.WriteString("\n\n")
	}
	b.WriteString(">>> ")
	b.WriteString(strings.ReplaceAll(string(body), "\n", "\n>>> "))
	if len(after) > 0 {
		b.WriteString("\n\n")
		b.Write(after)
		b.WriteString("…")
	}
	return attest.Artifact{
		MIME:   "text/plain; charset=utf-8",
		Body:   []byte(b.String()),
		Digest: attest.SHA256Hex(body),
	}, nil
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// isTextAsset reports whether a path is reviewable as text rather than as pages.
//
// Deliberately narrow. A PDF or an image goes through regions even when a
// transcription sidecar exists beside it, because for those the crop is the
// evidence and the sidecar is a machine's reading of it — reviewing the sidecar
// as text would hand somebody characters when the open question is whether the
// pixels say them.
func isTextAsset(path string) bool {
	if isPDF(path) || raglit.IsTranscription(path) {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".txt", ".eml", ".text", ".markdown":
		return true
	}
	return false
}
