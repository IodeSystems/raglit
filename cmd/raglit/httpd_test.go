package main

import (
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// gatTestServer stands up the gat daemon over a temp index seeded with one
// indexed document and one pending job (workers are NOT started, so the job
// stays pending for the status/jobs assertions).
func gatTestServer(t *testing.T) (*httptest.Server, *raglit.Registry) {
	t.Helper()
	home := raglit.Home(t.TempDir())
	reg, err := raglit.OpenRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := reg.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Ingest(context.Background(), raglit.Document{
		Path: "file:///notes.md", Title: "Notes",
		Fragments: []raglit.Fragment{{Text: "the refresh token rotates on every use"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue("file:///queued.md", "Queued"); err != nil {
		t.Fatal(err)
	}

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(home)
	h, err := buildGatHandler(reg, lf, home, 8, nil, raglit.GCPolicy{})
	if err != nil {
		t.Fatalf("buildGatHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); reg.Close() })
	return srv, reg
}

func TestGatDaemon_Surface(t *testing.T) {
	srv, _ := gatTestServer(t)
	cases := []struct{ name, url, want string }{
		{"health", "/api/health", `"status":"ok"`},
		{"openapi", "/openapi.json", "/api/health"},
		{"indexes", "/indexes", `"documents":1`},
		{"status", "/status?index=default", `"pending":1`},
		{"jobs", "/api/jobs?index=default", "file:///queued.md"},
		{"documents", "/api/documents?index=default", "file:///notes.md"},
		{"search", "/search?index=default&q=refresh%20token", "notes.md"},
		{"get-document", "/api/get-document?path=notes", "rotates on every use"},
		{"ui", "/", "<!doctype html>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if b := httpGet(t, srv.URL+c.url); !strings.Contains(b, c.want) {
				t.Fatalf("%s: %q not in\n%s", c.name, c.want, clip(b, 400))
			}
		})
	}
	// GraphQL surface mounted (gat.RegisterHuma).
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("/graphql not mounted")
	}
}

// TestGatDaemon_IngestOptionalFields guards that a POST /ingest with only
// `targets` (no index/title) is accepted — huma marks body fields required by
// default, so these must be tagged omitempty.
func TestGatDaemon_IngestOptionalFields(t *testing.T) {
	srv, _ := gatTestServer(t)
	body := httpPostJSON(t, srv.URL+"/ingest", `{"targets":["file:///x.md"]}`)
	if !strings.Contains(body, `"queued":1`) {
		t.Fatalf("ingest without index/title: %s", body)
	}
}

func TestGatDaemon_JobControlPOST(t *testing.T) {
	srv, reg := gatTestServer(t)
	st, _ := reg.Get("default")
	jobs, _ := st.Jobs("pending", 10)
	if len(jobs) != 1 {
		t.Fatalf("want 1 pending job, got %d", len(jobs))
	}
	id := jobs[0].ID

	// Cancel via POST body — exercises the gat POST/JSON-body path.
	body := httpPostJSON(t, srv.URL+"/api/jobs/cancel?index=default", `{"id":`+strconv.FormatInt(id, 10)+`}`)
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("cancel: %s", body)
	}
	after, _ := st.Jobs("all", 10)
	for _, j := range after {
		if j.ID == id {
			t.Fatal("job was not canceled")
		}
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s → HTTP %d: %s", url, resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func httpPostJSON(t *testing.T, url, body string) string {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s → HTTP %d: %s", url, resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
