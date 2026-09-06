package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

const testWOSchema = `{"type":"object","properties":{` +
	`"order_number":{"type":"string"},"customer":{"type":"string"},` +
	`"total":{"type":"number"}},"required":["order_number"]}`

// indexWithAWorkOrder is a throwaway index holding one document that has
// resolved as a registered type, and one that has not.
func indexWithAWorkOrder(t *testing.T) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "idx.sqlite")
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SetDocType(raglit.DocType{
		Name: "work order", Description: "a garage repair order",
		Prompt: "The RO number is top right.", Schema: json.RawMessage(testWOSchema),
	}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct{ path, typ string }{
		{"/c/ro-4471.pdf", "work order"},
		{"/c/letter.pdf", ""},
	} {
		if err := st.Ingest(ctx, raglit.Document{
			Path: d.path, Title: filepath.Base(d.path),
			Fragments: []raglit.Fragment{{Page: 1, Ord: 0, Text: "Signed by both parties on the date first written above."}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetDocumentIdentity(ctx, d.path, raglit.DocIdentity{
			Name: "Caption for " + d.path, Summary: "A summary long enough to be a real one.",
			Kind: "commercial", Source: "machine", Model: "m", At: 1,
			ContentTags: []string{"repair order"}, RoleTags: []string{"reference"},
			DocType: d.typ,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestHint_RoundTripsAndSaysWhatItCosts(t *testing.T) {
	db := filepath.Join(t.TempDir(), "idx.sqlite")
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	out := captureStdout(t, func() {
		if err := runHint([]string{"--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "no hint") {
		t.Errorf("an index with no hint did not say so:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runHint([]string{"--db", db, "--set", "RO means repair order."}); err != nil {
			t.Fatal(err)
		}
	})
	// The hint is part of the reading recipe, so setting it does NOT re-read
	// what is already indexed. Not saying so leaves somebody believing a corpus
	// was reinterpreted when it was not.
	if !strings.Contains(out, "reading recipe") || !strings.Contains(out, "Re-ingest") {
		t.Errorf("setting a hint did not say what it does and does not do:\n%s", out)
	}
	out = captureStdout(t, func() {
		if err := runHint([]string{"--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "RO means repair order.") {
		t.Errorf("hint did not round trip:\n%s", out)
	}
	if err := runHint([]string{"--db", db, "--clear"}); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		if err := runHint([]string{"--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "no hint") {
		t.Errorf("--clear left a hint:\n%s", out)
	}
}

func TestDocTypeList_ReportsCoverageAndFields(t *testing.T) {
	db := indexWithAWorkOrder(t)
	out := captureStdout(t, func() {
		if err := runDocType([]string{"list", "--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "work order") || !strings.Contains(out, "1 resolved") {
		t.Errorf("coverage missing:\n%s", out)
	}
	if !strings.Contains(out, "order_number") {
		t.Errorf("fields missing:\n%s", out)
	}
}

func TestDocTypeAdd_RegistersFromAFileAndRefusesTheWrongArgOrder(t *testing.T) {
	db := indexWithAWorkOrder(t)
	f := filepath.Join(t.TempDir(), "invoice.json")
	if err := os.WriteFile(f, []byte(`{"description":"a bill","prompt":"read it",
	  "schema":`+testWOSchema+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runDocType([]string{"add", "--db", db, "--file", f, "invoice"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "registered \"invoice\"") {
		t.Errorf("not registered:\n%s", out)
	}
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DocTypeByName("invoice"); err != nil {
		t.Error(err)
	}

	// Go's flag package stops parsing at the first non-flag argument, so
	// `add <name> --file f` silently drops the flag. Refused with the usage,
	// rather than registering a type with no schema.
	err = runDocType([]string{"add", "--db", db, "invoice2", "--file", f})
	if err == nil || !strings.Contains(err.Error(), "--file <F> <NAME>") {
		t.Errorf("flag-after-name = %v, want the usage", err)
	}
}

// Registering a changed schema invalidates every extraction made under the old
// one, and this is the moment somebody can act on it — nothing else in the
// output would ever mention it.
func TestDocTypeAdd_SaysWhatItJustInvalidated(t *testing.T) {
	db := indexWithAWorkOrder(t)
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dt, err := st.DocTypeByName("work order")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.DocText("/c/ro-4471.pdf", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// An extraction stamped as CURRENT under the type as it stands.
	if err := st.SetDocumentFields(ctx, "/c/ro-4471.pdf", raglit.DocFields{
		Type: "work order", Source: "machine", Model: "m", At: 1,
		Fields:   json.RawMessage(`{"order_number":"RO-04471"}`),
		TypeHash: dt.Hash(), TextHash: raglit.IdentityTextHash(c.Text),
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	f := filepath.Join(t.TempDir(), "wo2.json")
	if err := os.WriteFile(f, []byte(`{"description":"a garage repair order",
	  "prompt":"The RO number is top right. Technician initials bottom left.",
	  "schema":{"type":"object","properties":{"order_number":{"type":"string"},
	    "technician":{"type":"string"}},"required":["order_number"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runDocType([]string{"add", "--db", db, "--file", f, "work order"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "1 extraction(s) answer the PREVIOUS schema") {
		t.Errorf("the edit did not report what it invalidated:\n%s", out)
	}
}

func TestDocTypeRm_KeepsTheRecordsMadeUnderIt(t *testing.T) {
	db := indexWithAWorkOrder(t)
	out := captureStdout(t, func() {
		if err := runDocType([]string{"rm", "--db", db, "work order"}); err != nil {
			t.Fatal(err)
		}
	})
	// Removing a type is a statement about the index's vocabulary, not a
	// retraction of what documents said.
	if !strings.Contains(out, "are kept") {
		t.Errorf("removal did not say the extractions survive:\n%s", out)
	}
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DocTypeByName("work order"); err == nil {
		t.Error("the type survived rm")
	}
}

func TestDocType_UnknownSubcommandSaysWhatThereIs(t *testing.T) {
	err := runDocType([]string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "list | show | propose | add | rm") {
		t.Errorf("err = %v", err)
	}
}
