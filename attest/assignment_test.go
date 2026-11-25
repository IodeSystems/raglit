package attest

import (
	"encoding/json"
	"os"
	"testing"
)

// The log records the unit id linked to the party id, and the entry is stored
// in the party-assignment log. The party file itself is unchanged: a person
// assigning a unit to a party is not a write to the registry, and a write to
// the log that also rewrites the registry is how the two files bleed into
// each other.
func TestAppendAssignment_RecordsUnitIdLinkedToPartyId(t *testing.T) {
	dir := t.TempDir()

	// A registered party, in the corpus.
	if err := RegisterParty(dir, "p-001", "The Acme Company"); err != nil {
		t.Fatalf("RegisterParty: %v", err)
	}
	beforePartyFile, err := os.ReadFile(PartyPath(dir))
	if err != nil {
		t.Fatalf("read the party file before the assignment: %v", err)
	}

	// A person assigns a unit to that party.
	unitID := "t0.00-deadbeefdeadbeef"
	if err := AppendAssignment(dir, Assignment{
		Unit:  unitID,
		Party: "p-001",
		State: PersonConfirmed,
		By:    "carl",
	}); err != nil {
		t.Fatalf("AppendAssignment: %v", err)
	}

	// The entry is in the party-assignment log, with the unit id linked to the
	// party id.
	log, err := Assignments(dir)
	if err != nil {
		t.Fatalf("Assignments: %v", err)
	}
	if len(log) != 1 {
		t.Fatalf("assignment log has %d entries, want 1", len(log))
	}
	if log[0].Unit != unitID {
		t.Errorf("log entry unit = %q, want %q", log[0].Unit, unitID)
	}
	if log[0].Party != "p-001" {
		t.Errorf("log entry party = %q, want p-001", log[0].Party)
	}
	if log[0].State != PersonConfirmed {
		t.Errorf("log entry state = %q, want person-confirmed", log[0].State)
	}

	// The party file is unchanged: the assignment did not touch the registry.
	afterPartyFile, err := os.ReadFile(PartyPath(dir))
	if err != nil {
		t.Fatalf("read the party file after the assignment: %v", err)
	}
	if string(beforePartyFile) != string(afterPartyFile) {
		t.Error("the assignment changed the party file — the two logs bled into each other")
	}

	// The confirmed party is readable.
	confirmed, err := ConfirmedParty(dir, unitID)
	if err != nil {
		t.Fatalf("ConfirmedParty: %v", err)
	}
	if confirmed != "p-001" {
		t.Errorf("ConfirmedParty = %q, want p-001", confirmed)
	}
}

// A machine proposes a party for a unit with no prior assignment. The system
// stores the proposal as machine-proposed, and the unit has no confirmed party
// until a person confirms it. The machine's proposal is visible — a reviewer
// deciding whether to confirm sees it — but it is not an assignment in force.
func TestAssignment_MachineProposesNoConfirmedUntilPerson(t *testing.T) {
	dir := t.TempDir()
	if err := RegisterParty(dir, "p-002", "R. Latham"); err != nil {
		t.Fatalf("RegisterParty: %v", err)
	}

	unitID := "t1.00-cafebabe00000001"

	// The machine proposes. A machine identity: ByMachine() is true for it.
	if err := AppendAssignment(dir, Assignment{
		Unit:  unitID,
		Party: "p-002",
		State: MachineProposed,
		By:    "agent-vision",
	}); err != nil {
		t.Fatalf("machine proposal rejected: %v", err)
	}

	// The proposal is stored and visible.
	log, err := Assignments(dir)
	if err != nil {
		t.Fatalf("Assignments: %v", err)
	}
	if len(log) != 1 {
		t.Fatalf("log has %d entries, want 1", len(log))
	}
	if log[0].State != MachineProposed {
		t.Errorf("stored state = %q, want machine-proposed", log[0].State)
	}
	if log[0].By != "agent-vision" {
		t.Errorf("stored by = %q, want agent-vision", log[0].By)
	}
	proposed, err := ProposedParty(dir, unitID)
	if err != nil {
		t.Fatalf("ProposedParty: %v", err)
	}
	if proposed != "p-002" {
		t.Errorf("ProposedParty = %q, want p-002", proposed)
	}

	// No confirmed party until a person confirms.
	confirmed, err := ConfirmedParty(dir, unitID)
	if err != nil {
		t.Fatalf("ConfirmedParty: %v", err)
	}
	if confirmed != "" {
		t.Errorf("ConfirmedParty = %q before a person confirms; want empty", confirmed)
	}

	// A person confirms. The confirmation is a later line; the proposal
	// survives in the log as the record of who proposed it.
	if err := AppendAssignment(dir, Assignment{
		Unit:  unitID,
		Party: "p-002",
		State: PersonConfirmed,
		By:    "carl",
	}); err != nil {
		t.Fatalf("person confirmation rejected: %v", err)
	}

	log, err = Assignments(dir)
	if err != nil {
		t.Fatalf("Assignments after confirm: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("log has %d entries after confirm, want 2 (the proposal survives)", len(log))
	}
	if log[0].State != MachineProposed || log[1].State != PersonConfirmed {
		t.Errorf("log order = [%v, %v], want [machine-proposed, person-confirmed]", log[0].State, log[1].State)
	}
	if log[0].By != "agent-vision" {
		t.Errorf("the proposal's author was lost: %q", log[0].By)
	}

	confirmed, err = ConfirmedParty(dir, unitID)
	if err != nil {
		t.Fatalf("ConfirmedParty after confirm: %v", err)
	}
	if confirmed != "p-002" {
		t.Errorf("ConfirmedParty after confirm = %q, want p-002", confirmed)
	}
}

// A machine may propose, but it may not confirm. The log refuses a
// person-confirmed entry from a machine identity, on the way in AND on the way
// out, so a hand-edited file cannot smuggle one past the coverage count.
func TestAssignment_MachineCannotConfirm(t *testing.T) {
	dir := t.TempDir()
	if err := RegisterParty(dir, "p-003", "Northgate Holdings"); err != nil {
		t.Fatalf("RegisterParty: %v", err)
	}
	unitID := "t2.00-cafebabe00000002"

	// Write-time refusal.
	if err := AppendAssignment(dir, Assignment{
		Unit:  unitID,
		Party: "p-003",
		State: PersonConfirmed,
		By:    "ocr-text-layer",
	}); err == nil {
		t.Fatal("a machine confirmed a party — the write was not refused")
	}

	// Read-time refusal: a hand-edited line with a machine author and a
	// person-confirmed state must not be read as a valid confirmed assignment.
	path := AssignmentPath(dir)
	line := `{"unit":"` + unitID + `","party":"p-003","state":"confirmed","by":"agent-vision"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Assignments(dir); err == nil {
		t.Fatal("read a machine-confirmed assignment — the read was not refused")
	}
}

// A person may confirm a machine's proposal. The confirmation names the same
// party, and the log records both: the proposal and the confirmation, in
// order.
func TestAssignment_PersonConfirmsMachineProposal(t *testing.T) {
	dir := t.TempDir()
	if err := RegisterParty(dir, "p-004", "R. Latham"); err != nil {
		t.Fatalf("RegisterParty: %v", err)
	}
	unitID := "t3.00-cafebabe00000003"

	if err := AppendAssignment(dir, Assignment{
		Unit: unitID, Party: "p-004", State: MachineProposed, By: "agent-vision",
	}); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	if err := AppendAssignment(dir, Assignment{
		Unit: unitID, Party: "p-004", State: PersonConfirmed, By: "carl",
	}); err != nil {
		t.Fatalf("confirmation: %v", err)
	}

	log, err := Assignments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 {
		t.Fatalf("log has %d entries, want 2", len(log))
	}
	// The last entry in force is the person-confirmed one.
	if log[len(log)-1].State != PersonConfirmed {
		t.Errorf("last entry state = %q, want person-confirmed", log[len(log)-1].State)
	}
	confirmed, err := ConfirmedParty(dir, unitID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed != "p-004" {
		t.Errorf("ConfirmedParty = %q, want p-004", confirmed)
	}
}

// A corpus with no assignments has no file, and that is the normal starting
// state, not an error.
func TestAssignments_EmptyCorpus(t *testing.T) {
	dir := t.TempDir()
	if err := RegisterParty(dir, "p-005", "Anyone"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(AssignmentPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("an assignment file already exists: %v", err)
	}
	log, err := Assignments(dir)
	if err != nil {
		t.Fatalf("Assignments on an empty corpus: %v", err)
	}
	if len(log) != 0 {
		t.Errorf("got %d entries, want 0", len(log))
	}
	confirmed, err := ConfirmedParty(dir, "any-unit")
	if err != nil {
		t.Fatal(err)
	}
	if confirmed != "" {
		t.Errorf("ConfirmedParty on an empty corpus = %q, want empty", confirmed)
	}
}

// The record is exactly the fields a reader needs: unit, party, state, by.
// Anything more is the log growing a field it was designed not to have.
func TestAssignmentRecord_MinimalFields(t *testing.T) {
	dir := t.TempDir()
	unitID := "t4.00-cafebabe00000004"
	if err := AppendAssignment(dir, Assignment{
		Unit: unitID, Party: "p-001", State: PersonConfirmed, By: "carl",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(AssignmentPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("the record does not parse as a JSON object: %v", err)
	}
	want := map[string]bool{"unit": true, "party": true, "state": true, "by": true}
	for k := range rec {
		if !want[k] {
			t.Errorf("the record carries a field beyond the design: %q", k)
		}
	}
}

// A unit that has a proposal but no confirmation has no confirmed party, even
// if the proposal names a registered party. The empty/absence case.
func TestAssignment_ProposalAloneIsNotConfirmed(t *testing.T) {
	dir := t.TempDir()
	if err := RegisterParty(dir, "p-006", "R. Latham"); err != nil {
		t.Fatal(err)
	}
	unitID := "t5.00-cafebabe00000005"
	if err := AppendAssignment(dir, Assignment{
		Unit: unitID, Party: "p-006", State: MachineProposed, By: "agent-vision",
	}); err != nil {
		t.Fatal(err)
	}
	confirmed, err := ConfirmedParty(dir, unitID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed != "" {
		t.Errorf("ConfirmedParty = %q, want empty — a proposal is not a confirmation", confirmed)
	}
	// A different unit in the same corpus is unaffected.
	other, err := ConfirmedParty(dir, "t5.01-ffffffffffffffff")
	if err != nil {
		t.Fatal(err)
	}
	if other != "" {
		t.Errorf("a unit with no entries has a confirmed party: %q", other)
	}
}
