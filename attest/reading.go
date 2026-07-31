// Package attest is the machine-read-plus-human-verdict framework that oidio,
// raglit and kgraph each built separately and none of them built completely.
//
// The job is one job: an ASSET was read by a machine, the machine made claims
// about locatable pieces of it, and a person has to be able to rule on those
// claims durably — with an honest account of how much was actually ruled on.
// What varies between the three is only the LOCATOR. oidio addresses a span of
// time in a recording; raglit addresses a box on a page at a rotation and a dpi;
// kgraph addresses a (fact, source) edge. Everything else — the verdict
// vocabulary, the completeness accounting, the review loop, the discipline of
// showing a person the artifact the words actually came from — was reimplemented
// three times, and each implementation has a piece the other two lack.
//
// What each contributed, and why it is here:
//
//   - From oidio: the distinction between CONFIRMED and AFFIRMED. Listening to a
//     recording end to end and accepting the rest is a real act, and without a
//     way to record it the choice is to leave a finished pass looking half-done
//     or to tick every unit and destroy the question "did a person look at THIS
//     one". Also the generated completeness banner, which is what stops a
//     partly-reviewed transcript being cited as a verified one.
//   - From raglit: the digest of the exact artifact the model was shown. A
//     verdict is worthless if the person was looking at a different image than
//     the one that produced the text — and for a passage read off a rotated,
//     zoomed crop, handing them the whole page IS a different image.
//   - From kgraph: the append-only log, because two machines rewriting one JSON
//     object over a sync is a merge conflict and a lost verdict; and the
//     categorical verdict vocabulary, because "the document does not say this"
//     is not 0.3 of anything.
//
// This file defines what a machine produces. attestation.go defines what a
// person produces. state.go is how the two resolve into an answer.
package attest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readingSuffix names the machine's output beside the asset, the same
// convention oidio and raglit already use for their own sidecars. The suffix
// says what wrote it, so a re-read is detectable and nobody mistakes it for a
// hand-made note.
const readingSuffix = ".reading.json"

// readingVersion is the shape of the sidecar, so a consumer handed one written
// by a newer attest can say so rather than silently misreading it.
const readingVersion = 1

// ReadingPath is where an asset's machine read lives: beside it.
func ReadingPath(assetPath string) string { return assetPath + readingSuffix }

// IsReading reports whether a path is attest's own machine-read output.
func IsReading(path string) bool { return strings.HasSuffix(path, readingSuffix) }

// Asset is what was read. Digested, because every guarantee below is about
// pieces of THIS byte sequence — a re-encoded recording or a re-scanned page is
// a different asset wearing the same filename, and verdicts recorded against
// the old one do not transfer.
type Asset struct {
	// ID is the stable handle a consumer uses to refer to this asset: a path, a
	// URI, a content id. attest does not interpret it.
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Kind is audio | image | pdf | text. It selects which locator shape is
	// meaningful and which evidence the reviewer must be shown; it is not a MIME
	// type.
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256,omitempty"`
}

// Asset kinds.
//
// The pairing with Locator is the point: audio takes a Time, image and pdf take
// an Area, and text takes a Span. KindText is for an asset with no geometry at
// all — a note, an email, a markdown transcript. A SCANNED page whose text was
// read out of pixels is a pdf or an image, not text, because what the claim was
// read from is the crop.
const (
	KindAudio = "audio"
	KindImage = "image"
	KindPDF   = "pdf"
	KindText  = "text"
)

// Rect is a box in normalized coordinates — 0..1 of the page's width and
// height. Normalized rather than pixels so it survives a re-render at a
// different dpi, which is exactly what raglit's descent does when it zooms.
//
// Field names match raglit's Rect and kgraph's Region: the same four numbers
// spelled the same way in all three, because a translation layer between them
// is a place for x/y to swap.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// TimeSpan addresses a window of a recording, in seconds from its start.
//
// Absolute times, never an index into a segment list: the whole point of the
// review loop is that a person may re-cut the segmentation, and an offset
// within a segment stops meaning anything the moment the segment changes.
type TimeSpan struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Area addresses a piece of a page, at the rotation and rasterization it was
// read at.
//
// Rotation and DPI belong here rather than on the document because both vary
// per region in practice: one cell of a survey sheet holds text at four
// different angles, and a descent re-rasterizes the page to get more detail on
// a smaller box. A locator that records the box and not the resolution
// reproduces the geometry and loses the detail, and detail is the whole subject.
type Area struct {
	Page     int  `json:"page"`
	BBox     Rect `json:"bbox"`
	Rotation int  `json:"rotation,omitempty"`
	DPI      int  `json:"dpi,omitempty"`
}

// Span addresses a range of an asset's canonical text, in BYTES from its start.
//
// Byte offsets into the whole text, never an index into a fragment or page list
// — the same reasoning as TimeSpan. A reviewer may re-fragment or a producer may
// re-transcribe, and an offset within a fragment stops meaning anything the
// moment the fragmentation changes. Bytes rather than runes because that is what
// Go's strings.Builder counts and what raglit's RegionSpan already records; a
// producer converting between the two is a place for an off-by-one that only
// shows up on the one document with an accented name.
//
// This is the locator for text that has no geometry: a note, an email, a
// markdown transcript. Text that DOES have geometry — a block on a scanned page
// — is an Area, because the crop is what the claim was read from and a person
// checking it needs the pixels, not the characters.
type Span struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// Locator is where in the asset a claim points. Exactly one field is set.
//
// A tagged union rather than a flat struct with a Kind discriminator, so an
// audio unit cannot accidentally carry a bounding box and a consumer cannot
// read one that was never written. New media types add a field here; nothing
// about the verdict algebra changes when they do, which is the property this
// whole package is built to have.
type Locator struct {
	Time *TimeSpan `json:"time,omitempty"`
	Area *Area     `json:"area,omitempty"`
	Span *Span     `json:"span,omitempty"`
}

func (l Locator) empty() bool { return l.Time == nil && l.Area == nil && l.Span == nil }

// Unit is one machine claim about one piece of the asset.
//
// IMMUTABLE, and that is the load-bearing decision. oidio's workbench started
// with a single `speaker` field that held both the diarizer's grouping and the
// human's correction, which made a local fix and a global identity claim
// indistinguishable in the data. Here the unit is only ever what the MACHINE
// said; every human change lives in the attestation log. A re-read replaces
// units and cannot touch a verdict; a verdict cannot rewrite history.
type Unit struct {
	// ID is content-addressed — see UnitID. Assigned by Seal, not by the
	// producer.
	ID string `json:"id"`

	// Parent nests this unit under another, for producers that read
	// hierarchically: raglit's descent reads a sheet, then a drawing interior,
	// then a corner of it. Empty for a flat read.
	//
	// A parent REFERENCE rather than nested children, because the id is a digest
	// of the unit and nesting would make a parent's id depend on what was found
	// underneath it — so descending one level deeper would renumber the whole
	// ancestry and orphan every verdict above the new leaf.
	Parent string `json:"parent,omitempty"`

	Locator Locator `json:"locator"`

	// Text is what the machine read here. Empty is legitimate: a unit may carry
	// only a label, or be a container whose children hold the text.
	Text string `json:"text,omitempty"`

	// Label is the machine's CATEGORICAL claim: the diarizer's cluster, the
	// region's kind, the figure's class. Kept separate from Text because they
	// fail differently and a person rules on them separately — the words can be
	// right while the speaker is wrong, and conflating them is what made oidio's
	// first pass ambiguous.
	Label string `json:"label,omitempty"`

	// Evidence digests the EXACT artifact this claim was read from: the cropped
	// PNG, or the decoded PCM of the time window. Not the whole asset — the
	// whole asset is not what produced the claim, and showing a person the whole
	// asset is asking them to attest against a different image.
	//
	// Producers should record this. A unit without it can still be reviewed, but
	// nothing can then say whether what the reviewer saw is what the machine
	// saw, and Reproducible reports false.
	Evidence string `json:"evidence,omitempty"`

	// Flags are conditions that either hold or do not — low-resolution,
	// repetition, exhausted. Deliberately not a confidence score: a number here
	// would be an invented statistic, and the useful signal is categorical.
	Flags []string `json:"flags,omitempty"`

	// Extra is producer-specific data attest does not interpret and carries
	// through untouched — raglit's tokens-per-square-inch and downscale count,
	// oidio's word timings.
	//
	// Deliberately NOT part of the id. A producer adding a diagnostic field must
	// not orphan every verdict in the corpus.
	Extra json.RawMessage `json:"extra,omitempty"`
}

// HasFlag reports whether a condition was recorded on this unit.
func (u Unit) HasFlag(f string) bool {
	for _, x := range u.Flags {
		if x == f {
			return true
		}
	}
	return false
}

// Reading is one machine pass over one asset: the claims, and enough about who
// made them to tell two passes apart.
//
// Units are a FLAT list with parent references, not a tree. See Unit.Parent.
type Reading struct {
	Version int    `json:"version"`
	Asset   Asset  `json:"asset"`
	Units   []Unit `json:"units"`

	// Producer identifies what read the asset — "oidio/parakeet",
	// "raglit/regions". Two readings of the same asset by different producers
	// are different claims and are meant to be comparable, which is what makes
	// scoring one against a human pass possible.
	Producer string `json:"producer,omitempty"`
	Read     string `json:"read,omitempty"` // RFC3339
}

// Seal assigns content-addressed ids to every unit and returns the reading.
//
// Parents before children: a child's id depends on its parent's, so the list
// must be walked in dependency order. A producer that emits a child before its
// parent gets an error rather than a quietly wrong id.
func (r *Reading) Seal() error {
	r.Version = readingVersion
	// Producers address parents by whatever handle they used while reading —
	// raglit's "p1.0.2" path, an ordinal, anything. Sealing rewrites those to
	// content ids, so the mapping from the producer's handle to the real id has
	// to be built as we go.
	assigned := make(map[string]string, len(r.Units))
	for i := range r.Units {
		u := &r.Units[i]
		if u.Locator.empty() {
			return fmt.Errorf("attest: unit %d has no locator", i)
		}
		want := u.Parent
		if want != "" {
			id, ok := assigned[want]
			if !ok {
				return fmt.Errorf("attest: unit %d names parent %q, which has not been sealed yet — "+
					"emit parents before children", i, want)
			}
			u.Parent = id
		}
		before := u.ID
		u.ID = UnitID(*u)
		if before != "" {
			assigned[before] = u.ID
		}
		assigned[u.ID] = u.ID
	}
	return nil
}

// Unit resolves a unit by id.
func (r *Reading) Unit(id string) (Unit, bool) {
	for _, u := range r.Units {
		if u.ID == id {
			return u, true
		}
	}
	return Unit{}, false
}

// Children returns the units nested directly under id, in the order the
// producer emitted them — which is the producer's reading order and the only
// order that means anything for a drawing.
func (r *Reading) Children(id string) []Unit {
	var out []Unit
	for _, u := range r.Units {
		if u.Parent == id {
			out = append(out, u)
		}
	}
	return out
}

// Reproducible reports whether every unit recorded the artifact it was read
// from, and names the first that did not.
//
// A reading that is not reproducible can still be reviewed. What it cannot do
// is prove that the reviewer and the machine looked at the same thing, so a
// consumer that cares — anything publishing an attestation as evidence — should
// check this rather than assume it.
func (r *Reading) Reproducible() (bool, string) {
	for _, u := range r.Units {
		if u.Evidence == "" {
			return false, u.ID
		}
	}
	return true, ""
}

// ReadReading loads the sidecar beside an asset. A missing sidecar is not an
// error: most assets have not been read.
func ReadReading(assetPath string) (*Reading, bool, error) {
	b, err := os.ReadFile(ReadingPath(assetPath))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var rd Reading
	if err := json.Unmarshal(b, &rd); err != nil {
		return nil, false, fmt.Errorf("attest: %s: %w", ReadingPath(assetPath), err)
	}
	if rd.Version > readingVersion {
		return nil, false, fmt.Errorf("attest: %s was written by a newer attest (version %d, this one reads %d)",
			filepath.Base(ReadingPath(assetPath)), rd.Version, readingVersion)
	}
	return &rd, true, nil
}

// WriteReading replaces the machine read beside an asset.
//
// REPLACES, unlike the attestation log, and the asymmetry is the point: a
// re-read is a new set of claims and merging it with the old one would produce
// a reading no machine ever made. Verdicts are untouched by this — they live in
// a separate file and are keyed by content, so the ones that still apply
// survive and the ones that do not are reported as orphaned rather than
// silently re-attached to a claim nobody ruled on.
func WriteReading(assetPath string, r *Reading) (string, error) {
	if IsReading(assetPath) || IsLog(assetPath) {
		return "", fmt.Errorf("attest: %s is attest's own output; refusing to record a reading for it",
			filepath.Base(assetPath))
	}
	if err := r.Seal(); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	out := ReadingPath(assetPath)
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	return out, nil
}
