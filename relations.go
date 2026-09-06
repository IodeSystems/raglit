package raglit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// What a pair of overlapping documents IS, as opposed to how much text they share.
//
// similar.go answers the measurable question: how much of A occurs in B, where,
// and what differs. It deliberately stops there. Relation ("duplicate",
// "probe-inside-match") describes the OVERLAP, and the same overlap carries two
// meanings that have to be acted on differently:
//
//	COPY     the same instrument again. A scan of a PDF already held, an exhibit
//	         reproduced inside a filing. Nothing new; cite whichever is cleaner.
//	VERSION  the same instrument, filed or corrected differently. A re-recorded
//	         deed, an amended commitment, a second declaration. BOTH matter, and
//	         which one governs is the question.
//
// Calling a version a copy loses the amendment. Calling a copy a version invents
// a dispute. No coverage number separates them — a 0.97 alignment is equally
// consistent with either — so the separation has to come from WHAT disagrees,
// and then from a person.
//
// This file holds the SHAPE of a ruling and the proposal that precedes it; the
// storage is judgements.go. Rulings are durable — the one thing here that cannot
// be recomputed from the corpus — so they live in their own database beside the
// documents rather than in the index, which is derived, gitignored, per-machine
// and thrown away by a reindex.

// MarkKind is a person's ruling on an overlapping pair.
type MarkKind string

const (
	// MarkCopy — the same instrument, with no substantive difference. Whichever
	// copy is more legible is the one to cite; the other is redundant.
	MarkCopy MarkKind = "copy"
	// MarkVersion — the same instrument, differing in substance. Both are
	// evidence, and Supersedes records which one governs when that is known.
	MarkVersion MarkKind = "version"
	// MarkUnrelated — the overlap is real but means nothing: shared county forms,
	// a quoted paragraph, a common exhibit. Recorded rather than left blank so the
	// pair stops coming back as an open question.
	MarkUnrelated MarkKind = "unrelated"
)

// Mark is one ruling on a pair of documents.
//
// A and B are index-relative document paths, ORDER-NORMALIZED (A < B) so the pair
// is one fact rather than two and is found from either side. Supersedes is not
// normalized — it names a side, and which side is the whole content of an
// ordering.
type Mark struct {
	A    string   `json:"a"`
	B    string   `json:"b"`
	Kind MarkKind `json:"kind"`
	// Supersedes is the path that GOVERNS, for a version pair whose order is
	// known. Empty means the pair is two versions with no ruling on precedence —
	// a real and common state (two undated drafts), not a missing field.
	Supersedes string `json:"supersedes,omitempty"`
	Note       string `json:"note,omitempty"`
	By         string `json:"by,omitempty"`
	At         string `json:"at,omitempty"`

	// The evidence as it stood when the ruling was made, so a later reader can
	// see what was in front of the person. Not used for anything: a ruling is not
	// re-derived from these, it is quoted.
	Relation Relation `json:"relation,omitempty"`
	Coverage float64  `json:"coverage,omitempty"`
}

// pairKey identifies a pair independent of the order it was given in.
func pairKey(a, b string) string {
	if b < a {
		a, b = b, a
	}
	return a + "\x00" + b
}

// Normalize puts a mark's two sides in canonical order. Supersedes is untouched.
func (m Mark) Normalize() Mark {
	if m.B < m.A {
		m.A, m.B = m.B, m.A
	}
	return m
}

// Other returns the side of the pair that is not doc, and whether doc is in it.
func (m Mark) Other(doc string) (string, bool) {
	switch doc {
	case m.A:
		return m.B, true
	case m.B:
		return m.A, true
	}
	return "", false
}

// LegacyRelationsPath is the append-only file rulings used to live in, kept
// only so an existing corpus can be migrated into the database once.
func LegacyRelationsPath(projectDir string) string {
	return filepath.Join(projectDir, "relations.jsonl")
}

// ReadLegacyRelations parses the retired relations.jsonl. Later lines win, as
// the append-only format intended. Missing file is not an error.
func ReadLegacyRelations(path string) ([]Mark, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	seen := map[string]Mark{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var m Mark
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if m.A == "" || m.B == "" {
			return nil, fmt.Errorf("%s:%d: a mark needs both sides", path, n)
		}
		m = m.Normalize()
		k := m.A + "\x00" + m.B
		if _, ok := seen[k]; !ok {
			order = append(order, k)
		}
		seen[k] = m
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]Mark, 0, len(order))
	for _, k := range order {
		out = append(out, seen[k])
	}
	return out, nil
}

// check validates a ruling independently of where it is stored. Extracted from
// the old append-only writer so the database and any future writer enforce one
// definition rather than two that drift.
func (m Mark) check() error {
	if m.A == "" || m.B == "" {
		return fmt.Errorf("a mark needs both sides")
	}
	if m.A == m.B {
		return fmt.Errorf("a document cannot be a %s of itself", m.Kind)
	}
	switch m.Kind {
	case MarkCopy, MarkVersion, MarkUnrelated:
	default:
		return fmt.Errorf("unknown kind %q (want copy, version or unrelated)", m.Kind)
	}
	if m.Supersedes != "" {
		if m.Kind != MarkVersion {
			return fmt.Errorf("only a version pair can have a superseding side")
		}
		if m.Supersedes != m.A && m.Supersedes != m.B {
			return fmt.Errorf("superseding side %q is not one of the pair", m.Supersedes)
		}
	}
	return nil
}

// ── proposing ──────────────────────────────────────────────────────────

// Proposal is what raglit thinks a pair is, and why. It is never written to
// relations.jsonl: a proposal is an argument put in front of a person, and the
// file holds rulings.
type Proposal struct {
	Kind MarkKind `json:"kind"`
	// Why is the evidence in one line, for a person deciding. Written to be read
	// aloud in a review, not parsed.
	Why string `json:"why"`
	// Confident is whether this can be acted on without reading the documents.
	// Only exact-byte and near-exact alignment reach it.
	Confident bool `json:"confident"`
}

// yearRe matches a plausible four-digit year, and dateRe a numeric date. A
// disagreement in one of these is the strongest single signal that two copies of
// an instrument are two FILINGS of it.
var (
	yearRe = regexp.MustCompile(`^(19|20)\d{2}$`)
	dateRe = regexp.MustCompile(`^\d{1,2}[/-]\d{1,2}[/-]\d{2,4}$`)
)

// Propose reads a computed match and says what the pair probably is.
//
// The rule, and why it is this rule.
//
// Coverage decides whether the two are the same instrument at all; it cannot
// decide copy versus version, because a re-recorded deed and a scan of a deed
// both align at 0.97. What decides that is the SHAPE of the disagreement, and
// there are two kinds of disagreement in this corpus that look alike in a score
// and mean opposite things:
//
//	OCR NOISE      diffuse, alphabetic, one-sided. The 2008 lot certification
//	               reads "LAURENCE MOONION" for Clarence Brannock. Nothing was
//	               refiled; a scan was read badly. This is a COPY.
//	A SUBSTITUTION localized, numeric, and PAIRED — a number present only on one
//	               side sits against a different number present only on the other.
//	               An auditor file number, a distance, a date. Something was
//	               changed and filed. This is a VERSION.
//
// So the test for a version is paired numeric disagreement: tokens exclusive to
// each side, on BOTH sides. A number dropped from one side only is an omission,
// which OCR produces constantly, and does not qualify.
//
// This is a heuristic and it is allowed to be, because nothing acts on it — it
// proposes, a person rules, and the ruling is what gets stored. Its job is to
// put the right pairs and the right evidence in front of that person, not to be
// right on its own. Where it cannot tell, it says so rather than guessing.
func Propose(m DocMatch) Proposal {
	numeric := pairedNumericDisagreement(m.NumericOnlyInProbe, m.NumericOnlyInMatch)
	dated := hasDateLike(m.NumericOnlyInProbe) && hasDateLike(m.NumericOnlyInMatch)

	switch m.Relation {
	case RelIdentical:
		// Identical is decided on the text itself, and no disagreement survives
		// it. Nothing to weigh.
		return Proposal{Kind: MarkCopy, Why: "the two texts are the same", Confident: true}

	case RelDuplicate, RelProbeInside, RelMatchInside:
		// Say how much of the larger document the smaller one occupies.
		//
		// Without it a containment reads as "these are the same document", and a
		// 1,004-character preapproval letter inside a 156,321-character purchase
		// agreement is not that — it is a sub-document we already hold. The bare
		// word "copy" is true of the CONTENT and misleading about the pair, and
		// the person ruling needs to know which of the two they are looking at.
		where := "each is substantially the whole of the other"
		if m.Relation == RelProbeInside {
			where = fmt.Sprintf("the probe occurs whole inside the match, as %s of it", pct(m.BlockCoverMatch))
		} else if m.Relation == RelMatchInside {
			where = fmt.Sprintf("the match occurs whole inside the probe, as %s of it", pct(m.BlockCoverProbe))
		}
		switch {
		case dated:
			return Proposal{Kind: MarkVersion, Why: fmt.Sprintf(
				"%s, but a date differs across the aligned text (%s vs %s) — a refiling, not a second scan",
				where, joinFew(datesIn(m.NumericOnlyInProbe)), joinFew(datesIn(m.NumericOnlyInMatch)))}
		case numeric:
			return Proposal{Kind: MarkVersion, Why: fmt.Sprintf(
				"%s, but numbers differ on both sides across the aligned text (%s vs %s) — something was changed and filed",
				where, joinFew(m.NumericOnlyInProbe), joinFew(m.NumericOnlyInMatch))}
		case len(m.NumericOnlyInProbe)+len(m.NumericOnlyInMatch) > 0:
			// One-sided numeric loss. Almost always a failed read, but this is
			// exactly where a real deletion hides, so it does not get a confident
			// copy and the numbers are named.
			return Proposal{Kind: MarkCopy, Why: fmt.Sprintf(
				"%s; numbers missing from one side only (%s) — usually a failed read, but check them",
				where, joinFew(append(append([]string{}, m.NumericOnlyInProbe...), m.NumericOnlyInMatch...)))}
		default:
			return Proposal{Kind: MarkCopy, Why: where + ", with no numeric disagreement", Confident: m.Relation == RelDuplicate && m.Jaccard > 0.9}
		}

	default:
		// Overlap. Shared forms, a quotation, a common exhibit. Proposing
		// "unrelated" here is a claim about meaning that the numbers do not
		// support, so it stays open with the reason visible.
		return Proposal{Kind: "", Why: fmt.Sprintf(
			"they share passages (%d chars) without either containing the other — a quotation, a shared exhibit, or common forms",
			m.MatchedChars)}
	}
}

// pairedNumericDisagreement reports tokens exclusive to each side, on both
// sides — a substitution rather than an omission.
func pairedNumericDisagreement(a, b []string) bool {
	return len(a) > 0 && len(b) > 0
}

func hasDateLike(toks []string) bool { return len(datesIn(toks)) > 0 }

func datesIn(toks []string) []string {
	var out []string
	for _, t := range toks {
		if yearRe.MatchString(t) || dateRe.MatchString(t) {
			out = append(out, t)
		}
	}
	return out
}

// pct renders a fraction for a person. Below a tenth of a percent it says so
// rather than rounding to "0%", which reads as "none" when it means "tiny".
func pct(f float64) string {
	switch {
	case f <= 0:
		return "an unmeasured share"
	case f < 0.001:
		return "under 0.1%"
	case f < 0.01:
		return fmt.Sprintf("%.1f%%", f*100)
	default:
		return fmt.Sprintf("%.0f%%", f*100)
	}
}

// joinFew renders at most three tokens, because the point is to show a person
// what kind of thing differs, not to reproduce the diff.
func joinFew(toks []string) string {
	if len(toks) == 0 {
		return "none"
	}
	sort.Strings(toks)
	if len(toks) > 3 {
		return strings.Join(toks[:3], ", ") + fmt.Sprintf(" (+%d more)", len(toks)-3)
	}
	return strings.Join(toks, ", ")
}
