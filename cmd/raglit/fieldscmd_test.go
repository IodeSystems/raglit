package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// currentExtraction writes a machine extraction stamped as current under the
// type and text as they stand.
func currentExtraction(t *testing.T, db, path, fields string) {
	t.Helper()
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	dt, err := st.DocTypeByName("work order")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.DocText(path, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetDocumentFields(ctx, path, raglit.DocFields{
		Type: "work order", Source: "machine", Model: "m", At: 1,
		Fields:   json.RawMessage(fields),
		TypeHash: dt.Hash(), TextHash: raglit.IdentityTextHash(c.Text),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFieldsList_ReportsCoverageAndNamesWhatIsNotCurrent(t *testing.T) {
	db := indexWithAWorkOrder(t)
	currentExtraction(t, db, "/c/ro-4471.pdf", `{"order_number":"RO-04471"}`)

	out := captureStdout(t, func() {
		if err := runFields([]string{"--list", "--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "1 resolved") || !strings.Contains(out, "1 extracted") {
		t.Errorf("coverage missing:\n%s", out)
	}
	if strings.Contains(out, "stale") {
		t.Errorf("a current extraction was reported stale:\n%s", out)
	}

	// Edit the schema; the same extraction now answers the previous questions.
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	dt, _ := st.DocTypeByName("work order")
	dt.Prompt = "Technician initials are bottom left."
	if err := st.SetDocType(dt); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out = captureStdout(t, func() {
		if err := runFields([]string{"--list", "--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	// Counted apart, and NAMED with the reason: a stale extraction reads as a
	// complete record, so the reason is the only thing that says why it is
	// being re-run.
	if !strings.Contains(out, "(1 stale)") {
		t.Errorf("stale not counted apart:\n%s", out)
	}
	if !strings.Contains(out, "the schema changed under it") || !strings.Contains(out, "/c/ro-4471.pdf") {
		t.Errorf("stale not named with its reason:\n%s", out)
	}
}

func TestFieldsDryRun_NamesWhatIsOwedAndNothingElse(t *testing.T) {
	db := indexWithAWorkOrder(t)
	// Nothing extracted yet: the one document that resolved as a type is owed.
	out := captureStdout(t, func() {
		if err := runFields([]string{"--dry-run", "--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "/c/ro-4471.pdf") || !strings.Contains(out, "1 document") {
		t.Errorf("owed work missing:\n%s", out)
	}
	// The document that resolved as NO type is not work. Most documents in most
	// corpora are not forms.
	if strings.Contains(out, "/c/letter.pdf") {
		t.Errorf("a document that is not a form was queued:\n%s", out)
	}

	currentExtraction(t, db, "/c/ro-4471.pdf", `{"order_number":"RO-04471"}`)
	out = captureStdout(t, func() {
		if err := runFields([]string{"--dry-run", "--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "nothing to extract") {
		t.Errorf("a current corpus still reported work:\n%s", out)
	}
}

// "Nothing owed" and "nothing wrong" are different sentences. An extraction
// whose type was removed cannot be re-run, and saying everything is current
// would bury it.
func TestFieldsDryRun_SaysWhenSomethingIsStuckRatherThanCurrent(t *testing.T) {
	db := indexWithAWorkOrder(t)
	currentExtraction(t, db, "/c/ro-4471.pdf", `{"order_number":"RO-04471"}`)
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDocType("work order"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out := captureStdout(t, func() {
		if err := runFields([]string{"--dry-run", "--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "no longer registered") {
		t.Errorf("a stuck extraction was reported as current:\n%s", out)
	}
}

func TestFieldsSetAndRead_APersonsRulingRoundTrips(t *testing.T) {
	db := indexWithAWorkOrder(t)
	out := captureStdout(t, func() {
		if err := runFields([]string{"--db", db, "--set",
			`{"order_number":"RO-4471-A","customer":"Ardley"}`, "--by", "carl", "ro-4471"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "recorded by carl") || !strings.Contains(out, "RO-4471-A") {
		t.Errorf("a person's ruling was not recorded:\n%s", out)
	}
	// The type is taken from what the document RESOLVED as — a person recording
	// fields for the first time should not have to restate it.
	if !strings.Contains(out, "work order") {
		t.Errorf("the resolved type was not carried:\n%s", out)
	}

	// Reading it back.
	out = captureStdout(t, func() {
		if err := runFields([]string{"--db", db, "ro-4471"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "RO-4471-A") {
		t.Errorf("fields did not read back:\n%s", out)
	}

	// And a person's is never owed a machine re-run, whatever the schema does.
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	dt, _ := st.DocTypeByName("work order")
	dt.Prompt = "changed"
	if err := st.SetDocType(dt); err != nil {
		t.Fatal(err)
	}
	st.Close()
	out = captureStdout(t, func() {
		if err := runFields([]string{"--dry-run", "--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "/c/ro-4471.pdf") {
		t.Errorf("a person's extraction was queued for a re-run:\n%s", out)
	}
}

func TestFieldsList_SaysSoWhenNoDocumentResolvedAsAType(t *testing.T) {
	db := indexWithADocument(t)
	out := captureStdout(t, func() {
		if err := runFields([]string{"--list", "--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "no document resolved as a registered type") {
		t.Errorf("an index with no types did not say so:\n%s", out)
	}
}
