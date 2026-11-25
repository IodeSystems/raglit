package attest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The party a unit is ABOUT.
//
// The registry (party.go) answers "who are the people in this corpus" — a
// person's act of saying "that voice is THIS person". The assignment answers a
// different question: "this unit is about THAT party". The two are separate
// files for the same reason the registry is separate from the assets:
//
//   - The assignment is a LOG, not a field. A unit's party is not fixed at
//     read time; a machine proposes one, a person confirms it, and the record
//     of the proposal surviving the confirmation is what makes "person
//     confirmed" mean anything. A field would keep only the last value and lose
//     who proposed and who confirmed.
//
//   - The assignment lives beside the registry, at the corpus root, because it
//     is corpus-scoped: a unit's party is meaningful only within the corpus that
//     registered that party.
//
//   - The assignment does NOT touch the party file. Registering a party and
//     assigning a unit to it are two different acts by two different actors,
//     and a write to one that also rewrites the other is how the two bleed into
//     each other.
//
// Two states, and the distinction is the whole point:
//
//   - PROPOSED — a machine (or a person, in passing) says "this unit is about
//     party P". It is a reading-level claim, no more authoritative than any
//     other machine reading in the corpus. It is recorded, and it is visible,
//     but it is not an assignment in force.
//
//   - CONFIRMED — a person went to this unit specifically and affirmed the
//     party. This is the only state that counts as an assignment.
//
// A machine may propose. A machine may not confirm: "one voice" (machine may
// propose) is a different fact from "that voice is this person" (person only),
// and collapsing them is how a machine reading becomes a person's attestation
// of fact about who somebody is.

const assignmentFileName = "party-assignment.jsonl"

// AssignmentPath is where a corpus's party-assignment log lives: at the corpus
// root, beside the party registry.
func AssignmentPath(corpusRoot string) string {
	return filepath.Join(corpusRoot, assignmentFileName)
}

// IsAssignmentFile reports whether a path is attest's own party-assignment log.
func IsAssignmentFile(path string) bool {
	return filepath.Base(path) == assignmentFileName
}

// AssignmentState is who decided this entry.
type AssignmentState string

const (
	// MachineProposed — a machine (or a person, in passing) says this unit is
	// about this party. Recorded and visible, but not an assignment in force.
	// "One voice": the machine may propose.
	MachineProposed AssignmentState = "proposed"

	// PersonConfirmed — a person affirmed this unit's party. The only state
	// that counts as an assignment. "That voice is this person": a person only.
	PersonConfirmed AssignmentState = "confirmed"
)

func (s AssignmentState) valid() bool {
	return s == MachineProposed || s == PersonConfirmed
}

// Assignment is one line of the party-assignment log.
type Assignment struct {
	// Unit is the unit this entry is about: its content-addressed id.
	Unit string `json:"unit"`

	// Party is the party id the entry links the unit to. It must be a party
	// registered in the same corpus; the log records the id and does not
	// interpret it.
	Party string `json:"party"`

	// State is who decided: Proposed (machine or person, in passing) or
	// Confirmed (person only).
	State AssignmentState `json:"state"`

	// By is the actor who made this entry — a person, or a machine identity
	// like `agent-vision`. Self-declared and required, for the same reason the
	// verdict log requires it: a defaulted author reads afterwards exactly like
	// a real one.
	By string `json:"by"`

	// Auth is the authenticated account, supplied by the host and never by the
	// caller. Carried for the same reason the verdict log carries it.
	Auth string `json:"auth,omitempty"`

	At string `json:"at,omitempty"` // RFC3339
}

func (a Assignment) validate() error {
	if a.Unit == "" {
		return fmt.Errorf("attest: an assignment needs a unit")
	}
	if a.Party == "" {
		return fmt.Errorf("attest: an assignment needs a party id")
	}
	if !a.State.valid() {
		return fmt.Errorf("attest: unknown assignment state %q", a.State)
	}
	if a.By == "" {
		return fmt.Errorf("attest: an assignment has no author")
	}
	// A machine may propose; a machine may not confirm. "That voice is this
	// person" is a person's act, and a record that cannot tell the difference
	// is a false attestation.
	if a.State == PersonConfirmed && (Entry{By: a.By}).ByMachine() {
		return fmt.Errorf("attest: a machine may propose a party, not confirm one; %q recorded a confirmed assignment", a.By)
	}
	return nil
}

// AppendAssignment adds entries to a corpus's party-assignment log.
//
// Append-only, one JSON object per line, for the same reason the verdict log
// and the party registry are: the files sync between machines, and appending
// lines is a merge a person can read where rewriting an object is a conflict.
// The last entry about a unit is the one in force, so a person confirming a
// machine's proposal is a later line, and the record of the proposal survives.
//
// This write touches ONLY the assignment log. The party file is not read,
// written, or even opened: registering a party and assigning a unit to it are
// different acts, and a write to one that rewrites the other is how the two
// files bleed into each other.
func AppendAssignment(corpusRoot string, entries ...Assignment) error {
	if len(entries) == 0 {
		return nil
	}
	for i := range entries {
		if err := entries[i].validate(); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(AssignmentPath(corpusRoot), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
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

// Assignments reads a corpus's party-assignment log in the order the entries
// were made. A corpus with no assignments has no file, and that is the normal
// starting state, not an error.
//
// As with the registry and the verdict log, a malformed line is an error:
// these are identities, and quietly dropping the line nobody could parse is
// how a unit's party silently becomes a different person's.
func Assignments(corpusRoot string) ([]Assignment, error) {
	f, err := os.Open(AssignmentPath(corpusRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Assignment
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var a Assignment
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			return nil, fmt.Errorf("attest: %s line %d: %w", assignmentFileName, n, err)
		}
		if err := a.validate(); err != nil {
			return nil, fmt.Errorf("attest: %s line %d: %w", assignmentFileName, n, err)
		}
		out = append(out, a)
	}
	return out, sc.Err()
}

// ConfirmedParty returns the party a person has confirmed for this unit, or
// the empty string if there is none.
//
// The last entry about the unit is the one in force, and only a
// PersonConfirmed entry counts. A machine's proposal is recorded and visible,
// but it is not an assignment in force: the unit has no confirmed party until
// a person confirms one. A person may confirm a machine's proposal, and that
// confirmation is a later line — the proposal survives in the log as the
// record of who proposed it.
func ConfirmedParty(corpusRoot string, unit string) (string, error) {
	log, err := Assignments(corpusRoot)
	if err != nil {
		return "", err
	}
	confirmed := ""
	for _, a := range log {
		if a.Unit != unit {
			continue
		}
		if a.State == PersonConfirmed {
			confirmed = a.Party
		}
	}
	return confirmed, nil
}

// ProposedParty returns the party a machine (or a person) has proposed for
// this unit, or the empty string if there is none. It is the reading-level
// claim: visible to a reviewer who is deciding whether to confirm, and never
// counted as an assignment in force.
func ProposedParty(corpusRoot string, unit string) (string, error) {
	log, err := Assignments(corpusRoot)
	if err != nil {
		return "", err
	}
	proposed := ""
	for _, a := range log {
		if a.Unit != unit {
			continue
		}
		if a.State == MachineProposed {
			proposed = a.Party
		}
	}
	return proposed, nil
}
