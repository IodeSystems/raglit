package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// rootTestServer: an index holding two corpus documents under one tree and one
// extracted attachment in the index's own storage.
func rootTestServer(t *testing.T) (*httptest.Server, string, string) {
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
	corpus := filepath.Join(t.TempDir(), "case")
	att := filepath.Join(st.AttachmentDirFor(filepath.Join(corpus, "mail.eml")), "p01-01-order.pdf")
	for _, d := range []raglit.Document{
		{Path: filepath.Join(corpus, "records", "survey.pdf"), Title: "survey.pdf",
			Fragments: []raglit.Fragment{{Text: "the hillcrest survey monument"}}},
		{Path: filepath.Join(corpus, "notes.md"), Title: "notes.md",
			Fragments: []raglit.Fragment{{Text: "a note about the driveway"}}},
		{Path: att, Title: "p01-01-order.pdf",
			Fragments: []raglit.Fragment{{Text: "order on reconsideration"}}},
	} {
		if err := st.Ingest(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetDocumentIdentity(context.Background(), filepath.Join(corpus, "records", "survey.pdf"), raglit.DocIdentity{
		Name: "2010 Hillcrest survey", Kind: "survey", Source: "person",
	}); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(home)
	h, err := buildGatHandler(reg, lf, home, 8, nil, raglit.GCPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); reg.Close() })
	return srv, corpus, att
}

func getStatus(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRootExcludesStorageAndPathsAreRootRelative(t *testing.T) {
	srv, corpus, _ := rootTestServer(t)

	var idx struct {
		Indexes []struct{ Name, Root string } `json:"indexes"`
	}
	_ = json.Unmarshal([]byte(httpGet(t, srv.URL+"/indexes")), &idx)
	if len(idx.Indexes) != 1 || idx.Indexes[0].Root != corpus {
		t.Fatalf("/indexes root = %+v; want %s (the attachment must not widen it)", idx.Indexes, corpus)
	}

	var docs struct {
		Root      string `json:"root"`
		Documents []struct {
			Path, Rel string
			GenName   string `json:"gen_name"`
			GenKind   string `json:"gen_kind"`
		} `json:"documents"`
	}
	_ = json.Unmarshal([]byte(httpGet(t, srv.URL+"/api/documents?index=default")), &docs)
	rels := map[string]bool{}
	for _, d := range docs.Documents {
		rels[d.Rel] = true
		if d.Rel == "records/survey.pdf" && (d.GenName != "2010 Hillcrest survey" || d.GenKind != "survey") {
			t.Errorf("survey row lost its identity: %+v", d)
		}
	}
	for _, want := range []string{"records/survey.pdf", "notes.md"} {
		if !rels[want] {
			t.Errorf("no document with rel %q in %v", want, rels)
		}
	}
	var attRel string
	for r := range rels {
		if strings.HasPrefix(r, "@attachments/") && strings.HasSuffix(r, "/p01-01-order.pdf") {
			attRel = r
		}
	}
	if attRel == "" || docs.Root != corpus {
		t.Fatalf("attachment not spelled @attachments/…: %v (root %q)", rels, docs.Root)
	}

	// Search hits carry the display path, the root, and what the document is.
	var sr struct {
		Hits []struct {
			DocID string `json:"doc_id"`
			Rel   string `json:"rel"`
			Root  string `json:"root"`
			Name  string `json:"name"`
			Kind  string `json:"kind"`
		} `json:"hits"`
	}
	_ = json.Unmarshal([]byte(httpGet(t, srv.URL+"/search?index=default&q=hillcrest")), &sr)
	if len(sr.Hits) == 0 || sr.Hits[0].Rel != "records/survey.pdf" || sr.Hits[0].Root != corpus ||
		sr.Hits[0].Name != "2010 Hillcrest survey" || sr.Hits[0].Kind != "survey" {
		t.Fatalf("search hit = %+v", sr.Hits)
	}

	// Document endpoints take the display path back.
	if b := httpGet(t, srv.URL+"/api/get-document?index=default&path=records/survey.pdf"); !strings.Contains(b, "hillcrest survey monument") {
		t.Fatalf("get-document by rel: %s", clip(b, 300))
	}
	var det struct {
		Path, Root, Asset, Text string
	}
	_ = json.Unmarshal([]byte(httpGet(t, srv.URL+"/api/doc-detail?index=default&path=records/survey.pdf")), &det)
	if det.Path != "records/survey.pdf" || det.Asset != "records/survey.pdf" || det.Root != corpus ||
		!strings.Contains(det.Text, "monument") {
		t.Fatalf("doc-detail by rel = %+v", det)
	}
	det = struct{ Path, Root, Asset, Text string }{}
	_ = json.Unmarshal([]byte(httpGet(t, srv.URL+"/api/doc-detail?index=default&path="+url.QueryEscape(attRel))), &det)
	if det.Path != attRel || det.Asset != "" || !strings.Contains(det.Text, "reconsideration") {
		t.Fatalf("doc-detail of a stored attachment = %+v", det)
	}
}

func TestDocPathRefusesEscapes(t *testing.T) {
	srv, _, _ := rootTestServer(t)
	for _, p := range []string{"../etc/passwd", "records/../../x", "@attachments/../../x"} {
		code, b := getStatus(t, srv.URL+"/api/doc-detail?index=default&path="+url.QueryEscape(p))
		if code != http.StatusBadRequest {
			t.Errorf("%q → HTTP %d: %s", p, code, clip(b, 200))
		}
	}
}

// The $schema link names the scheme the request used, not https on a plain
// HTTP LAN address.
func TestSchemaLinkUsesRequestScheme(t *testing.T) {
	srv, _, _ := rootTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/indexes", nil)
	req.Host = "192.168.1.76:7431"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"$schema":"http://192.168.1.76:7431/schemas/`) {
		t.Fatalf("$schema not http: %s", clip(string(b), 200))
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"$schema":"https://192.168.1.76:7431/schemas/`) {
		t.Fatalf("$schema behind a TLS proxy not https: %s", clip(string(b), 200))
	}
}

// An index created after the daemon started still gets a review surface.
func TestAttestMountsIndexesCreatedLater(t *testing.T) {
	home := raglit.Home(t.TempDir())
	reg, err := raglit.OpenScopedRegistry(string(home))
	if err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(home)
	h, err := buildGatHandler(reg, lf, home, 8, nil, raglit.GCPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); reg.Close() })

	if code, _ := getStatus(t, srv.URL+"/api/attest/later/assets"); code != http.StatusNotFound {
		t.Fatalf("assets of an index that does not exist: %d, want 404", code)
	}
	st, err := reg.Get("later")
	if err != nil {
		t.Fatal(err)
	}
	corpus := filepath.Join(t.TempDir(), "case")
	if err := st.Ingest(context.Background(), raglit.Document{Path: filepath.Join(corpus, "a.md"), Title: "a.md",
		Fragments: []raglit.Fragment{{Text: "a later document"}}}); err != nil {
		t.Fatal(err)
	}
	if code, b := getStatus(t, srv.URL+"/api/attest/later/assets"); code != http.StatusOK || !strings.Contains(b, `"assets"`) {
		t.Fatalf("assets of a later index: %d %s", code, clip(b, 200))
	}
	if code, b := getStatus(t, srv.URL+"/attest/later"); code != http.StatusOK || !strings.Contains(strings.ToLower(b), "<!doctype html") {
		t.Fatalf("review page of a later index: %d %s", code, clip(b, 200))
	}
}
