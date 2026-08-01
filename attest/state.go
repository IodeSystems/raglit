package attest

import (
	"fmt"
	"strings"
)

// Resolving a machine reading and a verdict log into an answer.
//
// This is the only place that knows how the two files combine, and everything
// downstream — the review UI, the renderer, the scorer, a host service serving
// an unattested transcript — reads State rather than interpreting the log
// itself. That is a deliberate response to a failure oidio hit: a renderer that
// kept its own idea of what `confirmed` meant kept reporting the old coverage
// for days after a field was added, understating how much had been reviewed. A
// second reader of this vocabulary WILL go stale, so there is one.

// UnitStatus is one unit's effective state: what the machine claimed, and what a
// person did about it.
type UnitStatus struct {
	Unit Unit `json:"unit"`

	// Kind is the ruling in force, or empty for untouched.
	Kind Kind `json:"kind,omitempty"`

	// Text and Label are what this unit says NOW — the correction where one was
	// made, the machine's reading otherwise. A consumer rendering a transcript
	// wants these; a consumer scoring the machine wants Unit.Text and these both,
	// which is why the original is never overwritten.
	Text  string `json:"text,omitempty"`
	Label string `json:"label,omitempty"`

	Note string `json:"note,omitempty"`

	// By is the person who ruled; Auth is the account they were authorized
	// under. Both are carried, because a delegate doing the work is normal and a
	// reader asking "who actually reviewed this" must not be handed the account
	// holder's name instead. See identity.go.
	By   string `json:"by,omitempty"`
	Auth string `json:"auth,omitempty"`
	At   string `json:"at,omitempty"`

	// Authored marks a unit a person created with a Resegment, rather than one
	// the machine proposed. It is not itself a ruling: someone may cut a turn in
	// the right place and still not have decided who is speaking.
	Authored bool `json:"authored,omitempty"`

	// Swept marks a ruling that came from a blanket affirmation rather than a
	// line about this unit. Kind is Affirmed either way; this says whether anyone
	// named it. Not a demotion: the reviewer went through it, and the affirmation
	// they signed says under what terms.
	Swept bool `json:"swept,omitempty"`
}

// Corrected reports whether a person actually retyped this unit's words. The
// only units that are ground truth for word error rate — scoring a recogniser
// against text nobody disputed measures it against itself.
func (s UnitStatus) Corrected() bool { return s.Kind == Corrected && s.Text != s.Unit.Text }

// Stats is the completeness account. The states are reported SEPARATELY and
// never collapsed into one percentage: a single number hides which failure
// produced it.
//
// The distinction that has to survive is between a claim NOBODY HAS READ and one
// a reviewer went through and accepted. Confirmed and Affirmed are both the
// latter — the difference between them is whether something depended on this
// particular claim enough to check it twice. Untouched is the one that means
// nothing is known.
type Stats struct {
	Total       int `json:"total"`
	Confirmed   int `json:"confirmed"`
	Corrected   int `json:"corrected"`
	Affirmed    int `json:"affirmed"`
	Unclear     int `json:"unclear"`
	Unsupported int `json:"unsupported"`
	Untouched   int `json:"untouched"`

	// Authored is how many units a person cut themselves. A high count means the
	// machine's decomposition was wrong, which is a different complaint from its
	// text being wrong and is worth surfacing separately.
	Authored int `json:"authored"`

	// SweptBy, SweptAt and SweptStatement record the last blanket affirmation, so
	// a consumer can state provenance without reconstructing it by counting flags.
	//
	// SweptStatement is the reviewer's own words, and an empty one means the terms
	// were not recorded rather than that there were none. Provenance says which.
	SweptBy        string `json:"swept_by,omitempty"`
	SweptAt        string `json:"swept_at,omitempty"`
	SweptStatement string `json:"swept_statement,omitempty"`
}

// Complete reports whether every unit has been accounted for — read and
// accepted, or ruled on. Confirmed and Affirmed both count, because both mean a
// reviewer went through the claim; the separate fields say which.
func (s Stats) Complete() bool { return s.Total > 0 && s.Untouched == 0 }

// Ruled is how many units a person went to individually — checked, corrected,
// or marked unreadable — as opposed to accepting under an affirmation.
func (s Stats) Ruled() int { return s.Confirmed + s.Corrected + s.Unclear + s.Unsupported }

// State is a reading and its verdicts, resolved.
type State struct {
	Asset    Asset        `json:"asset"`
	Producer string       `json:"producer,omitempty"`
	Units    []UnitStatus `json:"units"`
	Stats    Stats        `json:"stats"`

	// Orphaned names verdicts that rule on units this reading does not contain
	// — the cost of a re-read that changed what the machine claims.
	//
	// Reported rather than dropped, and rather than matched onto whatever is
	// nearest. A verdict silently re-attached to a claim nobody ruled on is a
	// FALSE attestation, which is worse than a lost one. Recovering the overlap
	// is a rebinding pass that proposes matches for a person to accept.
	Orphaned []Entry `json:"orphaned,omitempty"`
}

// Resolve applies a verdict log to a machine reading.
//
// Entries are applied in log order and the last one about a unit wins, so an
// undo is a later line and the record of the original ruling survives.
func Resolve(r *Reading, log []Entry) (*State, error) {
	if r == nil {
		return nil, fmt.Errorf("attest: no reading")
	}
	st := &State{Asset: r.Asset, Producer: r.Producer}

	// order holds the effective unit list; at names each unit's position in it.
	// A resegment splices its replacements in where the first retired unit was,
	// so a turn cut in two stays where it was in the transcript rather than
	// jumping to the end. Reading order is not decoration here — it is how the
	// reviewer navigates.
	order := make([]UnitStatus, 0, len(r.Units))
	at := make(map[string]int, len(r.Units))
	// known remembers every id that has ever been in the effective set, so a
	// verdict on a unit that was later retired is not misreported as an orphan.
	// It was a real ruling on a real claim; the claim was simply replaced.
	known := make(map[string]bool, len(r.Units))

	for _, u := range r.Units {
		if _, dup := at[u.ID]; dup {
			// Two identical claims collapse to one id by construction. A reading
			// that emits the same claim twice is a producer bug, and silently
			// keeping one of them would make the unit count wrong.
			return nil, fmt.Errorf("attest: reading contains unit %s twice — "+
				"two units with the same locator, text, label and evidence are the same claim", u.ID)
		}
		at[u.ID] = len(order)
		known[u.ID] = true
		order = append(order, UnitStatus{Unit: u, Text: u.Text, Label: u.Label})
	}

	for _, e := range log {
		switch e.Kind {
		case Resegment:
			splice := -1
			for _, id := range e.Supersedes {
				i, ok := at[id]
				if !ok {
					continue
				}
				if splice < 0 || i < splice {
					splice = i
				}
			}
			kept := make([]UnitStatus, 0, len(order))
			retire := map[string]bool{}
			for _, id := range e.Supersedes {
				retire[id] = true
			}
			for _, s := range order {
				if !retire[s.Unit.ID] {
					kept = append(kept, s)
				}
			}
			add := make([]UnitStatus, 0, len(e.Units))
			for _, u := range e.Units {
				if _, dup := at[u.ID]; dup && known[u.ID] {
					// The person re-proposed a claim that is already present.
					// Nothing to add — the unit exists and can be ruled on.
					continue
				}
				known[u.ID] = true
				add = append(add, UnitStatus{
					Unit: u, Text: u.Text, Label: u.Label,
					Authored: true, By: e.By, Auth: e.Auth, At: e.At,
				})
			}
			if splice < 0 || splice > len(kept) {
				splice = len(kept)
			}
			order = append(kept[:splice:splice], append(add, kept[splice:]...)...)
			at = reindex(order)

		case Retract:
			if e.Blanket {
				// Withdraw a sweep, and ONLY a sweep. Individual rulings are left
				// exactly as they are: a person undoing "the rest is right" is
				// taking back the bulk acceptance, not the turns they sat and
				// judged one at a time.
				for i := range order {
					if !order[i].Swept {
						continue
					}
					s := &order[i]
					s.Kind, s.Note, s.By, s.Auth, s.At, s.Swept = "", "", "", "", "", false
					s.Text, s.Label = s.Unit.Text, s.Unit.Label
				}
				st.Stats.SweptBy, st.Stats.SweptAt, st.Stats.SweptStatement = "", "", ""
				continue
			}
			fallthrough

		case Affirmed:
			if e.Kind == Affirmed && e.Blanket {
				for i := range order {
					if order[i].Kind != "" {
						continue
					}
					order[i].Kind = Affirmed
					order[i].Swept = true
					order[i].By, order[i].Auth, order[i].At = e.By, e.Auth, e.At
				}
				st.Stats.SweptBy, st.Stats.SweptAt = e.By, e.At
				st.Stats.SweptStatement = e.Statement
				continue
			}
			fallthrough

		default:
			i, ok := at[e.Unit]
			if !ok {
				if !known[e.Unit] {
					st.Orphaned = append(st.Orphaned, e)
				}
				continue
			}
			s := &order[i]
			if e.Kind == Retract {
				// Back to untouched, including the correction. A retraction that
				// left corrected text in place would leave the transcript
				// carrying a reading its author has withdrawn.
				s.Kind, s.Note, s.By, s.Auth, s.At, s.Swept = "", "", "", "", "", false
				s.Text, s.Label = s.Unit.Text, s.Unit.Label
				continue
			}
			s.Kind, s.Note, s.By, s.Auth, s.At = e.Kind, e.Note, e.By, e.Auth, e.At
			s.Swept = false
			// Empty means "not disputed", never "cleared to empty" — see Entry.
			if e.Text != "" {
				s.Text = e.Text
			}
			if e.Label != "" {
				s.Label = e.Label
			}
		}
	}

	st.Units = order
	st.Stats.Total = len(order)
	for _, s := range order {
		if s.Authored {
			st.Stats.Authored++
		}
		switch s.Kind {
		case Confirmed:
			st.Stats.Confirmed++
		case Corrected:
			st.Stats.Corrected++
		case Affirmed:
			st.Stats.Affirmed++
		case Unclear:
			st.Stats.Unclear++
		case Unsupported:
			st.Stats.Unsupported++
		default:
			st.Stats.Untouched++
		}
	}
	return st, nil
}

func reindex(order []UnitStatus) map[string]int {
	at := make(map[string]int, len(order))
	for i, s := range order {
		at[s.Unit.ID] = i
	}
	return at
}

// Load resolves an asset's reading and log from disk.
func Load(assetPath string) (*State, error) {
	r, ok, err := ReadReading(assetPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("attest: %s has no reading — nothing has read it yet", assetPath)
	}
	log, err := ReadLog(assetPath)
	if err != nil {
		return nil, err
	}
	return Resolve(r, log)
}

// Provenance is the sentence a consumer must print beside anything derived from
// this asset.
//
// Generated, never written by hand. These banners were hand-written once, which
// meant they said whatever someone believed at the time — and on the corpus
// this was built against that produced a header claiming a completeness the
// data did not carry. A transcript is a source document downstream: facts get
// extracted from it and cited, and a file that is 18-of-39 confirmed presented
// as "the verified transcript" launders an unchecked machine guess into the
// record one step removed.
//
// Two axes, reported separately, because they are the two people conflate:
// whether the CLAIMS were ruled on, and whether the WORDS were corrected.
// Checking who spoke says nothing about whether the text is right.
func (st *State) Provenance() string {
	s := st.Stats
	var b strings.Builder
	if s.Total == 0 {
		return "No machine reading of this asset has been recorded."
	}
	if s.Complete() {
		var parts []string
		if s.Ruled() > 0 {
			parts = append(parts, fmt.Sprintf("%d checked individually", s.Ruled()))
		}
		if s.Affirmed > 0 {
			parts = append(parts, fmt.Sprintf("%d accepted under the reviewer's affirmation", s.Affirmed))
		}
		fmt.Fprintf(&b, "Review is COMPLETE — %s, of %d units.", strings.Join(parts, ", "), s.Total)

		// The affirmation is QUOTED, never summarised. A reviewer who went through
		// a recording and accepted what needed no change made a qualified claim
		// with a standard in it, and a tool that restates that in its own words has
		// changed what was attested to.
		if s.Affirmed > 0 {
			if s.SweptStatement != "" {
				fmt.Fprintf(&b, " The affirmation reads: %q%s.", s.SweptStatement, by(s.SweptBy, s.SweptAt))
			} else {
				fmt.Fprintf(&b, " Those units were accepted as part of a blanket affirmation%s;"+
					" its terms were not recorded by the tool that made it.", by(s.SweptBy, s.SweptAt))
			}
		}
	} else {
		fmt.Fprintf(&b, "Review is INCOMPLETE — %d checked individually, %d accepted under an affirmation,"+
			" %d of %d units UNTOUCHED. Nobody has ruled on the untouched units and nothing here says"+
			" they were read at all.", s.Ruled(), s.Affirmed, s.Untouched, s.Total)
	}
	if s.Corrected == 0 {
		b.WriteString(" The WORDS are unverified: no unit has had its text corrected by a person." +
			" Verify any quotation against the source before relying on its exact wording.")
	} else {
		fmt.Fprintf(&b, " The WORDS are only partly verified — %d of %d units have had their text"+
			" corrected by a person; the rest is machine output.", s.Corrected, s.Total)
	}
	if n := len(st.Orphaned); n > 0 {
		fmt.Fprintf(&b, " %d earlier verdict(s) do not match this reading and are not counted above.", n)
	}
	return b.String()
}

func by(who, when string) string {
	var s string
	if who != "" {
		s += " by " + who
	}
	if len(when) >= 10 {
		s += " on " + when[:10]
	}
	return s
}
