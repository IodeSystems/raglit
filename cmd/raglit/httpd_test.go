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
	h, err := buildGatHandler(reg, lf, home, 8, nil, raglit.GCPolicy{}, nil)
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

// A person's caption goes through the daemon, because on a daemon-routed project
// the daemon is the single writer — and it is what holds the model besides. The
// filename is untouched by it: the caption is a display name and a search target.
func TestGatDaemon_RecordsAPersonsIdentity(t *testing.T) {
	srv, reg := gatTestServer(t)
	body := httpPostJSON(t, srv.URL+"/api/identify?index=default&path=file:///notes.md"+
		"&name=Token+rotation+note&summary=A+note+recording+that+the+refresh+token+rotates+on+every+use."+
		"&kind=analysis&by=carl", `{}`)
	if !strings.Contains(body, "Token rotation note") || !strings.Contains(body, `"source":"person"`) {
		t.Fatalf("identify response: %s", clip(body, 400))
	}
	st, err := reg.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.DocumentIdentity("file:///notes.md")
	if err != nil || !id.ByPerson() || id.Kind != "analysis" {
		t.Fatalf("stored identity = %+v, %v", id, err)
	}
	// Findable by the caption, and the document's own text is unchanged.
	if b := httpGet(t, srv.URL+"/search?index=default&q=rotation%20note"); !strings.Contains(b, `"origin":"identity"`) {
		t.Errorf("a hit on the caption is not marked as one:\n%s", clip(b, 400))
	}
	if b := httpGet(t, srv.URL+"/api/get-document?path=notes"); strings.Contains(b, "Token rotation note") {
		t.Errorf("the caption leaked into the document's text:\n%s", clip(b, 400))
	}
}

// Generating one needs a model, and a daemon without one has to say so rather
// than answer with an empty identity.
func TestGatDaemon_IdentifyWithoutAModel(t *testing.T) {
	srv, _ := gatTestServer(t)
	resp, err := http.Post(srv.URL+"/api/identify?index=default&path=file:///notes.md", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400\n%s", resp.StatusCode, clip(string(b), 300))
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

// TestGatDaemon_RereadRejectsRelativePath guards the shape that put two rows in
// the ardley index for one file. A relative path resolves against the DAEMON's
// working directory, so it named nothing; the purge failed deep in the cascade
// as `pdftotext: exit status 1`, and on the runs where the daemon happened to be
// started in the project directory it succeeded and enqueued a second, cwd-bound
// document. Reject it at the door, and enqueue nothing when doing so.
func TestGatDaemon_RereadRejectsRelativePath(t *testing.T) {
	srv, reg := gatTestServer(t)
	st, _ := reg.Get("default")
	before, _ := st.Jobs("all", 100)

	resp, err := http.Post(srv.URL+"/api/reread?index=default&path=documents/evidence/x.pdf",
		"application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("relative path → HTTP %d: %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "relative") {
		t.Fatalf("error does not say why: %s", b)
	}
	if after, _ := st.Jobs("all", 100); len(after) != len(before) {
		t.Fatalf("rejected reread still enqueued: %d jobs before, %d after", len(before), len(after))
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
