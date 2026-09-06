package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The document path is ONE encoded segment — slashes and all. That is the rule
// the app's routes are built on, so the tests address it the same way.
func urlEnc(s string) string { return url.PathEscape(s) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// The SPA is served from the router's NotFound, which is the arrangement the
// vanilla page deliberately avoided: its comment said path routing "would need
// the daemon to catch-all every unknown URL — which would swallow the attest
// mount and every API route with one typo."
//
// These tests are the answer to that. They exist so the deny-list in webui.go
// cannot quietly stop denying: a mistyped API path returning a page of HTML
// presents to a client as a JSON parse error, and whoever chases it goes looking
// in the encoder rather than at their own URL.

func TestSPA_RoutesServeTheDocument(t *testing.T) {
	srv, _ := gatTestServer(t)

	// Every one of these is an unknown URL as far as chi is concerned, and every
	// one is a real address in the app.
	for _, path := range []string{
		"/",
		"/i/default",
		"/i/default/jobs",
		"/i/default/jobs/7",
		"/i/default/search",
		"/i/default/d",
		"/i/default/d/" + urlEnc("file:///notes.md") + "/pages",
		"/i/default/d/" + urlEnc("file:///notes.md") + "/pages/3",
		"/i/default/d/" + urlEnc("file:///notes.md") + "/notes",
		"/i/default/attest/a/" + urlEnc("some/asset.pdf"),
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200", path, res.StatusCode)
			continue
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: content-type %q, want text/html", path, ct)
		}
		if !strings.Contains(string(body), `id="root"`) {
			t.Errorf("%s: body is not the app document", path)
		}
	}
}

// The point of the deny-list. A typo under an API prefix must 404 as JSON's
// absence, not 200 as a web page.
func TestSPA_DoesNotSwallowTheAPI(t *testing.T) {
	srv, _ := gatTestServer(t)

	for _, path := range []string{
		"/api/documnets", // a typo
		"/api/",          // the bare prefix
		"/api/attest/nosuch/state",
		"/status/nope",
		"/indexes/nope",
		"/openapi.json/nope",
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode == http.StatusOK && strings.Contains(string(body), `id="root"`) {
			t.Errorf("%s: served the SPA; an unknown API path must not return a page", path)
		}
	}
}

// The routes the daemon really does own still answer, with the SPA mounted over
// everything else.
func TestSPA_APIStillAnswers(t *testing.T) {
	srv, _ := gatTestServer(t)
	res, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("health: %d %s", res.StatusCode, body)
	}
}

// The bundle's own assets are served as themselves, not as the fallback
// document. A JS file answered with HTML is a blank page and a syntax error in
// the console.
func TestSPA_ServesItsAssets(t *testing.T) {
	srv, _ := gatTestServer(t)

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	src := ""
	for part := range strings.SplitSeq(string(body), `"`) {
		if strings.HasPrefix(part, "/assets/") && strings.HasSuffix(part, ".js") {
			src = part
			break
		}
	}
	if src == "" {
		t.Fatal("index.html names no /assets/*.js — is web/dist built?")
	}

	res, err = http.Get(srv.URL + src)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	asset, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s: status %d", src, res.StatusCode)
	}
	if strings.Contains(string(asset), `id="root"`) {
		t.Fatalf("%s: served the fallback document instead of the asset", src)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("%s: content-type %q, want javascript", src, ct)
	}
}

// Notes end to end: a note filed against a page comes back on the document, and
// an unknown path is refused rather than silently written nowhere.
func TestNotesAPI(t *testing.T) {
	srv, _ := gatTestServer(t)
	path := "file:///notes.md"

	post := func(url, body string) (int, string) {
		res, err := http.Post(srv.URL+url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	code, body := post("/api/notes?index=default&path="+urlEnc(path),
		`{"body":"this is an annotation OF the survey, not the survey","author":"carl","page":2}`)
	if code != http.StatusOK {
		t.Fatalf("add note: %d %s", code, body)
	}

	res, err := http.Get(srv.URL + "/api/notes?index=default&path=" + urlEnc(path))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got struct {
		Notes []struct {
			ID     int64  `json:"id"`
			Page   int    `json:"page"`
			Body   string `json:"body"`
			Author string `json:"author"`
		} `json:"notes"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Notes) != 1 {
		t.Fatalf("got %d notes, want 1", len(got.Notes))
	}
	if got.Notes[0].Page != 2 || got.Notes[0].Author != "carl" {
		t.Errorf("note round-tripped as %+v", got.Notes[0])
	}

	// An empty body is a stray click, not a state anybody meant to reach.
	if code, _ := post("/api/notes?index=default&path="+urlEnc(path), `{"body":"   "}`); code != http.StatusBadRequest {
		t.Errorf("empty note: status %d, want 400", code)
	}

	// A note against a document not in the index would be unreachable from every
	// screen that shows notes, so the write is refused rather than accepted.
	if code, _ := post("/api/notes?index=default&path="+urlEnc("file:///nope.md"), `{"body":"x"}`); code != http.StatusBadRequest {
		t.Errorf("unknown path: status %d, want 400", code)
	}

	// Deleting it leaves the document with none.
	if code, body := post("/api/notes/delete?index=default&id="+itoa(got.Notes[0].ID), `{}`); code != http.StatusOK {
		t.Fatalf("delete note: %d %s", code, body)
	}
	res2, err := http.Get(srv.URL + "/api/notes?index=default&path=" + urlEnc(path))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	got.Notes = nil
	if err := json.NewDecoder(res2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Notes) != 0 {
		t.Errorf("after delete: %d notes, want 0", len(got.Notes))
	}
}
