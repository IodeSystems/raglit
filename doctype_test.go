package raglit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// workOrderSchema is a small, realistic field schema for the tests.
const workOrderSchema = `{"type":"object","properties":{` +
	`"order_number":{"type":"string","description":"the RO number, top right"},` +
	`"customer":{"type":"string"},` +
	`"total":{"type":"number"}},"required":["order_number"]}`

func registerWorkOrder(t *testing.T, s *Store) DocType {
	t.Helper()
	dt := DocType{
		Name:        "Work Order",
		Description: "a garage's repair order: RO number top right, parts in the margin",
		Prompt:      "\"RO\" means repair order. The customer's copy is the second column.",
		Schema:      json.RawMessage(workOrderSchema),
	}
	if err := s.SetDocType(dt); err != nil {
		t.Fatal(err)
	}
	return dt
}

func TestDocType_RegistersUnderANormalisedName(t *testing.T) {
	s := storeWithDocs(t, 1)
	registerWorkOrder(t, s)
	// "Work Order" and "work order" are one type. Two would each hold half the
	// corpus, and neither would be findable by the other's name.
	got, err := s.DocTypeByName("WORK   order")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "work order" {
		t.Errorf("name = %q", got.Name)
	}
	if f := got.FieldNames(); len(f) != 3 || f[0] != "customer" {
		t.Errorf("fields = %v", f)
	}
	if _, err := s.DocTypeByName("invoice"); err == nil {
		t.Error("an unregistered type resolved")
	}
}

func TestSetDocType_RefusesASchemaThatIsNotOne(t *testing.T) {
	s := storeWithDocs(t, 1)
	for _, bad := range []string{``, `not json`, `{"type":"string"}`, `{"type":"object"}`} {
		err := s.SetDocType(DocType{Name: "x", Schema: json.RawMessage(bad)})
		if err == nil {
			t.Errorf("schema %q was accepted", bad)
		}
	}
}

// A document type's names are the index's, so the doc_type the identity call
// may answer with is not known until the call — and an answer outside them is
// refused, because it would resolve to no schema.
func TestIdentify_ResolvesADocumentTypeOrNone(t *testing.T) {
	types := []DocType{{Name: "work order", Description: "a garage's repair order"}}

	c := &identityChatter{replies: []string{
		`{"name":"2021-05-25 repair order 4471 (Ardley)","summary":"A repair order for the vehicle, dated May 2021, listing parts and labour at sufficient length.","kind":"commercial","content_tags":["repair order","vehicle service","parts billing"],"role_tags":["reference"],"doc_type":"Work Order"}`,
	}}
	id, err := NewIdentifier(c, "m").Identify(context.Background(),
		IdentityAsk{Text: psaText, DocTypes: types})
	if err != nil {
		t.Fatal(err)
	}
	if id.DocType != "work order" {
		t.Errorf("doc_type = %q — want it normalised onto the registered name", id.DocType)
	}
	// The registered types and their descriptions must reach the prompt, or the
	// model is being asked to pick from a list it was not shown.
	if !strings.Contains(c.prompts[0], "a garage's repair order") {
		t.Errorf("the type list is not in the prompt:\n%s", c.prompts[0])
	}

	// A type nobody registered is refused and re-prompted, not recorded: a
	// document carrying it would claim a type nothing can extract.
	c2 := &identityChatter{replies: []string{
		`{"name":"A repair order for the vehicle","summary":"A repair order for the vehicle, dated May 2021, listing parts and labour at sufficient length.","kind":"commercial","content_tags":["repair order","vehicle service","parts billing"],"role_tags":["reference"],"doc_type":"invoice"}`,
		`{"name":"A repair order for the vehicle","summary":"A repair order for the vehicle, dated May 2021, listing parts and labour at sufficient length.","kind":"commercial","content_tags":["repair order","vehicle service","parts billing"],"role_tags":["reference"],"doc_type":""}`,
	}}
	id2, err := NewIdentifier(c2, "m").Identify(context.Background(),
		IdentityAsk{Text: psaText, DocTypes: types})
	if err != nil {
		t.Fatal(err)
	}
	if id2.DocType != "" {
		t.Errorf("doc_type = %q — want none", id2.DocType)
	}
	if len(c2.prompts) != 2 {
		t.Errorf("an unregistered type did not re-prompt: %d call(s)", len(c2.prompts))
	}

	// An index with no registered types is not asked the question at all.
	c3 := &identityChatter{replies: []string{
		`{"name":"A repair order for the vehicle","summary":"A repair order for the vehicle, dated May 2021, listing parts and labour at sufficient length.","kind":"commercial","content_tags":["repair order","vehicle service","parts billing"],"role_tags":["reference"]}`,
	}}
	if _, err := NewIdentifier(c3, "m").Identify(context.Background(), IdentityAsk{Text: psaText}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c3.prompts[0], "doc_type") {
		t.Errorf("an index with no types was asked for one:\n%s", c3.prompts[0])
	}
}

// The hint is the corpus owner's account of how to read the collection. It must
// reach every ask, or the one it misses is the one that misreads.
func TestIndexHint_ReachesEveryAsk(t *testing.T) {
	const hint = "RO means repair order, not received."
	c := &identityChatter{replies: []string{
		`{"name":"2021-05-25 repair order 4471 (Ardley)","summary":"A repair order for the vehicle, dated May 2021, listing parts and labour at sufficient length.","kind":"commercial","content_tags":["repair order","vehicle service","parts billing"],"role_tags":["reference"]}`,
	}}
	if _, err := NewIdentifier(c, "m").Identify(context.Background(),
		IdentityAsk{Text: psaText, IndexHint: hint}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.prompts[0], hint) {
		t.Errorf("identity prompt carries no hint:\n%s", c.prompts[0])
	}

	ct := &identityChatter{replies: []string{
		`{"content_tags":["repair order","vehicle service","parts billing"],"role_tags":["reference"]}`,
	}}
	if _, _, err := NewIdentifier(ct, "m").IdentifyTags(context.Background(),
		IdentityAsk{Text: psaText, IndexHint: hint}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ct.prompts[0], hint) {
		t.Errorf("tags prompt carries no hint:\n%s", ct.prompts[0])
	}

	cf := &identityChatter{replies: []string{`{"order_number":"RO-04471","customer":"Ardley","total":318.4}`}}
	dt := DocType{Name: "work order", Schema: json.RawMessage(workOrderSchema)}
	if _, err := NewIdentifier(cf, "m").ExtractFields(context.Background(), psaText, dt, hint); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cf.prompts[0], hint) {
		t.Errorf("extraction prompt carries no hint:\n%s", cf.prompts[0])
	}
}

func TestIndexHint_RoundTripsAndIsBlankWhenUnset(t *testing.T) {
	s := storeWithDocs(t, 1)
	if got := s.IndexHint(); got != "" {
		t.Errorf("a fresh index has a hint: %q", got)
	}
	if got := HintBlock(""); got != "" {
		t.Errorf("an empty hint rendered a block: %q", got)
	}
	if err := s.SetIndexHint("  RO means repair order.  ", 1); err != nil {
		t.Fatal(err)
	}
	if got := s.IndexHint(); got != "RO means repair order." {
		t.Errorf("hint = %q", got)
	}
}

// propChatter answers the type-proposal ask.
type propChatter struct {
	prompts []string
	reply   string
}

func (c *propChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	for _, m := range msgs {
		for _, p := range m.Parts {
			c.prompts = append(c.prompts, p.Text)
		}
	}
	return streamReply(c.reply), nil
}

// The authoring step: a person points at documents that ARE one, and the model
// proposes the schema and the reading instructions.
func TestProposeDocType_ReadsGoldDocumentsAndProposesASchema(t *testing.T) {
	s := storeWithDocs(t, 2)
	if err := s.SetIndexHint("RO means repair order.", 1); err != nil {
		t.Fatal(err)
	}
	c := &propChatter{reply: `{"description":"a garage's repair order",` +
		`"prompt":"The RO number is top right.",` +
		`"schema":` + workOrderSchema + `}`}
	s.SetIdentifier(NewIdentifier(c, "m"))

	gold := []string{"/corpus/scan_000.pdf", "/corpus/scan_001.pdf"}
	got, err := s.ProposeDocType(context.Background(), "Work Order", gold)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "work order" || got.Description == "" || len(got.FieldNames()) != 3 {
		t.Errorf("proposal = %+v", got)
	}
	// Both examples reach the prompt: a schema proposed from one document is a
	// transcription of that document's quirks.
	if !strings.Contains(c.prompts[0], "EXAMPLE 1") || !strings.Contains(c.prompts[0], "EXAMPLE 2") {
		t.Errorf("not every gold document reached the prompt:\n%s", c.prompts[0])
	}
	if !strings.Contains(c.prompts[0], "RO means repair order.") {
		t.Errorf("the corpus hint did not reach the proposal:\n%s", c.prompts[0])
	}
	// The gold documents are kept, so a revision can be judged against the same
	// examples rather than whatever is at hand.
	if len(got.Gold) != 2 {
		t.Errorf("gold = %v", got.Gold)
	}
	// Proposed, NOT registered: a schema nobody read before it started filling
	// in records is a schema nobody will trust afterwards.
	if _, err := s.DocTypeByName("work order"); err == nil {
		t.Error("the proposal registered itself")
	}
}

func TestProposeDocType_RefusesAProposalWithNoUsableSchema(t *testing.T) {
	s := storeWithDocs(t, 1)
	c := &propChatter{reply: `{"description":"a thing","prompt":"read it","schema":{"type":"string"}}`}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if _, err := s.ProposeDocType(context.Background(), "x", []string{"/corpus/scan_000.pdf"}); err == nil {
		t.Fatal("a non-object schema was accepted")
	}
}
