package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// usageServer answers a chat stream with a fixed usage chunk and an embeddings
// request with one vector per input. Every other route is a 404 (no /props,
// no tokenizer).
func usageServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			w.Header().Set("Content-Type", "application/json")
			var req struct {
				Input []string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			var data []string
			for i := range req.Input {
				data = append(data, fmt.Sprintf(`{"index":%d,"embedding":[1,0]}`, i))
			}
			fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(data, ","))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

type usageRec struct {
	role, model string
	u           llm.Usage
	estimated   bool
}

func drain(t *testing.T, c Chatter) {
	t.Helper()
	ch, err := c.ChatStream(context.Background(),
		[]llm.Message{{Role: "user", Parts: []llm.ContentPart{llm.TextPart("hi")}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
}

// Each role's client reports to Models.OnUsage under its own role and model;
// the segment role resolves its fallbacks first.
func TestNewWorker_OnUsageReportsPerRole(t *testing.T) {
	base := usageServer(t)
	var mu sync.Mutex
	var got []usageRec
	m := Models{
		Vision:   ModelRole{BaseURL: base, Model: "vis"},
		Segment:  ModelRole{Model: "seg"},
		Identity: ModelRole{Model: "idm"},
		Embed:    ModelRole{BaseURL: base, Model: "emb"},
		OCR:      noTesseract,
		Channels: NewChannels(),
		OnUsage: func(role, model string, u llm.Usage, estimated bool) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, usageRec{role, model, u, estimated})
		},
	}
	s := testStore(t)
	w := NewWorker(s, m, nil)
	drain(t, w.OCR.Client)
	drain(t, w.Segmenter.Client)
	drain(t, s.identity().Client)

	mu.Lock()
	defer mu.Unlock()
	want := map[string]string{"vision": "vis", "segment": "seg", "identity": "idm"}
	for _, r := range got {
		if r.role == "embed" {
			t.Errorf("NewWorker's embed limit probe reported usage: %+v", r)
			continue
		}
		if want[r.role] != r.model {
			t.Errorf("role %q reported model %q, want %q", r.role, r.model, want[r.role])
		}
		if r.u.PromptTokens != 11 || r.u.CompletionTokens != 5 || r.u.TotalTokens != 16 {
			t.Errorf("role %q usage %+v, want 11/5/16", r.role, r.u)
		}
		delete(want, r.role)
	}
	if len(want) != 0 {
		t.Errorf("roles never reported: %v (got %+v)", want, got)
	}
}

// An OnUsage already on a client is chained, not replaced; nil Models.OnUsage
// leaves the client alone.
func TestUsageHook_ChainsAndNilIsNoop(t *testing.T) {
	c := llm.NewClient("http://x", "", "mod")
	Models{}.usageHook(c, "vision", "mod")
	if c.OnUsage != nil {
		t.Fatal("nil Models.OnUsage installed a hook")
	}
	var order []string
	c.OnUsage = func(llm.Usage) { order = append(order, "prev") }
	hooked := Models{OnUsage: func(role, model string, u llm.Usage, estimated bool) {
		order = append(order, role+"/"+model)
	}}
	hooked.usageHook(c, "vision", "mod")
	c.OnUsage(llm.Usage{})
	if strings.Join(order, ",") != "prev,vision/mod" {
		t.Errorf("order %v, want prev then the Models hook", order)
	}
}

// The embed role is reported by UsageVector, as an estimate of the input size.
func TestUsageVector_ReportsEmbed(t *testing.T) {
	base := usageServer(t)
	var got []usageRec
	m := Models{
		Embed:   ModelRole{BaseURL: base, Model: "emb"},
		OnUsage: func(role, model string, u llm.Usage, estimated bool) { got = append(got, usageRec{role, model, u, estimated}) },
	}
	v := m.UsageVector(llm.NewClient(base, "", "emb"))
	if _, err := v.Embed(context.Background(), "emb", []string{strings.Repeat("a", 10)}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].role != "embed" || got[0].model != "emb" || got[0].u.PromptTokens != 3 || got[0].u.TotalTokens != 3 {
		t.Errorf("got %+v, want one embed/emb report of 3 tokens", got)
	}
	if !got[0].estimated {
		t.Error("embed estimate reported estimated=false")
	}
	// nil hook: the client comes back as it went in.
	c := llm.NewClient(base, "", "emb")
	if (Models{}).UsageVector(c) != VectorClient(c) {
		t.Error("nil OnUsage wrapped the embed client")
	}
}

// The embed calls ingest makes report through Models.Embedder: a worker from
// NewWorker, with that embedder on its store, sends "embed" usage to OnUsage.
func TestModelsEmbedder_IngestReportsEmbed(t *testing.T) {
	base := usageServer(t)
	var mu sync.Mutex
	var got []usageRec
	m := Models{
		Embed:           ModelRole{BaseURL: base, Model: "emb"},
		EmbedLimitChars: 4000,
		OCR:             noTesseract,
		Channels:        NewChannels(),
		OnUsage: func(role, model string, u llm.Usage, estimated bool) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, usageRec{role, model, u, estimated})
		},
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "auth.md")
	if err := os.WriteFile(src, []byte("Access tokens expire.\n\nThe refresh token rotates."), 0o644); err != nil {
		t.Fatal(err)
	}
	s := testStore(t)
	s.SetEmbedder(m.Embedder())
	w := NewWorker(s, m, nil)
	if _, err := s.Enqueue("file://"+src, ""); err != nil {
		t.Fatal(err)
	}
	if did, err := w.ProcessOne(context.Background()); err != nil || !did {
		t.Fatalf("ProcessOne: did=%v err=%v", did, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("ingest made no embed report")
	}
	for _, r := range got {
		if r.role != "embed" || r.model != "emb" || r.u.PromptTokens <= 0 {
			t.Errorf("report %+v, want role embed, model emb, tokens > 0", r)
		}
	}
	// No Embed model → no embedder.
	if (Models{Channels: NewChannels()}).Embedder() != nil {
		t.Error("Embedder without an Embed model is not nil")
	}
}

// A host that enforces a per-role daily token budget can apply the budget only
// to endpoint-reported counts (estimated=false) and treat embed estimates
// (estimated=true) separately. This is the composition test: it exercises both
// the UsageVector path (estimate) and the usageHook path (reported), then
// checks a budget host separates them.
func TestOnUsageEstimatedFlagDistinguishesEstimateFromReported(t *testing.T) {
	base := usageServer(t)
	var mu sync.Mutex
	var got []usageRec
	m := Models{
		Vision: ModelRole{BaseURL: base, Model: "vis"},
		Embed:  ModelRole{BaseURL: base, Model: "emb"},
		OCR:    noTesseract,
		Channels: NewChannels(),
		OnUsage: func(role, model string, u llm.Usage, estimated bool) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, usageRec{role, model, u, estimated})
		},
	}

	// 1. Endpoint-reported count: chat through the vision role (usageHook path).
	c := llm.NewClient(base, "", "vis")
	m.usageHook(c, "vision", "vis")
	drain(t, c)

	// 2. Embed estimate: embed through UsageVector (the estimate path).
	v := m.UsageVector(llm.NewClient(base, "", "emb"))
	if _, err := v.Embed(context.Background(), "emb", []string{strings.Repeat("x", 20)}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(got) != 2 {
		t.Fatalf("got %d reports, want 2 (one reported, one estimated): %+v", len(got), got)
	}

	// Find each.
	var reported, estimate *usageRec
	for i := range got {
		if got[i].estimated {
			estimate = &got[i]
		} else {
			reported = &got[i]
		}
	}

	// F69 spec 1: embed estimate is marked.
	if estimate == nil {
		t.Fatal("no report with estimated=true")
	}
	if estimate.role != "embed" {
		t.Errorf("estimated report has role %q, want embed", estimate.role)
	}
	if estimate.u.PromptTokens <= 0 {
		t.Errorf("estimated report has zero tokens: %+v", estimate.u)
	}

	// F69 spec 2: endpoint-reported count is unmarked.
	if reported == nil {
		t.Fatal("no report with estimated=false")
	}
	if reported.role != "vision" {
		t.Errorf("reported report has role %q, want vision", reported.role)
	}
	if reported.u.PromptTokens != 11 {
		t.Errorf("reported report has PromptTokens %d, want 11", reported.u.PromptTokens)
	}

	// F69 spec 3: a budget host applies its budget only to unmarked reports.
	const budget = 100
	var budgetUsed int
	for _, r := range got {
		if !r.estimated {
			budgetUsed += r.u.TotalTokens
		}
	}
	// Only the vision report (16 tokens) counts against the budget.
	if budgetUsed != 16 {
		t.Errorf("budget used %d, want 16 (only the endpoint-reported count)", budgetUsed)
	}
	// The estimate is tracked separately.
	var estTokens int
	for _, r := range got {
		if r.estimated {
			estTokens += r.u.TotalTokens
		}
	}
	if estTokens != 5 { // ceil(20/4) = 5
		t.Errorf("estimate tokens %d, want 5", estTokens)
	}
	_ = budget
}
