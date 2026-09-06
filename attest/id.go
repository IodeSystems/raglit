package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
)

// Unit ids are content-addressed, and that is what makes a verdict outlive a
// re-read.
//
// Both producers renumber. raglit says so outright: a second read of the same
// sheet proposes different regions and the path ids shift. oidio renumbers every
// time a person joins or splits a turn. With ordinal ids, a verdict recorded
// against unit 14 silently becomes a verdict about whatever unit 14 is now —
// which is not a lost verdict, it is a FALSE one, and false attestation is the
// exact failure this framework exists to prevent.
//
// So the id is a digest of the claim itself. The consequences are deliberate:
//
//   - A re-read that lands on the same window, reading the same words from the
//     same artifact, produces the same id. The verdict carries forward with no
//     matching heuristic and no chance of mismatching.
//   - A re-read that produces DIFFERENT text for the same window produces a
//     different id, and the old verdict is orphaned rather than transferred.
//     This is the honest behaviour and it is occasionally expensive: swapping
//     the recogniser orphans a whole corpus. That is correct — nobody ruled on
//     the new claim. Recovering the overlap is a rebinding pass that proposes
//     matches for a person to accept, never something that happens silently.
//   - Evidence is IN the digest. Same box, same words, but cropped from a page
//     rasterized by a different renderer means the claim was read from different
//     pixels, and the person who attested it was shown a different image.
//   - Extra is NOT in the digest. A producer adding a diagnostic field must not
//     orphan every verdict in the corpus.
//   - Parent IS in the digest, so a unit re-parented under a different region is
//     a different unit. Its ancestry is part of what it claims.

// idHashBytes is how much of the digest the id carries. 8 bytes over a corpus
// of even millions of units is a collision probability in the 1e-8 range, and
// the id is a filename fragment and a log line a person reads.
const idHashBytes = 8

// UnitID computes a unit's content address.
//
// The Extra and ID fields are ignored; see the note above.
func UnitID(u Unit) string {
	h := sha256.New()
	field(h, "parent", u.Parent)
	switch {
	case u.Locator.Time != nil:
		t := u.Locator.Time
		field(h, "time.start", num(t.Start))
		field(h, "time.end", num(t.End))
	case u.Locator.Area != nil:
		a := u.Locator.Area
		field(h, "area.page", strconv.Itoa(a.Page))
		field(h, "area.x", num(a.BBox.X))
		field(h, "area.y", num(a.BBox.Y))
		field(h, "area.w", num(a.BBox.W))
		field(h, "area.h", num(a.BBox.H))
		field(h, "area.rotation", strconv.Itoa(a.Rotation))
		field(h, "area.dpi", strconv.Itoa(a.DPI))
	case u.Locator.Span != nil:
		sp := u.Locator.Span
		field(h, "span.from", strconv.Itoa(sp.From))
		field(h, "span.to", strconv.Itoa(sp.To))
	}
	field(h, "label", u.Label)
	field(h, "text", u.Text)
	field(h, "evidence", u.Evidence)
	return locatorPrefix(u.Locator) + hex.EncodeToString(h.Sum(nil)[:idHashBytes])
}

// field writes one length-prefixed value into the digest.
//
// Length-prefixed because transcribed text contains newlines and separators of
// every kind, and a delimiter-joined digest lets two different claims hash the
// same by moving a character across the boundary.
func field(h io.Writer, name, v string) {
	fmt.Fprintf(h, "%s:%d:%s", name, len(v), v)
}

// num formats a coordinate for hashing: the shortest decimal that round-trips,
// so a value written and re-read digests identically.
func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// locatorPrefix puts a human-readable hint in front of the digest — "p3-" for a
// region on page three, "t142.50-" for a turn two minutes in.
//
// Decoration, for reading a log and a diff. It MUST NOT be parsed: it is not
// unique, and a unit's real address is the whole string. Everything that needs
// the page or the time reads the locator.
func locatorPrefix(l Locator) string {
	switch {
	case l.Time != nil:
		return "t" + strconv.FormatFloat(l.Time.Start, 'f', 2, 64) + "-"
	case l.Area != nil:
		return "p" + strconv.Itoa(l.Area.Page) + "-"
	case l.Span != nil:
		return "b" + strconv.Itoa(l.Span.From) + "-"
	}
	return "u-"
}
