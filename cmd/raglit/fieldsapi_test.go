package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/iodesystems/raglit"
)

// The daemon is the DEFAULT path — `raglit serve` proxies to it unless
// --embedded is passed — so a tool that works only against a local registry
// works for almost nobody. These drive the fields surface through the real HTTP
// handler and through the client-mode MCP proxy that sits on top of it.

// seedFields registers a type and one extracted document on a running daemon's
// index, so the read surface has something to return.
func seedFields(t *testing.T, reg *raglit.Registry) {
	t.Helper()
	st, err := reg.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.SetDocType(raglit.DocType{
		Name: "work order", Description: "a garage repair order",
		Prompt: "The RO number is top right.",
		Schema: json.RawMessage(`{"type":"object","properties":{"order_number":{"type":"string"}},"required":["order_number"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDocumentIdentity(ctx, "file:///notes.md", raglit.DocIdentity{
		Name: "Token rotation note", Summary: "A note recording that the refresh token rotates on every use.",
		Kind: "analysis", Source: "machine", Model: "m", At: 1,
		ContentTags: []string{"token rotation"}, RoleTags: []string{"reference"},
		DocType: "work order",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDocumentFields(ctx, "file:///notes.md", raglit.DocFields{
		Type: "work order", Source: "machine", Model: "m", At: 1,
		Fields: json.RawMessage(`{"order_number":"RO-04471"}`),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGatDaemon_FieldsSurface(t *testing.T) {
	srv, reg := gatTestServer(t)
	seedFields(t, reg)

	body := httpGet(t, srv.URL+"/api/fields?index=default&path=notes")
	if !strings.Contains(body, "RO-04471") || !strings.Contains(body, `"type":"work order"`) {
		t.Fatalf("/api/fields: %s", clip(body, 400))
	}
	// It is a model's reading, so the provenance travels with it.
	if !strings.Contains(body, `"source":"machine"`) {
		t.Errorf("/api/fields dropped the provenance: %s", clip(body, 400))
	}

	types := httpGet(t, srv.URL+"/api/doc-types?index=default")
	if !strings.Contains(types, "work order") || !strings.Contains(types, "order_number") {
		t.Fatalf("/api/doc-types: %s", clip(types, 400))
	}
	if !strings.Contains(types, `"resolved":1`) {
		t.Errorf("/api/doc-types dropped coverage: %s", clip(types, 400))
	}

}

// A document with no extraction answers with an empty record, and an unknown one
// is an error — the same two cases the library distinguishes.
func TestGatDaemon_FieldsForADocumentThatIsNotAForm(t *testing.T) {
	srv, reg := gatTestServer(t)
	seedFields(t, reg)
	st, err := reg.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Ingest(context.Background(), raglit.Document{
		Path: "file:///letter.md", Title: "Letter",
		Fragments: []raglit.Fragment{{Text: "a letter about the fence line, sent to the county"}},
	}); err != nil {
		t.Fatal(err)
	}
	body := httpGet(t, srv.URL+"/api/fields?index=default&path=letter")
	if !strings.Contains(body, `"type":""`) {
		t.Fatalf("a document that is not a form should return an empty record: %s", clip(body, 400))
	}
	if resp := httpGetStatus(t, srv.URL+"/api/fields?index=default&path=nosuchdocument"); resp != 404 {
		t.Errorf("an unknown document returned %d, want 404", resp)
	}
}

// Client mode is the default for `raglit serve`, so the MCP get_fields tool has
// to reach the daemon. Registered with a nil handler it would panic on first
// call, which no compile-time check catches.
func TestServeClient_ProxiesGetFields(t *testing.T) {
	srv, reg := gatTestServer(t)
	seedFields(t, reg)
	h := daemonToolHandlers(srv.URL, 8, "", nil)

	if h.getFields == nil {
		t.Fatal("client mode registers get_fields with no handler — it would panic on first call")
	}
	out := callTool(t, h.getFields, map[string]any{"path": "notes"})
	if !strings.Contains(out, "RO-04471") || !strings.Contains(out, "work order") {
		t.Fatalf("get_fields: %s", out)
	}
}

// Every tool the server registers must have a handler in BOTH backings. The
// contract is one; the backing is not.
func TestToolHandlers_ClientModeFillsEveryTool(t *testing.T) {
	srv, _ := gatTestServer(t)
	h := daemonToolHandlers(srv.URL, 8, "", nil)
	for name, fn := range map[string]any{
		"search": h.search, "search_figures": h.searchFigures, "ingest": h.ingest,
		"index_status": h.status, "list_indexes": h.listIndexes,
		"list_documents": h.listDocuments, "get_document": h.getDocument,
		"get_fields": h.getFields, "ocr": h.ocr,
	} {
		if fn == nil {
			t.Errorf("client mode has no handler for %s", name)
		}
	}
}

// httpGetStatus is httpGet for the cases where the STATUS is the assertion.
func httpGetStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
