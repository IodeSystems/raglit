package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An attachment's bytes come from /api/original, and nothing else does: not a
// corpus document, not a stray file in storage, not a path that climbs out.
func TestOriginalServesStorageDocumentsOnly(t *testing.T) {
	srv, corpus, att := rootTestServer(t)
	if err := os.MkdirAll(filepath.Dir(att), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(att, []byte("%PDF-order"), 0o644); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(filepath.Dir(att), "not-indexed.pdf")
	if err := os.WriteFile(stray, []byte("%PDF-stray"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The document page links to it.
	var d struct{ Path, Original string }
	_ = json.Unmarshal([]byte(httpGet(t, srv.URL+"/api/doc-detail?index=default&path="+url.QueryEscape(att))), &d)
	if !strings.HasPrefix(d.Path, "@") || !strings.HasPrefix(d.Original, "/api/original?") {
		t.Fatalf("doc-detail for an attachment: path=%q original=%q", d.Path, d.Original)
	}
	resp, err := http.Get(srv.URL + d.Original)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	if resp.StatusCode != http.StatusOK || string(body[:n]) != "%PDF-order" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("original: %d %q disposition=%q", resp.StatusCode, body[:n], resp.Header.Get("Content-Disposition"))
	}

	for name, p := range map[string]string{
		"stray storage file": stray,
		"corpus document":    filepath.Join(corpus, "notes.md"),
		"host file":          "/etc/hostname",
	} {
		if code, _ := getStatus(t, srv.URL+"/api/original?index=default&path="+url.QueryEscape(p)); code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", name, code)
		}
	}
	if code, _ := getStatus(t, srv.URL+"/api/original?index=default&path="+url.QueryEscape("@attachments/../../config.json")); code != http.StatusBadRequest {
		t.Errorf("climbing display path: %d, want 400", code)
	}
}
