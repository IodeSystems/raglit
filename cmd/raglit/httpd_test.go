package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
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
	if err != nil || !id.ByPerson() || id.Kind != "legal:analysis" {
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

// TestGatDaemon_JobByID pins the citation path: a job named by a health row must
// answer no matter how far back it is.
//
// The health report links AT a job id, and the jobs view fetched the newest N
// and looked for it there. On the fence-dispute index the cited job was 927 and the
// newest was 1467, so the link rendered two hundred rows none of which was the
// one it was about, and read as a link that did nothing. Asking for the FIRST
// job under a limit of 1 reproduces that exactly.
func TestGatDaemon_JobByID(t *testing.T) {
	srv, reg := gatTestServer(t)
	st, _ := reg.Get("default")
	first, _ := st.Jobs("all", 10)
	if len(first) != 1 {
		t.Fatalf("want the seeded job, got %d", len(first))
	}
	oldest := first[0].ID
	// Bury it: every later job is newer, so a window of 1 cannot contain it.
	for _, u := range []string{"file:///a.md", "file:///b.md", "file:///c.md"} {
		if _, err := st.Enqueue(u, ""); err != nil {
			t.Fatal(err)
		}
	}
	if b := httpGet(t, srv.URL+"/api/jobs?index=default&limit=1"); strings.Contains(b, "queued.md") {
		t.Fatalf("limit=1 should not reach the oldest job — test no longer reproduces the case: %s", clip(b, 300))
	}
	b := httpGet(t, srv.URL+"/api/jobs?index=default&limit=1&id="+strconv.FormatInt(oldest, 10))
	if !strings.Contains(b, "queued.md") {
		t.Fatalf("by-id did not return job %d: %s", oldest, clip(b, 400))
	}
	// And it is the ONLY job in the answer — a citation resolves to one row.
	if strings.Contains(b, "a.md") || strings.Contains(b, "b.md") {
		t.Fatalf("by-id returned more than the cited job: %s", clip(b, 400))
	}

	resp, err := http.Get(srv.URL + "/api/jobs?index=default&id=999999")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job id → HTTP %d, want 404", resp.StatusCode)
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

// Without an embedding model, search by meaning is refused plainly (400), and
// /indexes says so up front.
func TestSearchByMeaningWithoutEmbedder(t *testing.T) {
	srv, _ := gatTestServer(t)
	if b := httpGet(t, srv.URL+"/indexes"); !strings.Contains(b, `"meaning":false`) {
		t.Fatalf("/indexes should report meaning:false:\n%s", clip(b, 300))
	}
	for _, mode := range []string{"vec", "hybrid"} {
		resp, err := http.Get(srv.URL + "/search?index=default&q=token&mode=" + mode)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("mode %s without an embedder: %d, want 400", mode, resp.StatusCode)
		}
	}
}

// With no mode, search runs hybrid where the index has embeddings and bm25
// where it does not — so the default never fails on a daemon without a model —
// and every hit reports its term coverage.
func TestSearchDefaultMode(t *testing.T) {
	srv, _ := gatTestServer(t)
	b := httpGet(t, srv.URL+"/search?index=default&q=how%20the%20refresh%20token%20expires")
	for _, want := range []string{`"matched_terms":["refresh","token"]`, `"query_terms":3`} {
		if !strings.Contains(b, want) {
			t.Fatalf("%s not in\n%s", want, clip(b, 600))
		}
	}
	if strings.Contains(b, `"similarity"`) {
		t.Fatalf("bm25 fallback hit carries a similarity:\n%s", clip(b, 600))
	}
}

// A document whose file is deleted shows as prunable in /indexes, and the
// prune endpoint previews it and then removes it.
func TestPruneEndpoint(t *testing.T) {
	srv, reg := gatTestServer(t)
	st, err := reg.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "gone", "wt.md")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Ingest(context.Background(), raglit.Document{Path: f, Fragments: []raglit.Fragment{{Text: "worktree copy"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDocumentHash(f, "h"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(f)); err != nil {
		t.Fatal(err)
	}
	if b := httpGet(t, srv.URL+"/indexes"); !strings.Contains(b, `"prunable":1`) {
		t.Fatalf("/indexes should report prunable:1:\n%s", clip(b, 400))
	}
	dry := httpPostJSON(t, srv.URL+"/api/index/prune?index=default", `{"dry_run":true}`)
	if !strings.Contains(dry, `"documents":1`) || !strings.Contains(dry, `"removed":false`) || !strings.Contains(dry, filepath.Dir(f)) {
		t.Fatalf("dry run:\n%s", dry)
	}
	if b := httpPostJSON(t, srv.URL+"/api/index/prune?index=default", `{}`); !strings.Contains(b, `"removed":true`) {
		t.Fatalf("prune:\n%s", b)
	}
	if b := httpGet(t, srv.URL+"/indexes"); strings.Contains(b, `"prunable"`) || !strings.Contains(b, `"documents":1`) {
		t.Fatalf("after prune:\n%s", clip(b, 400))
	}
}

// A search says which indexes it read, so "N results from M indexes" counts
// what was searched and not only the indexes that happened to have hits.
func TestSearchReportsSearchedIndexes(t *testing.T) {
	srv, reg := gatTestServer(t)
	if _, err := reg.Get("empty"); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Hits     []struct{ Index string }
		Searched []string
	}
	_ = json.Unmarshal([]byte(httpGet(t, srv.URL+"/search?index=default,empty&q=refresh")), &out)
	if len(out.Hits) == 0 || strings.Join(out.Searched, ",") != "default,empty" {
		t.Fatalf("searched = %v, %d hits; want default,empty with hits", out.Searched, len(out.Hits))
	}
}

// A document's History starts with the ingest that read it: when, and from
// where. Without it a freshly added document said "Nothing recorded".
func TestDocDetailHistoryHasTheIngest(t *testing.T) {
	srv, reg := gatTestServer(t)
	st, err := reg.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "renewal.md")
	if err := os.WriteFile(f, []byte("# Renewal\n\nrenews automatically unless notice is given"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := st.Enqueue("file://"+f, "")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's own pending job is older; drain until this one is done.
	w := &raglit.Worker{Store: st, Fetcher: raglit.Fetch}
	for {
		did, err := w.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			break
		}
	}
	var det struct {
		AddedAt string `json:"added_at"`
		History []struct {
			Kind, Source, Note, At string
			Seq                    int64
		}
	}
	_ = json.Unmarshal([]byte(httpGet(t, srv.URL+"/api/doc-detail?index=default&path="+url.QueryEscape(f))), &det)
	if len(det.History) == 0 {
		t.Fatalf("no history: %+v", det)
	}
	h := det.History[0]
	if h.Kind != "ingest" || h.Seq != id || h.Source != "done" || !strings.Contains(h.Note, "renewal.md") ||
		h.At == "" || det.AddedAt != h.At {
		t.Fatalf("first history row = %+v, added_at %q; want the ingest of job %d", h, det.AddedAt, id)
	}

	// Ingested without a job (a direct Ingest, or a forgotten job): the
	// document row still dates it.
	det.History, det.AddedAt = nil, ""
	_ = json.Unmarshal([]byte(httpGet(t, srv.URL+"/api/doc-detail?index=default&path="+url.QueryEscape("file:///notes.md"))), &det)
	if len(det.History) == 0 || det.History[0].Kind != "ingest" || det.AddedAt == "" {
		t.Fatalf("job-less document: %+v", det)
	}
}
