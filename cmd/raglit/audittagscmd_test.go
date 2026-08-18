package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// indexWithTaggedDocuments is a throwaway index whose documents carry the drift
// an audit exists to surface: two spellings of one thing.
func indexWithTaggedDocuments(t *testing.T) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "idx.sqlite")
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	docs := []struct {
		path    string
		content []string
	}{
		{"/corpus/a.pdf", []string{"lead paint", "escrow closing"}},
		{"/corpus/b.pdf", []string{"lead paint inspection", "boundary survey"}},
		{"/corpus/c.pdf", nil},
	}
	for _, d := range docs {
		if err := st.Ingest(ctx, raglit.Document{
			Path: d.path, Title: filepath.Base(d.path),
			Fragments: []raglit.Fragment{{Page: 1, Ord: 0, Text: "Signed by both parties on the date first written above."}},
		}); err != nil {
			t.Fatal(err)
		}
		if d.content == nil {
			continue
		}
		if err := st.SetDocumentIdentity(ctx, d.path, raglit.DocIdentity{
			Name: "A caption for " + d.path, Summary: "A summary long enough to be a real one.",
			Kind: "agreement", Source: "machine", Model: "m", At: 1,
			ContentTags: d.content, RoleTags: []string{"reference"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestAuditTags_ReportsTheVocabularyAndProposesDrift(t *testing.T) {
	db := indexWithTaggedDocuments(t)
	out := captureStdout(t, func() {
		if err := runAuditTags([]string{"--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "lead paint") || !strings.Contains(out, "≈") {
		t.Errorf("no near-duplicate proposal:\n%s", out)
	}
	// The untagged document is named as work, not omitted — a report that lists
	// only what exists reads as complete however little of it there is.
	if !strings.Contains(out, "1 untagged") {
		t.Errorf("untagged documents not reported:\n%s", out)
	}
	// A proposal must not read as a finding.
	if !strings.Contains(out, "PROPOSAL") {
		t.Errorf("the ≈ list is not marked as a proposal:\n%s", out)
	}
}

func TestAuditTags_MergeAppliesOnlyWhatAPersonNamed(t *testing.T) {
	db := indexWithTaggedDocuments(t)
	out := captureStdout(t, func() {
		if err := runAuditTags([]string{"--db", db,
			"--merge", "lead paint inspection=>lead paint"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "1 document(s)") {
		t.Errorf("merge did not report what it did:\n%s", out)
	}
	st, err := raglit.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d, err := st.IndexDigestFor("", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range d.Content {
		if c.Tag == "lead paint inspection" {
			t.Errorf("the merged tag survives: %+v", d.Content)
		}
		if c.Tag == "lead paint" && c.Count != 2 {
			t.Errorf("lead paint = %d documents, want 2", c.Count)
		}
	}
	// The tags nobody named are untouched.
	if !hasTag(d.Content, "boundary survey") || !hasTag(d.Content, "escrow closing") {
		t.Errorf("a merge touched tags it was not given: %+v", d.Content)
	}
}

func hasTag(tags []raglit.TagCount, want string) bool {
	for _, t := range tags {
		if t.Tag == want {
			return true
		}
	}
	return false
}

func TestAbout_ShowsTheCountedDigestWithoutAModel(t *testing.T) {
	db := indexWithTaggedDocuments(t)
	out := captureStdout(t, func() {
		if err := runAbout([]string{"--db", db}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "3 document(s)") || !strings.Contains(out, "1 untagged") {
		t.Errorf("counts missing:\n%s", out)
	}
	if !strings.Contains(out, "lead paint") || !strings.Contains(out, "agreement") {
		t.Errorf("tags/kinds missing:\n%s", out)
	}
	// No paragraph has been written, and the command says so rather than
	// printing an empty section that reads as "nothing to say about it".
	if !strings.Contains(out, "no summary written yet") {
		t.Errorf("the absent paragraph is not reported:\n%s", out)
	}
}
