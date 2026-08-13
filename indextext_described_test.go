package raglit

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The real thing chandra returns for a photograph, captured from the delano
// corpus: one Image region, an <img alt> and prose, and not one word of it
// written by anybody.
const photoDescription = `<div data-bbox="0 0 1000 1000" data-label="Image"><img alt="A red Chevrolet Malibu SUV parked on a gravel lot next to an asphalt road. The car has its rear hatch open and a license plate that reads 'CEP0912'. In the background, there are trees, a white pickup truck, and a house under a clear blue sky."/>A photograph of a red Chevrolet Malibu SUV parked on a gravel lot. The car is facing left, with its rear hatch open. The license plate is visible and reads 'CEP0912'. To the left of the gravel lot is an asphalt road where a white pickup truck is driving away. In the background, there are several trees, including a large evergreen and a weeping willow, and a house. The sky is clear blue.</div>`

// And the real thing it returns for a fax cover sheet that is ALSO a .jpg —
// the case that must not be marked, because every word of it is on the page.
const faxTranscription = `<div data-bbox="100 90 900 200" data-label="Text"><p>LISSER & ASSOCIATES, PLLC</p><p>320 Milwaukee Street, PO Box 1109</p><p>Mount Vernon, Washington 98273-5461</p></div>` +
	`<div data-bbox="100 210 900 400" data-label="Text"><p>DATE May 24, 2022</p><p>FACSIMILE TRANSMITTAL</p><p>To: Lawrence and Michele McKinnon</p><p>Re: Record of Survey</p></div>`

func TestDescribedFraction_TellsADescriptionFromATranscription(t *testing.T) {
	if got := DescribedFraction(photoDescription); got < 0.99 {
		t.Fatalf("a photograph scored %.2f — it is description end to end", got)
	}
	if !IsDescribedPage(photoDescription) {
		t.Fatal("a photograph was not recognised as described")
	}
	// The file kind says nothing: this one is a .jpg too, and it is a real
	// transcription of text that is on the page.
	if got := DescribedFraction(faxTranscription); got != 0 {
		t.Fatalf("a transcribed fax cover scored %.2f, want 0", got)
	}
	if IsDescribedPage(faxTranscription) {
		t.Fatal("transcribed text was marked as generated — that is its own kind of lie")
	}
}

// A scanned page carrying a figure is MOSTLY real. Marking the whole fragment
// generated would misrepresent the transcription around it, so the threshold is
// deliberately high and this case stays unmarked.
func TestDescribedFraction_APageWithAFigureIsStillATranscription(t *testing.T) {
	page := faxTranscription +
		`<div data-bbox="100 500 900 800" data-label="Image"><img alt="A survey diagram showing parcel boundaries."/></div>`
	f := DescribedFraction(page)
	if f == 0 {
		t.Fatal("the figure contributed nothing — alt text is indexed and must be counted")
	}
	if IsDescribedPage(page) {
		t.Fatalf("a transcribed page with one figure scored %.2f and was marked described", f)
	}
}

func TestDescribedFraction_PlainTextIsNeverDescribed(t *testing.T) {
	for _, s := range []string{"", "just some indexed prose", "a < b and c > d"} {
		if got := DescribedFraction(s); got != 0 {
			t.Fatalf("DescribedFraction(%q) = %v, want 0", s, got)
		}
	}
}

// Guards the fixture itself: if the captured output ever stops being what the
// model actually returns, the tests above are measuring a museum piece.
func TestDescribedFraction_FixtureMatchesTheCapturedOutput(t *testing.T) {
	b, err := os.ReadFile("testdata/photo-description.txt")
	if err != nil {
		t.Skip("no captured fixture")
	}
	if !IsDescribedPage(string(b)) {
		t.Fatal("the captured live output is not recognised as described")
	}
}

// A described fragment is the document's CONTENT; an identity fragment is a
// caption about it. Anything filtering by origin must keep the first and drop
// the second, or a photograph becomes an empty document.
//
// Six queries were written as `origin = ''` before descriptions existed, and
// each changed meaning the moment they did. Measured on the delano photo sets:
// 17 photographs reported zero fragments in the document list while search
// returned them perfectly well — indexed, searchable, and listed exactly like
// the no-fragments failure they were not.
func TestDescribedFragmentCountsAsTheDocumentsOwnContent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// A photograph, as the pipeline stores one: its only fragment is a model's
	// description of it.
	if err := s.Ingest(ctx, Document{
		Path: "file:///corpus/driveway.jpg", Title: "driveway.jpg",
		Fragments: []Fragment{{Text: "A red Chevrolet Malibu parked on a gravel lot, licence plate CEP0912."}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`UPDATE fragments SET origin = ? WHERE doc_id = (SELECT id FROM documents WHERE path = ?)`,
		FragOriginDescribed, "file:///corpus/driveway.jpg"); err != nil {
		t.Fatal(err)
	}

	docs, err := s.Documents()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range docs {
		if d.Path == "file:///corpus/driveway.jpg" {
			found = true
			if d.Fragments != 1 {
				t.Fatalf("a photograph lists %d fragments — its description IS its content", d.Fragments)
			}
		}
	}
	if !found {
		t.Fatal("the photograph is not in the document list at all")
	}

	// And it is not reported as the indexed-but-unsearchable failure.
	probs, err := s.Problems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		if p.Kind == ProblemNoFragments && p.Subject == "file:///corpus/driveway.jpg" {
			t.Fatal("a searchable photograph was reported as having no fragments")
		}
	}

	// get_document must return it: a photograph whose text is withheld is a
	// document nothing can read.
	txt, err := s.DocText("file:///corpus/driveway.jpg", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt.Text, "CEP0912") {
		t.Fatalf("DocText dropped the description: %q", txt.Text)
	}
}
