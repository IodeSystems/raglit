package raglit

import (
	"context"
	"strings"
	"testing"
)

// tagged records an identity carrying tags, for a digest to count.
func tagged(t *testing.T, s *Store, path, kind string, content, roles []string) {
	t.Helper()
	if err := s.SetDocumentIdentity(context.Background(), path, DocIdentity{
		Name:    "A caption for " + path,
		Summary: "A document held in this corpus, summarised at sufficient length to be real.",
		Kind:    kind, Source: IdentityByMachine, Model: "m", At: 1,
		ContentTags: content, RoleTags: roles,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexDigest_CountsWhatTheIndexHolds(t *testing.T) {
	s := storeWithDocs(t, 3)
	tagged(t, s, "/corpus/scan_000.pdf", "agreement",
		[]string{"lead paint", "escrow closing"}, []string{"reference"})
	tagged(t, s, "/corpus/scan_001.pdf", "agreement",
		[]string{"lead paint", "boundary survey"}, []string{"reference", "report"})
	// scan_002 stays untagged — an index is normally partway through.

	d, err := s.IndexDigest()
	if err != nil {
		t.Fatal(err)
	}
	if d.Documents != 3 || d.Untagged != 1 {
		t.Fatalf("digest = %d documents, %d untagged", d.Documents, d.Untagged)
	}
	if len(d.Content) == 0 || d.Content[0].Tag != "lead paint" || d.Content[0].Count != 2 {
		t.Errorf("content = %+v — want lead paint(2) first", d.Content)
	}
	if len(d.Kinds) != 1 || d.Kinds[0].Tag != "agreement" || d.Kinds[0].Count != 2 {
		t.Errorf("kinds = %+v", d.Kinds)
	}
	if len(d.Roles) != 2 || d.Roles[0].Tag != "reference" {
		t.Errorf("roles = %+v", d.Roles)
	}
	if got := TagLine(d.Content); !strings.Contains(got, "lead paint(2)") {
		t.Errorf("TagLine = %q", got)
	}
}

// A digest is what makes an empty search legible, and search can be scoped to a
// subtree — so the digest must be scopable the same way. One that reports the
// whole index for a path-scoped query claims coverage the subtree does not have.
func TestIndexDigest_ScopesToTheSameSubtreeAsTheSearch(t *testing.T) {
	s := storeWithDocs(t, 2)
	tagged(t, s, "/corpus/scan_000.pdf", "deed", []string{"lead paint"}, []string{"reference"})
	tagged(t, s, "/corpus/scan_001.pdf", "survey", []string{"boundary survey"}, []string{"report"})

	d, err := s.IndexDigestFor("/corpus/scan_001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Documents != 1 {
		t.Fatalf("digest under the subtree = %d documents, want 1", d.Documents)
	}
	for _, c := range d.Content {
		if c.Tag == "lead paint" {
			t.Errorf("a subtree digest reported a tag from outside it: %+v", d.Content)
		}
	}
}

func TestTagContext_IsTheIndexsEstablishedVocabulary(t *testing.T) {
	s := storeWithDocs(t, 2)
	if got := s.TagContext(); got != "" {
		t.Errorf("a fresh index has no vocabulary, got %q", got)
	}
	tagged(t, s, "/corpus/scan_000.pdf", "deed", []string{"lead paint"}, []string{"reference"})
	tagged(t, s, "/corpus/scan_001.pdf", "deed", []string{"lead paint", "escrow"}, []string{"reference"})
	got := s.TagContext()
	if !strings.Contains(got, "lead paint(2)") || !strings.Contains(got, "escrow(1)") {
		t.Errorf("TagContext = %q", got)
	}
}

func TestIndexAbout_IsStaleWhenTheCorpusMovesUnderIt(t *testing.T) {
	s := storeWithDocs(t, 2)
	tagged(t, s, "/corpus/scan_000.pdf", "deed", []string{"lead paint"}, []string{"reference"})
	if err := s.SetMeta(metaIndexAbout, "A small corpus about one property.", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(metaIndexAboutDocs, "2", 1); err != nil {
		t.Fatal(err)
	}
	about, stale, err := s.IndexAbout()
	if err != nil || about == "" || stale {
		t.Fatalf("about = %q stale=%v err=%v — want the paragraph, current", about, stale, err)
	}
	// The same paragraph over an index that has since tripled describes a
	// different corpus, and saying so is the whole point of the stamp.
	if err := s.SetMeta(metaIndexAboutDocs, "1", 1); err != nil {
		t.Fatal(err)
	}
	if _, stale, _ = s.IndexAbout(); !stale {
		t.Error("an About written from half the documents is not reported stale")
	}
}
