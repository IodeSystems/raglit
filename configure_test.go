package raglit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// Models.Configure runs once per built client with that client's role, and a
// header it sets reaches chat, embed and the context probe.
func TestModelsConfigure_OncePerRoleAndHeaderReachesTheWire(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]map[string]bool{} // path suffix -> X-Role values
	note := func(path, role string) {
		mu.Lock()
		defer mu.Unlock()
		if seen[path] == nil {
			seen[path] = map[string]bool{}
		}
		seen[path][role] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-Role")
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			var req struct {
				MaxTokens int `json:"max_tokens"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.MaxTokens == 1 {
				note("probe", role)
				// Any prompt is accepted; the probe ends at its ceiling.
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"content":"x"}}]}`)
				return
			}
			note("chat", role)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			note("embed", role)
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
	defer srv.Close()
	base := srv.URL + "/v1"

	calls := map[string]int{}
	m := Models{
		Vision:   ModelRole{BaseURL: base, Model: "vis"},
		Segment:  ModelRole{Model: "seg"},
		Identity: ModelRole{Model: "idm"},
		Embed:    ModelRole{BaseURL: base, Model: "emb"},
		OCR:      noTesseract,
		Channels: NewChannels(),
		// Fixed limits keep NewWorker's own embed probe off; the chat probe
		// below is the one that proves the header on DiscoverContext.
		EmbedLimitChars: 4096,
		Configure: func(role string, c *llm.Client) {
			calls[role]++
			c.Header = http.Header{"X-Role": {role}}
		},
	}
	log.SetOutput(&bytes.Buffer{})
	defer log.SetOutput(os.Stderr)
	s := testStore(t)
	w := NewWorker(s, m, nil)
	for _, role := range []string{"vision", "segment", "identity", "embed"} {
		if calls[role] != 1 {
			t.Errorf("Configure(%q) called %d times in NewWorker, want 1", role, calls[role])
		}
	}
	if len(calls) != 4 {
		t.Errorf("unexpected roles: %v", calls)
	}
	drain(t, w.OCR.Client)
	drain(t, w.Segmenter.Client)
	drain(t, s.identity().Client)
	if _, err := m.Embedder().EmbedDocs(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if calls["embed"] != 2 {
		t.Errorf("Embedder() should configure its own client: embed calls %d, want 2", calls["embed"])
	}

	mu.Lock()
	defer mu.Unlock()
	for _, c := range []struct{ kind, role string }{
		{"chat", "vision"}, {"chat", "segment"}, {"chat", "identity"},
		{"embed", "embed"}, {"probe", "vision"},
	} {
		if !seen[c.kind][c.role] {
			t.Errorf("no %s request carried X-Role=%s (saw %v)", c.kind, c.role, seen[c.kind])
		}
	}
}

// Configure nil is a no-op.
func TestModelsConfigure_NilIsNoop(t *testing.T) {
	c := Models{}.client("vision", ModelRole{BaseURL: "http://x", Model: "m"})
	if c == nil || c.Header != nil {
		t.Fatalf("client %+v", c)
	}
}

type orderProber struct {
	buf   *bytes.Buffer
	lines int
}

func (p *orderProber) ContextWindow(context.Context) (int, bool) { return 0, false }
func (p *orderProber) DiscoverContext(context.Context) (int, error) {
	p.lines = strings.Count(p.buf.String(), "probing chat input limit")
	return 1000, nil
}
func (p *orderProber) ProbeHost() string { return "gw.example:8080" }

// The probe line is written once, before the first probe request, and names the
// host without a path or key.
func TestChatInputLimitTokens_LogsBeforeTheProbe(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	p := &orderProber{buf: &buf}
	s := testStore(t)
	if n := s.ChatInputLimitTokens(context.Background(), p, "mod", 0); n <= 0 {
		t.Fatalf("limit %d", n)
	}
	if p.lines != 1 {
		t.Errorf("log line count before the probe = %d, want 1", p.lines)
	}
	out := buf.String()
	if !strings.Contains(out, "for mod at gw.example:8080 ") || strings.Contains(out, "http") {
		t.Errorf("log line: %q", out)
	}
	// Cached: no second probe, no second line.
	s.ChatInputLimitTokens(context.Background(), p, "mod", 0)
	if c := strings.Count(buf.String(), "probing chat input limit"); c != 1 {
		t.Errorf("line written %d times across two calls, want 1", c)
	}
}

// firstEmbedSpy records how many probe lines existed when the first request came.
type firstEmbedSpy struct {
	VectorClient
	buf   *bytes.Buffer
	first int
	n     int
}

func (v *firstEmbedSpy) Embed(ctx context.Context, model string, in []string) ([][]float32, error) {
	if v.n == 0 {
		v.first = strings.Count(v.buf.String(), "probing embed input limit")
	}
	v.n++
	return v.VectorClient.Embed(ctx, model, in)
}

// The embed probe logs once, before its first request.
func TestDiscoverEmbedLimit_LogsBeforeTheProbe(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	probes := 0
	spy := &firstEmbedSpy{VectorClient: &limitedVecClient{max: 1024, probes: &probes}, buf: &buf}
	e := NewEmbedder(spy, "m1")
	e.probeHost = "emb.example"
	if _, err := e.DiscoverEmbedLimit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spy.first != 1 {
		t.Errorf("lines before the first request = %d, want 1", spy.first)
	}
	if c := strings.Count(buf.String(), "probing embed input limit for m1 at emb.example"); c != 1 {
		t.Errorf("embed probe line written %d times, want 1: %q", c, buf.String())
	}
}
