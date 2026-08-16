package raglit

import (
	"context"
	"strings"
	"testing"
)

// A search result must say how far it can be relied on.
//
// A ranked list presents every row in the same shape, and that shape reads as
// "the document says this". caselit binds those rows to elements and DERIVES
// backing from the bind, so a row that is a model's account of a picture and a
// row transcribed off a court order arrive indistinguishable at the one moment
// the difference decides something.

// The case Origin cannot express, and the one that actually occurs.
//
// Origin marks a fragment only when EVERY page it touches is ≥90% description.
// The delano survey correction measures 88%: its whole indexed text is a model's
// account of a map, down to which annotation arrow is which colour, and it comes
// back with Origin empty, reading as the record.
func TestHitTrust_TheEightyEightPercentSurveyMapIsCaveated(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const doc = "file:///corpus/survey-correction.pdf"
	if err := s.Ingest(ctx, Document{Path: doc, Title: "survey-correction.pdf", Fragments: []Fragment{
		{Text: "Survey map showing Parcel A, B, C and various lot lines with bearings and distances. " +
			"Includes handwritten annotations: 'Incorrect' (blue arrow), 'Correct' (pink arrow)."},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReading(Reading{
		SourceSHA256: "surveysha", SourcePath: doc, DocPath: doc,
		Method: MethodVision, Level: ReadingMachine, DescribedPct: 88,
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := s.SearchPath("parcel", "", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %d hits err=%v", len(hits), err)
	}
	h := hits[0]
	if h.Origin != "" {
		t.Fatal("fixture no longer covers the case — Origin already marks it")
	}
	if h.Trust == nil {
		t.Fatal("a hit carries no trust, so nothing distinguishes it from a transcribed order")
	}
	if h.Trust.DescribedPct != 88 || h.Trust.Method != MethodVision {
		t.Fatalf("trust %+v", h.Trust)
	}
	cav := h.Trust.Caveat()
	if !strings.Contains(cav, "88%") || !strings.Contains(cav, "describ") {
		t.Fatalf("caveat %q says nothing useful before a quotation", cav)
	}
	// Both claims are reported: the transcription is real AND partly a model's.
	if _, ok := h.Trust.Facets[FacetSubject]; !ok {
		t.Fatal("no subject claim on a document that is 88% description")
	}
	if _, ok := h.Trust.Facets[FacetText]; !ok {
		t.Fatal("the text claim vanished — a mixed document's transcription is real")
	}
}

// A caveat on every row is a caveat on none.
func TestHitTrust_AnOrdinaryTranscriptionIsSilent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const doc = "file:///corpus/order.pdf"
	if err := s.Ingest(ctx, Document{Path: doc, Title: "order.pdf",
		Fragments: []Fragment{{Text: "IT IS HEREBY ORDERED that the motion to continue is granted."}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReading(Reading{
		SourceSHA256: "ordersha", SourcePath: doc, DocPath: doc,
		Method: MethodTextLayer, Level: ReadingMachine, DescribedPct: 0,
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchPath("ordered", "", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %d hits err=%v", len(hits), err)
	}
	if got := hits[0].Trust.Caveat(); got != "" {
		t.Fatalf("caveat %q — a transcribed order needs no caution, and one on every row is one on none", got)
	}
	if hits[0].Trust.Facets[FacetText] != TrustExact {
		t.Fatal("a text layer is the document's own bytes")
	}
}

// No reading is NOT a clean bill of health, and the absence has to say so —
// otherwise the least-examined documents look the safest.
func TestHitTrust_AnUnreadDocumentSaysSoRatherThanStayingSilent(t *testing.T) {
	s := testStore(t)
	if err := s.Ingest(context.Background(), Document{
		Path: "file:///corpus/loose.txt", Title: "loose.txt",
		Fragments: []Fragment{{Text: "the surveyor set the corners on Thursday"}}}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchPath("surveyor", "", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %d hits err=%v", len(hits), err)
	}
	if hits[0].Trust != nil {
		t.Fatal("fixture has a reading; it must not")
	}
	// A nil receiver answers, deliberately — a renderer that forgot the nil check
	// would otherwise print nothing for exactly these rows.
	if got := hits[0].Trust.Caveat(); !strings.Contains(got, "never recorded") {
		t.Fatalf("caveat %q — an unrecorded read must not read as a reliable one", got)
	}
}

// An unmeasured read is distinct from a measured-clean one, all the way to the
// caveat a person sees.
func TestHitTrust_UnmeasuredIsNotClean(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const doc = "file:///corpus/oldread.pdf"
	if err := s.Ingest(ctx, Document{Path: doc, Title: "oldread.pdf",
		Fragments: []Fragment{{Text: "a phone screen showing a message thread about the surveyor"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReading(Reading{
		SourceSHA256: "oldsha", SourcePath: doc, DocPath: doc,
		Method: MethodVision, Level: ReadingMachine, DescribedPct: DescribedUnmeasured,
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchPath("surveyor", "", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %d hits err=%v", len(hits), err)
	}
	if got := hits[0].Trust.Caveat(); !strings.Contains(got, "never measured") {
		t.Fatalf("caveat %q — unmeasured must not render as measured-clean", got)
	}
}

// What a person ruled on travels with the hit, because that is the only thing
// that raises a machine claim to something quotable.
func TestHitTrust_ARulingReachesTheHit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const doc = "file:///corpus/hearing.mp4"
	if err := s.Ingest(ctx, Document{Path: doc, Title: "hearing.mp4",
		Fragments: []Fragment{{Text: "THE COURT: the petition is denied and dismissed."}}}); err != nil {
		t.Fatal(err)
	}
	r := Reading{
		SourceSHA256: "hearsha", SourcePath: doc, DocPath: doc,
		Method: MethodASR, Level: ReadingAttested, RuledBy: "carl",
	}.WithRuling(FacetSpeaker, TrustRuled)
	if err := s.RecordReading(r); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchPath("petition", "", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %d hits err=%v", len(hits), err)
	}
	tr := hits[0].Trust
	if tr == nil || tr.RuledBy != "carl" || tr.Level != ReadingAttested {
		t.Fatalf("trust %+v — who ruled and how far is the whole point", tr)
	}
	// Attribution was ruled on; the WORDS were not, and both have to show.
	if tr.Facets[FacetSpeaker] != TrustRuled {
		t.Fatalf("speaker %d — a person went through the attribution", tr.Facets[FacetSpeaker])
	}
	if tr.Facets[FacetText] != TrustASRWords {
		t.Fatalf("text %d — the words are still the recogniser's", tr.Facets[FacetText])
	}
}
