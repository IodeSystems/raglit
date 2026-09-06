package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A withdrawal that cannot be READ is the bug this method exists to close.
//
// The failure it prevents is silent by construction: a consumer walks the
// documents directory, finds a withdrawn file undeclared, and offers it as a
// candidate — forever, and identically on every run. Measured on a real corpus:
// 23 candidates had been ruled out three weeks earlier, all with the same
// recorded grounds. Nothing errored and nothing warned.
func TestWithdrawalsCarryTheGrounds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/problems" {
			t.Errorf("asked %s, want /api/problems", r.URL.Path)
		}
		// The list is the health report filtered to this kind, and asking for it
		// by name matters: every OTHER kind hides withdrawn documents, so a
		// caller that forgot the filter would get an empty answer that reads
		// exactly like "nothing has been withdrawn".
		if got := r.URL.Query().Get("kind"); got != "withdrawn" {
			t.Errorf("kind=%q, want withdrawn", got)
		}
		if got := r.URL.Query().Get("index"); got != "matter" {
			t.Errorf("index=%q, want matter", got)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"problems":[
			{"kind":"withdrawn","subject":"documents/drafts/letter.md","detail":"a draft is argument, not record"}
		]}`))
	}))
	defer srv.Close()

	ws, err := New(srv.URL).Withdrawals(context.Background(), "matter")
	if err != nil {
		t.Fatalf("Withdrawals: %v", err)
	}
	if len(ws) != 1 {
		t.Fatalf("got %d withdrawals, want 1", len(ws))
	}
	if ws[0].Path != "documents/drafts/letter.md" {
		t.Errorf("path %q", ws[0].Path)
	}
	// THE GROUNDS ARE THE POINT. A withdrawal without them is a delete, and a
	// caller that reports "ruled out" with no reason has told somebody a
	// decision was made and given them no way to check it or reverse it.
	if ws[0].Reason != "a draft is argument, not record" {
		t.Errorf("reason %q — the grounds did not survive the round trip", ws[0].Reason)
	}
}

// NOBODY COULD ASK is not NOTHING WAS WITHDRAWN, and the two must not collapse.
//
// A stopped daemon reported as an empty ledger is worse than an error: the
// consumer proceeds to offer every withdrawn document as a candidate, believing
// it checked.
func TestAnUnreachableDaemonIsNotAnEmptyLedger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// An older raglit without the route. Same meaning as a stopped one.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	ws, err := New(srv.URL).Withdrawals(context.Background(), "matter")
	if err == nil {
		t.Fatalf("a daemon with no route returned %d withdrawals and no error", len(ws))
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("error %v — a caller distinguishes unreachable from answered-with-an-error", err)
	}
}

// THIS FUNCTION HAD NEVER RETURNED A DOCUMENT.
//
// It decoded into a type whose `pages` is a list of pages with their text; the
// endpoint sends `pages` as a count. Every real response failed to unmarshal, and
// the failure was invisible one layer up: kgraph maps the error to "could not
// ask" and falls back to looking for a sidecar on disk, so a backlog that was
// meant to read the index went on answering from the filesystem for every
// corpus, permanently, while looking like it worked.
//
// The payload here is copied from what /api/find-documents emits.
func TestDocumentsDecodesWhatTheEndpointSends(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/find-documents" {
			t.Errorf("asked %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"documents":[
			{"index":"i","path":"/c/deed.pdf","title":"Deed","fragments":12,"pages":3,"vision":0,"frag_mode":"text"},
			{"index":"i","path":"/c/scan.pdf","title":"Scan","fragments":0,"pages":9,"vision":0,"frag_mode":""}
		]}`))
	}))
	defer srv.Close()

	ds, err := New(srv.URL).Documents(context.Background(), "i")
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if len(ds) != 2 {
		t.Fatalf("got %d documents, want 2", len(ds))
	}
	if ds[0].Fragments != 12 || ds[0].Pages != 3 {
		t.Errorf("deed: fragments=%d pages=%d", ds[0].Fragments, ds[0].Pages)
	}
	// THE ROW THIS EXISTS FOR. In the index, nine pages, and nothing readable
	// came out of it — a scan nobody OCR'd. "In the index" and "has text" are
	// different questions and this is the document that separates them.
	if ds[1].Fragments != 0 || ds[1].Pages != 9 {
		t.Errorf("scan: fragments=%d pages=%d", ds[1].Fragments, ds[1].Pages)
	}
}
