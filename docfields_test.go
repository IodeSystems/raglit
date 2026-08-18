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
		DocType:     docType,
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
