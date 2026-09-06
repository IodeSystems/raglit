package raglit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// fieldsChatter answers the extraction ask and records what it was sent.
type fieldsChatter struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	reply   string
}

func (c *fieldsChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	c.mu.Lock()
	c.calls++
	for _, m := range msgs {
		for _, p := range m.Parts {
			c.prompts = append(c.prompts, p.Text)
		}
	}
	reply := c.reply
	c.mu.Unlock()
	return streamReply(reply), nil
}

// resolvedAs records an identity that resolved as a type, so a fields job has
// something to extract against.
func resolvedAs(t *testing.T, s *Store, path, docType string) {
	t.Helper()
	if err := s.SetDocumentIdentity(context.Background(), path, DocIdentity{
		Name: "A caption for " + path, Summary: "A summary long enough to be a real one.",
		Kind: "commercial", Source: IdentityByMachine, Model: "m", At: 1,
		ContentTags: []string{"repair order"}, RoleTags: []string{"reference"},
		DocType: docType,
	}); err != nil {
		t.Fatal(err)
	}
}

const workOrderReply = `{"order_number":"RO-04471","customer":"Ardley","total":318.4}`

func TestExtractFields_FillsTheSchemaAndIndexesIt(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	doc := "/corpus/scan_000.pdf"
	registerWorkOrder(t, s)
	resolvedAs(t, s, doc, "work order")
	c := &fieldsChatter{reply: workOrderReply}
	s.SetIdentifier(NewIdentifier(c, "m"))

	got, err := s.ExtractFields(ctx, doc, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "work order" || got.Source != IdentityByMachine {
		t.Fatalf("fields = %+v", got)
	}
	var fields map[string]any
	if err := json.Unmarshal(got.Fields, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["order_number"] != "RO-04471" {
		t.Errorf("order_number = %v", fields["order_number"])
	}
	// The type's own reading instructions must reach the prompt — a schema
	// without them produces a confidently filled-in form.
	if !strings.Contains(c.prompts[0], "repair order") {
		t.Errorf("the type's prompt did not reach the ask:\n%s", c.prompts[0])
	}

	// Indexed, or the extraction is a record nothing can find. A reference
	// number appears nowhere the ordinary transcript ranks it usefully.
	hits, err := s.Search("RO-04471", 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, h := range hits {
		if h.Origin == fragOriginFields && h.Path == doc {
			found = true
		}
	}
	if !found {
		t.Errorf("the extraction is not searchable: %d hit(s)", len(hits))
	}

	// A second run is not more work.
	if _, err := s.ExtractFields(ctx, doc, false); !errors.Is(err, ErrIdentityKept) {
		t.Errorf("re-extract = %v, want ErrIdentityKept", err)
	}
}

func TestExtractFields_RefusesADocumentThatIsNotOne(t *testing.T) {
	s := storeWithDocs(t, 1)
	doc := "/corpus/scan_000.pdf"
	resolvedAs(t, s, doc, "") // captioned, but not one of the types
	s.SetIdentifier(NewIdentifier(&fieldsChatter{reply: workOrderReply}, "m"))
	if _, err := s.ExtractFields(context.Background(), doc, false); !errors.Is(err, ErrNoDocType) {
		t.Fatalf("err = %v, want ErrNoDocType", err)
	}
}

// A model handed a schema will sometimes answer WITH the schema. That
// validates cleanly against required-keys checking, so it has to be caught.
func TestExtractFields_RefusesTheSchemaEchoedBack(t *testing.T) {
	s := storeWithDocs(t, 1)
	doc := "/corpus/scan_000.pdf"
	registerWorkOrder(t, s)
	resolvedAs(t, s, doc, "work order")
	c := &fieldsChatter{reply: workOrderSchema}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if _, err := s.ExtractFields(context.Background(), doc, false); err == nil {
		t.Fatal("the schema was accepted as its own values")
	}
	if c.calls < 2 {
		t.Errorf("an echoed schema did not re-prompt: %d call(s)", c.calls)
	}
}

func TestRecordFields_APersonsExtractionIsNeverRegenerated(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	doc := "/corpus/scan_000.pdf"
	registerWorkOrder(t, s)
	resolvedAs(t, s, doc, "work order")
	mine, err := s.RecordFields(ctx, doc,
		DocFields{Fields: json.RawMessage(`{"order_number":"RO-4471-A"}`)}, "carl")
	if err != nil {
		t.Fatal(err)
	}
	if !mine.ByPerson() || mine.Type != "work order" {
		t.Fatalf("recorded = %+v", mine)
	}
	// Forced, and it still refuses: a person's extraction is not a machine's to
	// replace, which is the rule a person's caption already follows.
	s.SetIdentifier(NewIdentifier(&fieldsChatter{reply: workOrderReply}, "m"))
	if _, err := s.ExtractFields(ctx, doc, true); !errors.Is(err, ErrIdentityKept) {
		t.Errorf("forced re-extract over a person's = %v", err)
	}
}

// The fields fragment is what makes an extraction findable, and it must be a
// flattened "label: value" text rather than the JSON: a lexical index over
// {"po_number":"4471"} ranks the punctuation and the word "number".
func TestFieldsFragmentText_FlattensAndSkipsWhatIsAbsent(t *testing.T) {
	got := fieldsFragmentText(DocFields{Type: "work order", Fields: json.RawMessage(
		`{"order_number":"RO-04471","customer":null,"total":318.4,` +
			`"parts":[{"sku":"AC-19","qty":2}],"notes":"  "}`)})
	for _, want := range []string{"TYPE: work order", "order number: RO-04471",
		"total: 318.4", "parts 1 sku: AC-19", "parts 1 qty: 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("fragment missing %q:\n%s", want, got)
		}
	}
	// A field the document did not state is not a fact and must not be indexed
	// as one — neither a null nor an empty string.
	if strings.Contains(got, "customer") || strings.Contains(got, "notes") {
		t.Errorf("an absent field was indexed:\n%s", got)
	}
}

// The queue serves all three asks. A fields job must extract against the type
// the document resolved as, and leave the caption alone.
func TestIdentityWorker_FieldsJobExtractsAgainstTheResolvedType(t *testing.T) {
	s := storeWithDocs(t, 2)
	ctx := context.Background()
	registerWorkOrder(t, s)
	resolvedAs(t, s, "/corpus/scan_000.pdf", "work order")
	resolvedAs(t, s, "/corpus/scan_001.pdf", "") // not a form; must be left alone
	before, err := s.DocumentIdentity("/corpus/scan_000.pdf")
	if err != nil {
		t.Fatal(err)
	}

	c := &fieldsChatter{reply: workOrderReply}
	s.SetIdentifier(NewIdentifier(c, "m"))
	n, err := s.EnqueueMissingFields(false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("queued %d — only the document that resolved as a type is work", n)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := s.DocumentFields("/corpus/scan_000.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "work order" || !strings.Contains(string(got.Fields), "RO-04471") {
		t.Errorf("fields = %+v", got)
	}
	// The caption is not what a fields job writes.
	after, err := s.DocumentIdentity("/corpus/scan_000.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name || after.At != before.At {
		t.Errorf("the extraction changed the caption: %+v (was %+v)", after, before)
	}

	cov, err := s.FieldsCoverage()
	if err != nil {
		t.Fatal(err)
	}
	if len(cov) != 1 || cov[0].Resolved != 1 || cov[0].Extracted != 1 {
		t.Errorf("coverage = %+v", cov)
	}
}

// The fields fragment is a record ABOUT the document, like the caption — not
// the document's own words, like a photograph's description. Getting that wrong
// puts the extraction into get_document's text and into the fragment count,
// which is the exact trap indextext.go records for 'described'.
func TestFieldsFragment_IsNotTheDocumentsOwnWords(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	doc := "/corpus/scan_000.pdf"
	registerWorkOrder(t, s)
	resolvedAs(t, s, doc, "work order")
	before, err := s.Documents()
	if err != nil {
		t.Fatal(err)
	}
	s.SetIdentifier(NewIdentifier(&fieldsChatter{reply: workOrderReply}, "m"))
	if _, err := s.ExtractFields(ctx, doc, false); err != nil {
		t.Fatal(err)
	}

	after, err := s.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Fragments != before[0].Fragments {
		t.Errorf("the extraction counted as a fragment: %d → %d",
			before[0].Fragments, after[0].Fragments)
	}
	content, err := s.DocText(doc, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content.Text, "RO-04471") {
		t.Errorf("the extraction came back as the document's own text:\n%s", content.Text)
	}
	// And the captioning path must not read its own extraction back either.
	idText, err := s.IdentityText(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(idText, "RO-04471") {
		t.Errorf("identity would re-read its own extraction:\n%s", idText)
	}
}

// A schema is edited and every extraction already made answers the OLD
// questions — carrying the right type name and a plausible record, with the new
// field simply absent, which reads exactly like a document that did not state
// one. Nothing but the recorded hash can tell the difference.
func TestFieldsStaleness_ASchemaEditInvalidatesWhatWasReadUnderIt(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	doc := "/corpus/scan_000.pdf"
	dt := registerWorkOrder(t, s)
	resolvedAs(t, s, doc, "work order")
	s.SetIdentifier(NewIdentifier(&fieldsChatter{reply: workOrderReply}, "m"))
	if _, err := s.ExtractFields(ctx, doc, false); err != nil {
		t.Fatal(err)
	}
	stale, err := s.FieldsStaleness()
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("a fresh extraction is stale: %+v", stale)
	}

	// A cosmetic re-registration is NOT an edit: same questions, same answers,
	// and re-extracting a corpus for a reformatted schema is a bill for nothing.
	same := dt
	same.Schema = json.RawMessage("  " + workOrderSchema + "\n")
	if err := s.SetDocType(same); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FieldsStaleness(); len(got) != 0 {
		t.Errorf("a reformatted schema read as changed: %+v", got)
	}
	// Nor is a rename: it does not change what was asked of the document.
	renamed := dt
	renamed.Name = "WORK ORDER"
	if err := s.SetDocType(renamed); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FieldsStaleness(); len(got) != 0 {
		t.Errorf("a rename read as a schema change: %+v", got)
	}

	// Adding a field IS.
	edited := dt
	edited.Schema = json.RawMessage(`{"type":"object","properties":{` +
		`"order_number":{"type":"string"},"customer":{"type":"string"},` +
		`"total":{"type":"number"},"technician":{"type":"string"}},` +
		`"required":["order_number"]}`)
	if err := s.SetDocType(edited); err != nil {
		t.Fatal(err)
	}
	stale, err = s.FieldsStaleness()
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].Reason != FieldsSchemaMoved {
		t.Fatalf("stale = %+v", stale)
	}

	// And so is a change to the READING INSTRUCTIONS alone — the prompt is
	// where "that column is the customer's, not a duplicate" lives, and an
	// extraction made without it is as wrong as one made against fewer fields.
	edited.Prompt = "The technician's initials are bottom left, not the customer's."
	if err := s.SetDocType(edited); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FieldsStaleness(); len(got) != 1 {
		t.Errorf("a prompt edit did not invalidate: %+v", got)
	}

	// Owed work, so a plain sweep re-runs it — no --force needed. Declining it
	// would leave a record that looks right and answers nobody's questions.
	owed, err := s.ExtractableMissing()
	if err != nil {
		t.Fatal(err)
	}
	if len(owed) != 1 || owed[0] != doc {
		t.Fatalf("owed = %v", owed)
	}
	c := &fieldsChatter{reply: `{"order_number":"RO-04471","technician":"JM"}`}
	s.SetIdentifier(NewIdentifier(c, "m"))
	got, err := s.ExtractFields(ctx, doc, false)
	if err != nil {
		t.Fatalf("a stale extraction was declined: %v", err)
	}
	if !strings.Contains(string(got.Fields), "JM") {
		t.Errorf("fields = %s", got.Fields)
	}
	if after, _ := s.FieldsStaleness(); len(after) != 0 {
		t.Errorf("still stale after re-running: %+v", after)
	}
	// And now it is done again.
	if _, err := s.ExtractFields(ctx, doc, false); !errors.Is(err, ErrIdentityKept) {
		t.Errorf("re-extract of a current record = %v", err)
	}
}

// A person's extraction is theirs. A schema edit does not make what they wrote
// wrong, and re-running over it would discard a ruling.
func TestFieldsStaleness_APersonsExtractionIsNeverStale(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	doc := "/corpus/scan_000.pdf"
	dt := registerWorkOrder(t, s)
	resolvedAs(t, s, doc, "work order")
	if _, err := s.RecordFields(ctx, doc,
		DocFields{Fields: json.RawMessage(`{"order_number":"RO-4471-A"}`)}, "carl"); err != nil {
		t.Fatal(err)
	}
	dt.Prompt = "changed"
	if err := s.SetDocType(dt); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FieldsStaleness(); len(got) != 0 {
		t.Errorf("a person's extraction went stale: %+v", got)
	}
	owed, _ := s.ExtractableMissing()
	if len(owed) != 0 {
		t.Errorf("a person's extraction is owed a re-run: %v", owed)
	}
}

// The other three reasons an extraction stops being current.
func TestFieldsStaleness_TheOtherReasons(t *testing.T) {
	newStore := func(t *testing.T) (*Store, string) {
		s := storeWithDocs(t, 1)
		doc := "/corpus/scan_000.pdf"
		registerWorkOrder(t, s)
		resolvedAs(t, s, doc, "work order")
		s.SetIdentifier(NewIdentifier(&fieldsChatter{reply: workOrderReply}, "m"))
		if _, err := s.ExtractFields(context.Background(), doc, false); err != nil {
			t.Fatal(err)
		}
		return s, doc
	}

	// The type is gone. Reported, but NOT re-queued: there is nothing to extract
	// against, and a permanent no-op would read as outstanding work forever.
	s, _ := newStore(t)
	if err := s.DeleteDocType("work order"); err != nil {
		t.Fatal(err)
	}
	got, err := s.FieldsStaleness()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Reason != FieldsTypeGone {
		t.Fatalf("stale = %+v", got)
	}
	if owed, _ := s.ExtractableMissing(); len(owed) != 0 {
		t.Errorf("a removed type was queued for re-extraction: %v", owed)
	}

	// The document now resolves as a DIFFERENT type.
	s2, doc2 := newStore(t)
	if err := s2.SetDocType(DocType{Name: "invoice", Schema: json.RawMessage(workOrderSchema)}); err != nil {
		t.Fatal(err)
	}
	resolvedAs(t, s2, doc2, "invoice")
	got2, err := s2.FieldsStaleness()
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 || got2[0].Reason != FieldsTypeChanged {
		t.Fatalf("stale = %+v", got2)
	}

	// The transcript moved under it — the same rule a caption follows.
	s3, doc3 := newStore(t)
	if err := s3.Ingest(context.Background(), Document{
		Path: doc3, Title: "scan_000.pdf",
		Fragments: []Fragment{{Page: 1, Ord: 0, Text: psaText + " And a corrected clause, added on re-reading."}},
	}); err != nil {
		t.Fatal(err)
	}
	got3, err := s3.FieldsStaleness()
	if err != nil {
		t.Fatal(err)
	}
	if len(got3) != 1 || got3[0].Reason != FieldsTextMoved {
		t.Fatalf("stale = %+v", got3)
	}
}

func TestFieldsCoverage_CountsStaleSeparately(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	doc := "/corpus/scan_000.pdf"
	dt := registerWorkOrder(t, s)
	resolvedAs(t, s, doc, "work order")
	s.SetIdentifier(NewIdentifier(&fieldsChatter{reply: workOrderReply}, "m"))
	if _, err := s.ExtractFields(ctx, doc, false); err != nil {
		t.Fatal(err)
	}
	dt.Prompt = "changed"
	if err := s.SetDocType(dt); err != nil {
		t.Fatal(err)
	}
	cov, err := s.FieldsCoverage()
	if err != nil {
		t.Fatal(err)
	}
	// "1 of 1 extracted" over a schema edited a moment ago is a coverage report
	// that lies, so the stale count is its own column.
	if len(cov) != 1 || cov[0].Extracted != 1 || cov[0].Stale != 1 {
		t.Fatalf("coverage = %+v", cov)
	}
}

// seqChatter answers a scripted sequence, under a lock, and keeps every
// prompt in order — so a test can assert WHICH ask came first.
type seqChatter struct {
	mu      sync.Mutex
	replies []string
	calls   int
	prompts []string
}

func (c *seqChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	c.mu.Lock()
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
	reply := c.replies[i]
	c.mu.Unlock()
	return streamReply(reply), nil
}

// A caption is what establishes a document's TYPE, so an extraction must run
// after it, not beside it. The queue holds one row per document, which is the
// sequencing: the worker chains the extraction on as the caption closes.
func TestIdentityWorker_ACaptionIsFollowedByTheExtractionItEstablishes(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	doc := "/corpus/scan_000.pdf"
	registerWorkOrder(t, s)

	// One chatter answering BOTH asks in order: the caption resolves the type,
	// then the extraction fills out that type's schema.
	c := &seqChatter{replies: []string{
		`{"name":"2021 repair order 4471 (Ardley)","summary":"A repair order for the vehicle, listing parts and labour at sufficient length to be real.","kind":"commercial","content_tags":["repair order","vehicle service","parts billing"],"role_tags":["reference"],"doc_type":"work order"}`,
		workOrderReply,
	}}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if _, err := s.EnqueueIdentity(doc, false); err != nil {
		t.Fatal(err)
	}
	// One drain. The extraction is queued by the worker as the caption closes,
	// so it is picked up in the same pass rather than needing a second sweep.
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}

	id, err := s.DocumentIdentity(doc)
	if err != nil {
		t.Fatal(err)
	}
	if id.DocType != "work order" {
		t.Fatalf("doc_type = %q", id.DocType)
	}
	f, err := s.DocumentFields(doc)
	if err != nil {
		t.Fatal(err)
	}
	if f.Empty() || !strings.Contains(string(f.Fields), "RO-04471") {
		t.Fatalf("the extraction did not follow the caption: %+v", f)
	}
	// Order, not just arrival: the extraction ask must have been made SECOND,
	// or it read against a type the caption had not yet established.
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.prompts) != 2 {
		t.Fatalf("%d ask(s), want 2", len(c.prompts))
	}
	if !strings.Contains(c.prompts[0], "cataloguing a document") {
		t.Errorf("the first ask was not the caption:\n%s", c.prompts[0])
	}
	if !strings.Contains(c.prompts[1], "fill out the fields") {
		t.Errorf("the second ask was not the extraction:\n%s", c.prompts[1])
	}

	// And it settles: nothing is owed, so a second drain asks nothing.
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if len(c.prompts) != 2 {
		t.Errorf("the chain did not settle: %d ask(s)", len(c.prompts))
	}
}

// A document that resolves as NO type must not chain into an extraction — most
// documents are not forms, and a queued no-op per document is a bill for it.
func TestIdentityWorker_ACaptionWithNoTypeChainsNothing(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	registerWorkOrder(t, s)
	c := &seqChatter{replies: []string{
		`{"name":"1994 surveyor letter re fence line","summary":"A letter from the surveyor about the fence line, dated 1994, sent to the county at length.","kind":"correspondence","content_tags":["fence line","boundary survey","county filing"],"role_tags":["reference"],"doc_type":""}`,
	}}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if _, err := s.EnqueueIdentity("/corpus/scan_000.pdf", false); err != nil {
		t.Fatal(err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.prompts) != 1 {
		t.Errorf("a document that is not a form was asked %d times", len(c.prompts))
	}
}

// commitDoc re-arms a caption when the transcript moves under it. The row is
// one per path, so a document whose last job was an EXTRACTION would be revived
// as one — leaving the caption it exists to refresh untouched, and nothing
// saying so.
func TestCommitDoc_ReArmsTheCaptionEvenAfterAnExtraction(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	const doc = "/corpus/psa.pdf"
	commit := func(text string) {
		t.Helper()
		if err := s.commitDoc(doc, "psa.pdf", "llm-seg", "r",
			[]stagedFrag{{page: 1, ord: 0, text: text}}, nil, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	registerWorkOrder(t, s)
	const caption = `{"name":"2021 repair order 4471 (Ardley)","summary":"A repair order for the vehicle, listing parts and labour at sufficient length to be real.","kind":"commercial","content_tags":["repair order","vehicle service","parts billing"],"role_tags":["reference"],"doc_type":"work order"}`
	// Caption, extraction, and the same pair again after the transcript moves.
	c := &seqChatter{replies: []string{caption, workOrderReply, caption, workOrderReply}}
	s.SetIdentifier(NewIdentifier(c, "m"))

	commit(psaText)
	if _, err := (&IdentityWorker{Store: s, Slots: 1}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	// The document is captioned AND extracted, so the queue's one row for it now
	// says 'fields'.
	if f, ferr := s.DocumentFields(doc); ferr != nil || f.Empty() {
		t.Fatalf("fields = %+v, %v", f, ferr)
	}
	jobs, err := s.IdentityJobs("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Mode != IdentityAskFields {
		t.Fatalf("queue = %+v", jobs)
	}

	// Now the transcript moves under both.
	commit(psaText + " And a clause corrected on re-reading, added by a second pass.")
	jobs, err = s.IdentityJobs("pending", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("nothing was re-armed when the transcript moved: %+v", jobs)
	}
	// The row is one per path, so this revived whatever the document's last job
	// was. Left implicit, that is an EXTRACTION — and the caption this re-arm
	// exists to refresh would never be re-asked, with nothing saying so.
	if jobs[0].Mode != IdentityAskFull {
		t.Fatalf("the caption re-arm queued mode %q", jobs[0].Mode)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 1}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Caption, extraction, caption again, extraction again: the caption is
	// re-asked first, and the extraction follows it rather than racing it.
	if len(c.prompts) != 4 {
		t.Fatalf("%d ask(s), want 4", len(c.prompts))
	}
	if !strings.Contains(c.prompts[2], "cataloguing a document") {
		t.Errorf("the re-arm did not re-ask the caption:\n%s", c.prompts[2])
	}
	if !strings.Contains(c.prompts[3], "fill out the fields") {
		t.Errorf("the extraction did not follow it:\n%s", c.prompts[3])
	}
}
