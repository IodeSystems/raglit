package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// indexWithADocument is a throwaway index holding one badly-named document.
func indexWithADocument(t *testing.T) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "idx.sqlite")
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Ingest(context.Background(), raglit.Document{
		Path: "/corpus/0428_001.pdf", Title: "0428_001.pdf",
		Fragments: []raglit.Fragment{{Page: 1, Ord: 0, Text: "Signed by both parties on the date first written above."}},
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

// The list has to show the documents with NO name too. A coverage report that
// lists only what exists reads as complete no matter how little of it there is.
func TestIdentifyList_NamesWhatIsNotNamed(t *testing.T) {
	db := indexWithADocument(t)
	out := captureStdout(t, func() {
		if err := runIdentify([]string{"--db", db, "--list"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "0 of 1 document(s) named") {
		t.Errorf("coverage line missing:\n%s", out)
	}
	if !strings.Contains(out, "has not been established") {
		t.Errorf("an unnamed document was not reported as unnamed:\n%s", out)
	}
}

// A person recording an identity, and it sticking: the caption is stored, the
// filename is untouched, and the ruling is attributed.
func TestIdentifyByAPerson(t *testing.T) {
	db := indexWithADocument(t)
	err := runIdentify([]string{"--db", db,
		"--name", "2021-05-25 purchase and sale agreement (Ardley/Brannock)",
		"--summary", "The executed counterpart of the buyer's offer for 24053 North Northlea Road.",
		"--kind", "agreement", "--by", "carl", "/corpus/0428_001.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.DocumentIdentity("/corpus/0428_001.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !id.ByPerson() || id.Kind != "agreement" || !strings.HasPrefix(id.Name, "2021-05-25") {
		t.Fatalf("identity = %+v", id)
	}
	docs, err := st.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if docs[0].Path != "/corpus/0428_001.pdf" || docs[0].Title != "0428_001.pdf" {
		t.Errorf("the file was renamed: %+v — the path is what every citation joins on", docs[0])
	}
}

// An invented kind is refused rather than stored: the vocabulary is closed, and
// a fortieth spelling of "letter" is a kind nobody can filter on.
func TestIdentifyByAPerson_RefusesAnUnknownKind(t *testing.T) {
	db := indexWithADocument(t)
	err := runIdentify([]string{"--db", db, "--name", "Something", "--kind", "thingamajig", "/corpus/0428_001.pdf"})
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("err = %v, want a complaint about the kind", err)
	}
}

// Without a model there is nothing to generate with, and the command has to say
// that rather than reporting that it identified nothing.
func TestIdentify_WithoutAModelSaysSo(t *testing.T) {
	db := indexWithADocument(t)
	t.Setenv("RAGLIT_LLM_KEY", "")
	// An empty home, so the answer comes from the flags rather than from
	// whatever this machine happens to have configured.
	err := runIdentify([]string{"--db", db, "--home", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "no identity model") {
		t.Fatalf("err = %v, want 'no identity model configured'", err)
	}
}
