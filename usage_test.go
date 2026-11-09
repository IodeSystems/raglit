package raglit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
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
		OnUsage: func(role, model string, u llm.Usage) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, usageRec{role, model, u})
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
			continue // the limit probe; checked in the next test
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
	hooked := Models{OnUsage: func(role, model string, u llm.Usage) {
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
		OnUsage: func(role, model string, u llm.Usage) { got = append(got, usageRec{role, model, u}) },
	}
	v := m.UsageVector(llm.NewClient(base, "", "emb"))
	if _, err := v.Embed(context.Background(), "emb", []string{strings.Repeat("a", 10)}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].role != "embed" || got[0].model != "emb" || got[0].u.PromptTokens != 3 || got[0].u.TotalTokens != 3 {
		t.Errorf("got %+v, want one embed/emb report of 3 tokens", got)
	}
	// nil hook: the client comes back as it went in.
	c := llm.NewClient(base, "", "emb")
	if (Models{}).UsageVector(c) != VectorClient(c) {
		t.Error("nil OnUsage wrapped the embed client")
	}
}
