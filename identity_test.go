package raglit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// identityChatter answers with the next canned reply per call, so a fix loop can
// be driven from a test.
type identityChatter struct {
	replies []string
	calls   int
	prompts []string
}

func (c *identityChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		for _, p := range m.Parts {
			b.WriteString(p.Text)
		}
	}
	c.prompts = append(c.prompts, b.String())
	i := c.calls
	c.calls++
	if i >= len(c.replies) {
		i = len(c.replies) - 1
	}
	return streamReply(c.replies[i]), nil
}

// A body long enough to be worth identifying, and named nothing like what it is
// — the case this whole feature exists for.
const psaText = `PURCHASE AND SALE AGREEMENT dated May 25, 2021 between ARDLEY, buyer,
and BRANNOCK, seller, for the property at 24053 North Northlea Road. The buyer
agrees to purchase the described parcel for the sum stated in Section 2, closing
on or before July 1, 2021, with title to be conveyed by statutory warranty deed.`

func TestIdentify_ReturnsCaptionSummaryKind(t *testing.T) {
	c := &identityChatter{replies: []string{
		`{"name":"2021-05-25 Form 21 purchase and sale agreement (Ardley/Brannock)",
		  "summary":"The buyer's signed offer for 24053 North Northlea Road, dated May 25 2021, between Ardley and Brannock, closing July 1 2021.",
		  "kind":"agreement",
		  "content_tags":["purchase agreement","property transfer","escrow closing"],
		  "role_tags":["reference"]}`,
	}}
	id, err := NewIdentifier(c, "test-model").Identify(context.Background(), psaText, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(id.Name, "purchase and sale agreement") {
		t.Errorf("name = %q", id.Name)
	}
	if id.Kind != "agreement" || id.Source != IdentityByMachine || id.Model != "test-model" || id.At == 0 {
		t.Errorf("identity = %+v", id)
	}
	if len(id.ContentTags) != 3 || id.ContentTags[0] != "purchase agreement" {
		t.Errorf("content_tags = %v", id.ContentTags)
	}
	if len(id.RoleTags) != 1 || id.RoleTags[0] != "reference" {
		t.Errorf("role_tags = %v", id.RoleTags)
	}
	// The filename is deliberately never sent: a model handed "Lead-Based
	// Paint.pdf" hedges toward it, which reproduces the failure being fixed.
	if strings.Contains(strings.ToLower(c.prompts[0]), ".pdf") {
		t.Error("the prompt carried a filename")
	}
}

func TestIdentify_FixLoopOnAnInventedKind(t *testing.T) {
	c := &identityChatter{replies: []string{
		`{"name":"A letter about the fence","summary":"A letter from the surveyor about the fence line, dated 1994, sent to the county.","kind":"missive",
		  "content_tags":["fence line","surveyor correspondence","property boundary"],
		  "role_tags":["reference"]}`,
		`{"name":"1994 surveyor letter re fence line","summary":"A letter from the surveyor about the fence line, dated 1994, sent to the county.","kind":"correspondence",
		  "content_tags":["fence line","surveyor correspondence","property boundary"],
		  "role_tags":["reference"]}`,
	}}
	id, err := NewIdentifier(c, "m").Identify(context.Background(), psaText, "")
	if err != nil {
		t.Fatal(err)
	}
	if id.Kind != "correspondence" {
		t.Errorf("kind = %q", id.Kind)
	}
	if c.calls != 2 {
		t.Errorf("calls = %d, want 2 (the first answer should have been refused)", c.calls)
	}
	if !strings.Contains(c.prompts[1], "kind") {
		t.Error("the retry did not tell the model what was wrong")
	}
}

func TestIdentify_ErrorsRatherThanGuessing(t *testing.T) {
	// A model that will not produce a usable answer must yield NO identity. A
	// wrong caption stated with a machine's confidence is worse than none.
	c := &identityChatter{replies: []string{`{"name":"","summary":"","kind":"","content_tags":[],"role_tags":[]}`}}
	if _, err := NewIdentifier(c, "m").Identify(context.Background(), psaText, ""); err == nil {
		t.Fatal("want an error, got an identity")
	}
	var short *ErrIdentityTooShort
	if _, err := NewIdentifier(c, "m").Identify(context.Background(), "hi", ""); !errors.As(err, &short) {
		t.Fatalf("short document: err = %v, want ErrIdentityTooShort", err)
	}
}

func TestNormalizeKind(t *testing.T) {
	for in, want := range map[string]string{
		"deed": "deed", "Court Filing": "court filing", "letter": "correspondence",
		"E-Mail": "correspondence", "contract": "agreement", "plat": "survey",
		"REPORT.": "analysis", "other": "other",
	} {
		got, ok := NormalizeKind(in)
		if !ok || got != want {
			t.Errorf("NormalizeKind(%q) = %q,%v; want %q", in, got, ok, want)
		}
	}
	if _, ok := NormalizeKind("thingamajig"); ok {
		t.Error("an invented kind was accepted — the vocabulary is supposed to be closed")
	}
}

func TestIdentityExcerpt_MarksTheGap(t *testing.T) {
	long := strings.Repeat("a ", identityHeadChars) + "MIDDLE" + strings.Repeat("z ", identityTailChars)
	got := identityExcerpt(long)
	if len(got) >= len(long) {
		t.Fatal("a long document was not bounded")
	}
	if !strings.Contains(got, "…the middle of this document is not shown…") {
		t.Error("the cut was not marked — the model reads across it as continuous prose")
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "z") {
		t.Error("the tail was dropped; a document that is several things stapled together needs its end")
	}
	if short := "a short instrument"; identityExcerpt(short) != short {
		t.Error("a short document should pass through whole")
	}
}

// storeWithDoc is an in-memory index holding one two-page document.
func storeWithDoc(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Ingest(context.Background(), Document{
		Path: "/corpus/0428_001.pdf", Title: "0428_001.pdf",
		Fragments: []Fragment{{Page: 1, Ord: 0, Text: psaText}, {Page: 2, Ord: 0, Text: "Signature page."}},
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func machineIdentity() DocIdentity {
	return DocIdentity{
		Name:    "2021-05-25 Form 21 purchase and sale agreement (Ardley/Brannock)",
		Summary: "The executed purchase and sale agreement for 24053 North Northlea Road.",
		Kind:    "agreement", Source: IdentityByMachine, Model: "m", At: 1,
		ContentTags: []string{"purchase agreement", "property transfer", "escrow closing"},
		RoleTags:    []string{"reference"},
	}
}

func TestIdentity_IndexedButNotPartOfTheDocument(t *testing.T) {
	s := storeWithDoc(t)
	ctx := context.Background()
	if err := s.SetDocumentIdentity(ctx, "/corpus/0428_001.pdf", machineIdentity()); err != nil {
		t.Fatal(err)
	}

	// Findable BY the summary: the body never says "Form 21".
	hits, err := s.Search("Form 21", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1 (the summary should be searchable)", len(hits))
	}
	if hits[0].Origin != fragOriginIdentity || !hits[0].IsDescription() {
		t.Errorf("hit origin = %q — a hit on a paraphrase must say so", hits[0].Origin)
	}
	if !strings.Contains(hits[0].Text, "NOT the document's own words") {
		t.Error("the fragment text does not carry its own provenance")
	}

	// But NOT part of the document's text, on any path that reassembles it.
	c, err := s.DocText("/corpus/0428_001.pdf", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Text, "Form 21") || len(c.Pages) != 2 {
		t.Errorf("DocText picked up the identity fragment: pages=%d text=%q", len(c.Pages), c.Text)
	}
	tp, err := s.TruePages("/corpus/0428_001.pdf")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tp {
		if strings.Contains(p.Text, "Form 21") {
			t.Errorf("TruePages picked up the identity fragment on page %d", p.Page)
		}
	}
	docs, err := s.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if docs[0].Fragments != 2 {
		t.Errorf("fragment count = %d, want 2 — the caption is not one of the document's fragments", docs[0].Fragments)
	}
	if docs[0].GenName == "" || docs[0].GenKind != "agreement" {
		t.Errorf("document list does not carry the caption: %+v", docs[0])
	}
}

func TestSetDocumentIdentity_ReplacesRatherThanAccumulates(t *testing.T) {
	s := storeWithDoc(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.SetDocumentIdentity(ctx, "/corpus/0428_001.pdf", machineIdentity()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM fragments WHERE origin=?`, fragOriginIdentity).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("identity fragments = %d, want 1", n)
	}
}

func TestCommitDoc_KeepsIdentityAndItsFragmentAcrossAReingest(t *testing.T) {
	s := storeWithDoc(t)
	ctx := context.Background()
	if err := s.SetDocumentIdentity(ctx, "/corpus/0428_001.pdf", machineIdentity()); err != nil {
		t.Fatal(err)
	}
	// A re-ingest wipes the document's fragments. The caption must survive, and
	// so must the fragment that makes it searchable — columns saying one thing
	// while nothing is indexed is the half-state to avoid.
	if err := s.commitDoc("/corpus/0428_001.pdf", "0428_001.pdf", "text-overlap", "r",
		[]stagedFrag{{page: 1, ord: 0, text: "fresh text"}}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	id, err := s.DocumentIdentity("/corpus/0428_001.pdf")
	if err != nil || id.Empty() {
		t.Fatalf("identity lost on re-ingest: %+v %v", id, err)
	}
	hits, err := s.Search("Form 21", 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("summary no longer searchable after re-ingest: %d hits, %v", len(hits), err)
	}
}

func TestCommitDoc_APersonsCaptionOutranksTheMachines(t *testing.T) {
	s := storeWithDoc(t)
	ctx := context.Background()
	mine, err := s.RecordIdentity(ctx, "/corpus/0428_001.pdf",
		DocIdentity{Name: "The one Bert actually signed", Summary: "The executed counterpart, hand-checked.", Kind: "agreement"}, "carl")
	if err != nil {
		t.Fatal(err)
	}
	if !mine.ByPerson() || mine.Model != "carl" {
		t.Fatalf("recorded identity = %+v", mine)
	}
	machine := machineIdentity()
	if err := s.commitDoc("/corpus/0428_001.pdf", "0428_001.pdf", "text-overlap", "r",
		[]stagedFrag{{page: 1, ord: 0, text: "fresh"}}, nil, nil, nil, &machine); err != nil {
		t.Fatal(err)
	}
	got, err := s.DocumentIdentity("/corpus/0428_001.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != mine.Name || !got.ByPerson() {
		t.Errorf("an ingest overwrote a person's caption: %+v", got)
	}
}

func TestIdentifyDocument_KeepsWhatIsRecordedUnlessForced(t *testing.T) {
	s := storeWithDoc(t)
	ctx := context.Background()
	answer := `{"name":"A generated caption for the agreement","summary":"The purchase and sale agreement between Ardley and Brannock for North Northlea Road.","kind":"agreement",
		"content_tags":["purchase agreement","property transfer","escrow closing"],"role_tags":["reference"]}`
	c := &identityChatter{replies: []string{answer}}
	s.SetIdentifier(NewIdentifier(c, "m"))

	if _, err := s.IdentifyDocument(ctx, "/corpus/0428_001.pdf", false); err != nil {
		t.Fatal(err)
	}
	if c.calls != 1 {
		t.Fatalf("calls = %d, want 1", c.calls)
	}
	// Already captioned → no second model call.
	if _, err := s.IdentifyDocument(ctx, "/corpus/0428_001.pdf", false); !errors.Is(err, ErrIdentityKept) {
		t.Fatalf("err = %v, want ErrIdentityKept", err)
	}
	if c.calls != 1 {
		t.Errorf("calls = %d — a captioned document was re-read anyway", c.calls)
	}
	if _, err := s.IdentifyDocument(ctx, "/corpus/0428_001.pdf", true); err != nil {
		t.Fatal(err)
	}
	if c.calls != 2 {
		t.Errorf("calls = %d — --force did not re-read", c.calls)
	}
	// A person's caption is never regenerated, forced or not.
	if _, err := s.RecordIdentity(ctx, "/corpus/0428_001.pdf", DocIdentity{Name: "Mine"}, "carl"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IdentifyDocument(ctx, "/corpus/0428_001.pdf", true); !errors.Is(err, ErrIdentityKept) {
		t.Fatalf("--force overwrote a person's caption (err = %v)", err)
	}

	// And the model reads the DOCUMENT, not the caption of it.
	if strings.Contains(c.prompts[1], "NOT the document's own words") {
		t.Error("the re-read fed the previous caption back to the model")
	}
}

func TestDocumentsMissingIdentity(t *testing.T) {
	s := storeWithDoc(t)
	missing, err := s.DocumentsMissingIdentity()
	if err != nil || len(missing) != 1 {
		t.Fatalf("missing = %v, %v", missing, err)
	}
	if err := s.SetDocumentIdentity(context.Background(), "/corpus/0428_001.pdf", machineIdentity()); err != nil {
		t.Fatal(err)
	}
	missing, err = s.DocumentsMissingIdentity()
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing after captioning = %v, %v", missing, err)
	}
}

func TestPooledDoc_CarriesTheCaptionAndNotItsFragment(t *testing.T) {
	s := storeWithDoc(t)
	ctx := context.Background()
	if err := s.SetDocumentIdentity(ctx, "/corpus/0428_001.pdf", machineIdentity()); err != nil {
		t.Fatal(err)
	}
	pooled, err := s.ExportDoc("/corpus/0428_001.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if pooled.Identity == nil || pooled.Identity.Kind != "agreement" {
		t.Fatalf("pooled identity = %+v", pooled.Identity)
	}
	for _, f := range pooled.Fragments {
		if strings.Contains(f.Text, "NOT the document's own words") {
			t.Fatal("the identity fragment was pooled as ordinary document text")
		}
	}

	// Replay into a second index: the caption comes back, once.
	dst, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := dst.IngestPooled(ctx, "/corpus/0428_001.pdf", "0428_001.pdf", pooled, ""); err != nil {
		t.Fatal(err)
	}
	id, err := dst.DocumentIdentity("/corpus/0428_001.pdf")
	if err != nil || id.Name != machineIdentity().Name {
		t.Fatalf("identity did not survive the pool: %+v %v", id, err)
	}
	var n int
	if err := dst.db.QueryRow(`SELECT COUNT(*) FROM fragments WHERE origin=?`, fragOriginIdentity).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("identity fragments after reuse = %d, want 1", n)
	}
	c, err := dst.DocText("/corpus/0428_001.pdf", 0, 0, 0)
	if err != nil || strings.Contains(c.Text, "Form 21") {
		t.Errorf("pooled reuse put the caption into the document's text: %q", c.Text)
	}
}

// A caption is written from the reading IN FORCE, not from the machine's first
// attempt at it. Fragments deliberately keep the OCR text — citations index into
// them — so a corrected page lives only in page_readings, and anything asking
// what the document SAYS has to look there.
func TestIdentityText_PrefersTheCorrectedReading(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	const machineRead = "Record of survey certified by BRLICE LISSFR, certificate no. 2OO8O81O2O."
	const corrected = "Record of survey certified by Bruce Halvor, certificate number 20080818 0120."
	if err := s.Ingest(ctx, Document{Path: "/corpus/ros.pdf", Title: "ros.pdf",
		Fragments: []Fragment{
			{Page: 1, Ord: 0, Text: machineRead},
			{Page: 2, Ord: 0, Text: "Existing corners table."},
		}}); err != nil {
		t.Fatal(err)
	}

	// No ruling yet → the indexed text is what the document says.
	got, err := s.IdentityText(ctx, "/corpus/ros.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "BRLICE") {
		t.Fatalf("uncorrected document should read as indexed:\n%s", got)
	}

	if err := s.AddPageReading(ctx, PageReading{Doc: "/corpus/ros.pdf", Page: 1,
		Text: corrected, Source: "corrected", Note: "read at 150%", By: "carl"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.IdentityText(ctx, "/corpus/ros.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "BRLICE") {
		t.Errorf("a caption would be written from the reading a person ruled wrong:\n%s", got)
	}
	if !strings.Contains(got, "Bruce Halvor") || !strings.Contains(got, "20080818 0120") {
		t.Errorf("the correction did not reach the text a caption is written from:\n%s", got)
	}
	// Pages nobody ruled on are unaffected.
	if !strings.Contains(got, "Existing corners table.") {
		t.Errorf("an uncorrected page was dropped:\n%s", got)
	}
	// And the FRAGMENTS still hold the machine's words — citations index into
	// them, so correcting a reading must not move them.
	c, err := s.DocText("/corpus/ros.pdf", 0, 0, 0)
	if err != nil || !strings.Contains(c.Text, "BRLICE") {
		t.Errorf("the correction rewrote the indexed text: %v\n%s", err, c.Text)
	}
}

// The vocabulary gained a term because a corpus said it was missing one: after
// the junk was excluded, 9% of captions still landed in "other", and every one
// of them was a working file — a timeline, a witness list, a call transcript, a
// packet assembled for counsel. Those are not an absence of a kind.
func TestNormalizeKind_NotesCoversWorkProduct(t *testing.T) {
	for _, in := range []string{"notes", "timeline", "transcript", "witness list", "packet", "worklist", "log"} {
		got, ok := NormalizeKind(in)
		if !ok || got != "notes" {
			t.Errorf("NormalizeKind(%q) = %q,%v; want notes", in, got, ok)
		}
	}
	for _, in := range []string{"invoice", "work order", "receipt", "mls listing", "statement"} {
		if got, ok := NormalizeKind(in); !ok || got != "commercial" {
			t.Errorf("NormalizeKind(%q) = %q,%v; want commercial", in, got, ok)
		}
	}
	// And it did not swallow the filed kinds it sits next to.
	for in, want := range map[string]string{"deed": "deed", "letter": "correspondence", "report": "analysis"} {
		if got, _ := NormalizeKind(in); got != want {
			t.Errorf("NormalizeKind(%q) = %q; want %q", in, got, want)
		}
	}
}
