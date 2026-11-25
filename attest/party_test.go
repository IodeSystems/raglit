package attest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The record is exactly id and display name. Anything else is the registry
// growing an identity field it was designed not to have.
func TestPartyRecord_Minimal_IdAndDisplayNameOnly(t *testing.T) {
	dir := t.TempDir()
	if err := RegisterParty(dir, "p-001", "The Acme Company"); err != nil {
		t.Fatalf("RegisterParty: %v", err)
	}

	raw, err := os.ReadFile(PartyPath(dir))
	if err != nil {
		t.Fatalf("read the party file: %v", err)
	}
	var rec map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("the party record does not parse as a JSON object: %v", err)
	}
	var keys []string
	for k := range rec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"id", "name"}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("the record carries fields beyond id and display name: %v", keys)
	}
	var id, name string
	if err := json.Unmarshal(rec["id"], &id); err != nil || id != "p-001" {
		t.Errorf("id = %q, want p-001", id)
	}
	if err := json.Unmarshal(rec["name"], &name); err != nil || name != "The Acme Company" {
		t.Errorf("name = %q, want The Acme Company", name)
	}
}

// Registration creates the file beside the assets, with the id and display
// name in it, and touches nothing the corpus already had.
func TestRegisterParty_CreatesFileWithIdAndDisplayName(t *testing.T) {
	dir := t.TempDir()
	// The corpus has content before the party is registered.
	asset := filepath.Join(dir, "a.txt")
	if err := writeFile(asset, "a recording of the matter\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(LogPath(asset), `{"kind":"affirmed","unit":"u-1","blanket":true,"by":"reviewer"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	beforeAsset, _ := os.ReadFile(asset)
	beforeLog, _ := os.ReadFile(LogPath(asset))

	if _, err := os.Stat(PartyPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("a party file already exists: %v", err)
	}

	if err := RegisterParty(dir, "p-002", "R. Latham"); err != nil {
		t.Fatalf("RegisterParty: %v", err)
	}

	afterAsset, _ := os.ReadFile(asset)
	afterLog, _ := os.ReadFile(LogPath(asset))
	if string(beforeAsset) != string(afterAsset) {
		t.Error("registration changed an existing corpus file (the asset)")
	}
	if string(beforeLog) != string(afterLog) {
		t.Error("registration changed an existing corpus file (the verdict log)")
	}

	parts, err := Parties(dir)
	if err != nil {
		t.Fatalf("Parties: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("registered %d parties, want 1", len(parts))
	}
	if parts[0].ID != "p-002" || parts[0].Name != "R. Latham" {
		t.Errorf("record = %+v, want id p-002 and name R. Latham", parts[0])
	}
}

// A party is scoped to the corpus it was registered in. The same id in two
// corpora is two independent records, and registering in one leaves the
// other's file exactly as it was.
func TestRegisterParty_CorpusScoped_IndependentRecords(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()

	if err := RegisterParty(a, "p-003", "Northgate Holdings"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(PartyPath(a))
	if err != nil {
		t.Fatal(err)
	}

	// The same id, a different display name, in a different corpus.
	if err := RegisterParty(b, "p-003", "The Northgate Trust"); err != nil {
		t.Fatalf("RegisterParty in corpus B: %v", err)
	}

	after, err := os.ReadFile(PartyPath(a))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("registering in corpus B changed corpus A's party file")
	}

	pa, err := Parties(a)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := Parties(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(pa) != 1 || pa[0].Name != "Northgate Holdings" {
		t.Errorf("corpus A = %+v, want the original record untouched", pa)
	}
	if len(pb) != 1 || pb[0].Name != "The Northgate Trust" {
		t.Errorf("corpus B = %+v, want its own record", pb)
	}

	// The file in corpus B is its own: corpus A has exactly one record and it
	// is the one registered there.
	if _, err := os.Stat(PartyPath(b)); err != nil {
		t.Fatalf("corpus B has no party file: %v", err)
	}
}
