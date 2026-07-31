package attest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// What a person produces.
//
// Append-only, one JSON object per line, last relevant entry wins. Append-only
// because these files sync between machines, and two machines rewriting one
// JSON object is a merge conflict and a lost verdict — two machines appending
// lines is a merge a person can read. It is also the only structure in which
// "who said what, when" survives; a mutable verdict file answers "what is the
// current ruling" and destroys the record of how it was reached.
//
// The log is SEPARATE from the reading and is never rewritten by a re-read.
// That separation is the whole reason unit ids are content-addressed: the two
// files are joined by id, so a claim that survived a re-read keeps its verdict
// and one that did not is reported as orphaned rather than quietly re-attached
// to a ruling nobody made about it.

const logSuffix = ".attest.jsonl"

// LogPath is where an asset's verdicts live: beside it, next to the reading.
func LogPath(assetPath string) string { return assetPath + logSuffix }

// IsLog reports whether a path is attest's own verdict log.
func IsLog(path string) bool { return strings.HasSuffix(path, logSuffix) }

// Kind is what a person decided. Categorical, never a score: "the document does
// not say this" is not 0.3 of anything, and a confidence number here would be
// an invented statistic.
type Kind string

const (
	// Confirmed — a person went to this claim specifically, checked it, and
	// positively affirmed it.
	//
	// The ESCALATION, not the baseline. Going through a recording and accepting
	// what needs no change is the ordinary pass (see Affirmed); this is for the
	// passage something depends on — the sentence a case hinges on, the figure a
	// filing quotes. Somebody was asked to double-check that one, and did.
	//
	// So a low Confirmed count next to a high Affirmed count is not a thin
	// review. It means few claims were load-bearing enough to need re-checking,
	// which is the normal shape of a careful pass.
	Confirmed Kind = "confirmed"

	// Corrected — the machine was wrong. Text and Label carry what it should
	// say; whichever is empty was not disputed.
	//
	// Distinct from Confirmed on purpose, and it is the only thing that makes
	// word error rate meaningful: scoring a recogniser against text nobody
	// retyped measures it against itself and reports a flawless zero.
	Corrected Kind = "corrected"

	// Affirmed — the reviewer went through the asset and accepted this claim
	// under the standard they stated.
	//
	// THE ORDINARY PASS, and the description of it used to be wrong here in a way
	// that mattered. A reviewer listens to every turn, edits the ones that need
	// it, and moves past the ones they agree with; the affirmation at the end is
	// what covers everything they passed over. So an affirmed claim was HEARD.
	// Calling it "swept past, rather than judged" — which this comment did —
	// reports a thorough review as a shallow one, which is the same failure the
	// completeness account exists to prevent, running backwards.
	//
	// It is still not Confirmed, because the two answer different questions.
	// Affirmed says "I went through this and nothing here needed changing, to the
	// standard I stated". Confirmed says "something depends on this one and I
	// checked it again". Collapsing them would lose the ability to escalate.
	//
	// Statement carries the reviewer's own words. Ticking every unit instead
	// would destroy the distinction above, and paraphrasing the statement in the
	// tool would be worse: a qualified attestation with a materiality standard is
	// not "the rest is right".
	Affirmed Kind = "affirmed"

	// Unclear — looked, cannot tell. A verdict on the ARTIFACT, not the claim:
	// an inaudible passage, an illegible scan.
	//
	// It marks the units where neither the machine nor a person should be
	// expected to be right, and it keeps a bad recording from being silently
	// retried forever. It does not subtract: failing to read a scan is a fact
	// about the scan, and treating it as disproof would let a bad photocopy
	// strip a document of its evidence.
	Unclear Kind = "unclear"

	// Unsupported — the asset is SILENT here. Not "says the opposite": absent.
	//
	// The machine invented it. For a transcript that is a hallucinated turn; for
	// a page it is text read off a region that does not contain it. This is the
	// one verdict that subtracts, because a claim resting on nothing must stop
	// counting as read.
	Unsupported Kind = "unsupported"

	// Resegment — the machine's decomposition was wrong, and the person fixed
	// the pieces rather than the labels.
	//
	// This is not a nicety. It is the common case in both media and it is why
	// this framework is an EDITOR and not a survey: a diarization boundary
	// falling mid-sentence leaves one speaker's last word attached to the next
	// speaker's turn, and no amount of relabelling expresses the fix. The
	// raglit analogue is a region whose box was drawn around the wrong thing.
	//
	// Units carries the replacements and Supersedes names what they retire.
	Resegment Kind = "resegment"

	// Retract — take back an earlier ruling; the unit returns to untouched.
	//
	// Needed because the log cannot be edited. An undo is a later line, which is
	// also the honest record: the person did rule, and then withdrew it.
	Retract Kind = "retract"
)

func (k Kind) valid() bool {
	switch k {
	case Confirmed, Corrected, Affirmed, Unclear, Unsupported, Resegment, Retract:
		return true
	}
	return false
}

// Ruled reports whether this kind means a person judged the claim itself, as
// opposed to sweeping past it or withdrawing.
func (k Kind) Ruled() bool {
	switch k {
	case Confirmed, Corrected, Unclear, Unsupported:
		return true
	}
	return false
}

// Entry is one line of the log.
type Entry struct {
	Kind Kind `json:"kind"`

	// Unit is what this entry rules on. Empty when Blanket is set, and for
	// Resegment entries that only add units.
	Unit string `json:"unit,omitempty"`

	// Blanket applies this entry to every unit with no ruling EARLIER IN THE
	// LOG. Only meaningful with Affirmed.
	//
	// One line rather than one line per unit, so the sweep keeps its identity:
	// who swept and when is a single fact about the pass, and reconstructing it
	// by counting per-unit flags is how a reader ends up reporting the wrong
	// thing. Units added after this line are not covered — a sweep is a
	// statement about what had been read at the time it was made.
	Blanket bool `json:"blanket,omitempty"`

	// Text and Label carry a correction. Empty means "not disputed", not
	// "cleared to empty" — clearing text is a Resegment or an Unsupported, both
	// of which say something a blank field does not.
	Text  string `json:"text,omitempty"`
	Label string `json:"label,omitempty"`

	// Note is the reason: what the region actually says, why this paragraph is
	// about the septic and not the pole.
	Note string `json:"note,omitempty"`

	// Statement is the reviewer's own words on a blanket affirmation — the terms
	// they are accepting the remainder under.
	//
	// Separate from Note and never generated. An affirmation is a qualified claim
	// with a standard in it: "reasonably certain there are only minor errors or
	// mis-transcriptions that should not be materially relevant to the best of my
	// ability" is a different assertion from "the rest is right", and a tool that
	// paraphrases the first as the second has changed what somebody attested to.
	// Provenance QUOTES this rather than summarising it.
	//
	// Empty is honest and means the terms were not recorded — which is the case
	// for anything imported from a tool that had no field for them.
	Statement string `json:"statement,omitempty"`

	// Units and Supersedes are the payload of a Resegment.
	Units      []Unit   `json:"units,omitempty"`
	Supersedes []string `json:"supersedes,omitempty"`

	// By is the PERSON who made this ruling — or a machine identity like
	// `ocr-text-layer` when a process did.
	//
	// Self-declared, and required. Self-declared because the person at the
	// keyboard is routinely not the account holder: an attorney hands a
	// paralegal the link and the paralegal does the review, which is normal and
	// authorized and has to be recordable. Required because a defaulted author
	// reads afterwards exactly like a real one.
	//
	// The distinction that matters downstream is not who signed but whether a
	// PERSON did: a machine can honestly report that a string is or is not in a
	// text layer; it cannot report that a recording does not SUPPORT what was
	// transcribed.
	By string `json:"by"`

	// Auth is the authenticated account the ruling was made under, supplied by
	// the host's Identity and never by the caller.
	//
	// The other half of the pair. By alone lets any holder of a link type any
	// name; Auth alone files a delegate's work under the account holder's name.
	// Together they say what happened: authorized as X, performed by Y. Empty
	// means the mount is not account-based — a loopback binding, a shared link —
	// which is worth saying plainly rather than papering over with an invented
	// account.
	Auth string `json:"auth,omitempty"`

	At string `json:"at,omitempty"` // RFC3339
}

// Machine identity prefixes. A machine verifier is not a lesser person; it
// answers a narrower question.
var machinePrefixes = []string{"ocr-", "text-layer", "agent-", "raglit", "oidio", "attest-"}

// ByMachine reports whether this entry came from a process rather than a
// person. A predicate rather than a rank: what a machine ruling is worth is a
// reader's call, not an invented statistic.
func (e Entry) ByMachine() bool {
	if e.By == "" {
		return true
	}
	for _, p := range machinePrefixes {
		if strings.HasPrefix(e.By, p) {
			return true
		}
	}
	return false
}

func (e Entry) validate() error {
	if !e.Kind.valid() {
		return fmt.Errorf("attest: unknown verdict %q", e.Kind)
	}
	// Enforced on read as well as write. A line with no author is not a verdict
	// that merely lacks a nicety — it is a ruling nobody can be asked about, and
	// counting it towards coverage would report a pass as reviewed by nobody.
	if e.By == "" {
		return fmt.Errorf("attest: %q has no author", e.Kind)
	}
	switch e.Kind {
	case Resegment:
		if len(e.Units) == 0 && len(e.Supersedes) == 0 {
			return fmt.Errorf("attest: a resegment must add units, retire units, or both")
		}
	default:
		if e.Blanket {
			// Affirmed sweeps forward, Retract withdraws that sweep. Nothing else
			// may be blanket: a bulk `corrected` is not a thing anyone can mean,
			// and a bulk `confirmed` is precisely the act Affirmed exists to keep
			// distinguishable from individual judgement.
			if e.Kind != Affirmed && e.Kind != Retract {
				return fmt.Errorf("attest: %q cannot be blanket — only an affirmation sweeps, "+
					"and only a retraction withdraws one", e.Kind)
			}
		} else if e.Unit == "" {
			return fmt.Errorf("attest: %q needs a unit", e.Kind)
		}
	}
	return nil
}

// ReadLog loads an asset's verdicts in the order they were made. A missing log
// is not an error: a corpus nobody has reviewed yet is the normal starting
// state.
//
// A malformed line is an error rather than a skip. These files are evidence,
// and quietly dropping the line nobody could parse is how a verdict disappears
// without anyone being told.
func ReadLog(assetPath string) ([]Entry, error) {
	f, err := os.Open(LogPath(assetPath))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	// Corrected text of a long turn comfortably exceeds bufio's default 64K
	// line cap, and the failure mode is a truncated verdict, so raise it.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("attest: %s line %d: %w", filepath.Base(LogPath(assetPath)), n, err)
		}
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("attest: %s line %d: %w", filepath.Base(LogPath(assetPath)), n, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Append adds entries to an asset's log.
//
// O_APPEND with one Write per line, so a concurrent reviewer on the same file
// interleaves lines rather than losing them. No lock file: the format is
// designed so that any interleaving of complete lines is a valid log.
func Append(assetPath string, entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	for i := range entries {
		if err := entries[i].validate(); err != nil {
			return err
		}
		if entries[i].Units != nil {
			// A human-authored unit needs a content address like any other, and
			// the reviewer that produced it has no reason to compute one.
			for j := range entries[i].Units {
				entries[i].Units[j].ID = UnitID(entries[i].Units[j])
			}
		}
	}
	f, err := os.OpenFile(LogPath(assetPath), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return f.Sync()
}
