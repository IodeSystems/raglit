package attest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Who a unit is ABOUT.
//
// The verdict log names who RULED — By and Auth, a person's act. A transcript
// also has people it is ABOUT: the speaker on the other end, the party a
// statement is made by. Those are a different fact from any act of review,
// and they live in a different file, because that is the only shape that
// keeps the two from bleeding into each other:
//
//   - The registry is scoped to a CORPUS, not to an asset. The same person
//     appears in every recording of the matter, and a per-asset file would
//     say so ten times where it is a fact once.
//
//   - The registry is its OWN file, beside the assets and their verdict
//     logs, so an asset can be handed to another tool without carrying the
//     identities of the people in it. A verdict log that had grown a party
//     name field would make that impossible.
//
//   - The record is id and display name and NOTHING ELSE. The id is what a
//     consumer that wants to say more — kgraph, caselit — keys on; the
//     display name is what a person sees. Anything added is a reason to
//     re-examine why it belongs here.
//
// Registration is a person's act: it is "that voice is THIS person", not
// "this voice is one voice". A machine may PROPOSE a party — that is a
// reading-level concern — but the registry records only what a person
// registered. The assignment of a unit to a party is a separate log entry,
// with its own states, and belongs to a separate file; it is not here.

const partyFileName = "party.jsonl"

// PartyPath is where a corpus's party registry lives: at the corpus root,
// beside the assets and the verdict logs that live beside them.
func PartyPath(corpusRoot string) string { return filepath.Join(corpusRoot, partyFileName) }

// IsPartyFile reports whether a path is attest's own party registry.
func IsPartyFile(path string) bool { return filepath.Base(path) == partyFileName }

// Party is one record of the registry. The record is deliberately the two
// fields and no more; see the package comment for why.
type Party struct {
	// ID is the stable handle a consumer keys on. attest does not interpret it.
	ID string `json:"id"`
	// Name is what a person sees.
	Name string `json:"name"`
}

// RegisterParty adds a party to a corpus's registry.
//
// Append-only, one JSON object per line, for the same reason the verdict log
// is: the files sync between machines, and appending lines is a merge a
// person can read where rewriting an object is a conflict and a lost record.
// Registering a party already in the corpus appends a line; the last record
// for an id is the one in force, and the earlier lines are the record of how
// the display name came to be what it is.
func RegisterParty(corpusRoot string, id, name string) error {
	if id == "" {
		return fmt.Errorf("attest: a party needs an id")
	}
	if name == "" {
		return fmt.Errorf("attest: a party needs a display name")
	}
	b, err := json.Marshal(Party{ID: id, Name: name})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(PartyPath(corpusRoot), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// Parties reads a corpus's registry. A corpus with no registered parties has
// no file, and that is the normal starting state, not an error.
//
// As with the verdict log, a malformed line is an error: these are the first
// identities in the system, and quietly dropping the line nobody could parse
// is how a party's display name silently becomes somebody else's.
func Parties(corpusRoot string) ([]Party, error) {
	f, err := os.Open(PartyPath(corpusRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Party
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var p Party
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, fmt.Errorf("attest: %s line %d: %w", partyFileName, n, err)
		}
		if p.ID == "" || p.Name == "" {
			return nil, fmt.Errorf("attest: %s line %d: a party needs an id and a display name", partyFileName, n)
		}
		out = append(out, p)
	}
	return out, sc.Err()
}
