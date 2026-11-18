package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

type liveBody struct {
	Version   string             `json:"version"`
	StartedAt string             `json:"started_at"`
	Embed     liveEmbed          `json:"embed"`
	Indexes   []raglit.IndexLive `json:"indexes"`
}

func liveNames(b liveBody) []string {
	var out []string
	for _, l := range b.Indexes {
		out = append(out, l.Name)
	}
	return out
}

// /api/status lists only the indexes the CALLER may read, and the daemon does
// the filtering: the admin sees every project, a session key for project t
// sees t's indexes and nothing of project u.
func TestLiveStatus_FiltersByTheCallersACL(t *testing.T) {
	base, reg, _ := testDaemon(t)
	st := testIndex(t, reg)
	if err := st.Ingest(context.Background(), raglit.Document{Path: "file:///a.md", Title: "A",
		Fragments: []raglit.Fragment{{Text: "walrus"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Get(nsIndex("u", "default")); err != nil {
		t.Fatal(err)
	}

	get := func(tok string) liveBody {
		t.Helper()
		code, b := doAs(t, "GET", base+"/api/status", tok, "")
		if code != http.StatusOK {
			t.Fatalf("/api/status: %d %s", code, b)
		}
		var out liveBody
		if err := json.Unmarshal([]byte(b), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// "default" is a global index (no project); a session key reads global
	// indexes too (its :global read pattern).
	admin := get(testToken)
	if got := strings.Join(liveNames(admin), ","); got != "default,t__default,u__default" {
		t.Fatalf("admin sees %q, want every index", got)
	}
	if admin.Version == "" || admin.StartedAt == "" || admin.Embed.Backend != "none" {
		t.Errorf("daemon fields: %+v", admin)
	}
	l := admin.Indexes[1]
	if l.Project != "t" || l.Index != "default" || l.State != raglit.LiveIdle || l.Files != 1 || l.Chunks != 1 {
		t.Errorf("t__default: %+v", l)
	}

	code, b := doAs(t, "POST", base+"/api/keys", testToken, `{"project":"t","session":"s1"}`)
	if code != http.StatusOK {
		t.Fatalf("mint: %d %s", code, b)
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(b), &minted); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(liveNames(get(minted.Token)), ","); got != "default,t__default" {
		t.Fatalf("session key for t sees %q, want the global index and t's, not u's", got)
	}
}

// `raglit status --json --all` needs no --project and prints the daemon's
// JSON; plain `status --project` keeps its text.
func TestStatusCmd_JSONAll(t *testing.T) {
	base, reg, flags := testDaemon(t)
	testIndex(t, reg)
	out := captureStdout(t, func() {
		if err := runStatus([]string{"--daemon", base, "--home", t.TempDir(), "--json", "--all"}); err != nil {
			t.Fatal(err)
		}
	})
	var body liveBody
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got := strings.Join(liveNames(body), ","); got != "default,t__default" {
		t.Errorf("indexes %q", got)
	}
	for _, field := range []string{`"project"`, `"index"`, `"state"`, `"queue_length"`, `"files"`, `"chunks"`,
		`"files_last_minute"`, `"embed_calls_last_minute"`, `"embed_seconds"`, `"embed_tokens_estimated"`,
		`"version"`, `"embed"`, `"backend"`, `"in_flight"`} {
		if !strings.Contains(out, field) {
			t.Errorf("JSON is missing %s:\n%s", field, out)
		}
	}

	text := captureStdout(t, func() {
		if err := runStatus(flags); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.HasPrefix(text, "index: ") || strings.Contains(text, "{") {
		t.Errorf("plain status changed:\n%s", text)
	}
}

func TestEmbedBackend(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:8080/v1": "local",
		"http://127.0.0.1:11434":   "local",
		"http://[::1]:9000/v1":     "local",
		"http://embedhost:8080/v1": "remote",
		"https://api.example/v1":   "remote",
	} {
		if got := embedBackend(in); got != want {
			t.Errorf("embedBackend(%q) = %q, want %q", in, got, want)
		}
	}
}
