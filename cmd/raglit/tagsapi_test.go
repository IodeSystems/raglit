package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// taggedDocsCorpus builds a scoped-registry index with documents carrying the
// two content tags the test will merge, plus a third document with an
// unrelated tag so the merge touches exactly the right rows.
func taggedDocsCorpus(t *testing.T) (root string, reg *raglit.Registry, corpusRoot string) {
	t.Helper()
	root = t.TempDir()
	var err error
	reg, err = raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })

	corpusRoot = filepath.Join(root, "corpus")
	must(t, os.MkdirAll(corpusRoot, 0o755))
	a := filepath.Join(corpusRoot, "a.pdf")
	b := filepath.Join(corpusRoot, "b.pdf")
	c := filepath.Join(corpusRoot, "c.pdf")
	for _, p := range []string{a, b, c} {
		must(t, os.WriteFile(p, []byte("x"), 0o644))
	}

	ctx := context.Background()
	st, _ := reg.Get("case")
	must(t, st.Ingest(ctx, raglit.Document{Path: a, Title: "a.pdf", Fragments: []raglit.Fragment{{Text: "alpha"}}}))
	must(t, st.Ingest(ctx, raglit.Document{Path: b, Title: "b.pdf", Fragments: []raglit.Fragment{{Text: "bravo"}}}))
	must(t, st.Ingest(ctx, raglit.Document{Path: c, Title: "c.pdf", Fragments: []raglit.Fragment{{Text: "charlie"}}}))

	// a and b carry "lead paint"; c carries an unrelated tag.
	must(t, st.SetDocumentIdentity(ctx, a, raglit.DocIdentity{
		Name: "A", Summary: "S", Kind: "agreement", Source: "machine", Model: "m", At: 1,
		ContentTags: []string{"lead paint", "escrow"}, RoleTags: []string{"reference"},
	}))
	must(t, st.SetDocumentIdentity(ctx, b, raglit.DocIdentity{
		Name: "B", Summary: "S", Kind: "agreement", Source: "machine", Model: "m", At: 1,
		ContentTags: []string{"lead paint"}, RoleTags: []string{"reference"},
	}))
	must(t, st.SetDocumentIdentity(ctx, c, raglit.DocIdentity{
		Name: "C", Summary: "S", Kind: "agreement", Source: "machine", Model: "m", At: 1,
		ContentTags: []string{"boundary survey"}, RoleTags: []string{"reference"},
	}))
	return
}

// startTagsAPI wires the daemon routes around a scoped registry and returns
// an httptest server. The test calls doAs with the admin token to reach
// /api/tags/merge.
func startTagsAPI(t *testing.T, reg *raglit.Registry, root string) (*httptest.Server, string) {
	t.Helper()
	mw, tokenPath, err := daemonAuth(context.Background(), reg, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	adminTokenPath = tokenPath
	t.Cleanup(func() { adminTokenPath = "" })
	tokBytes, _ := os.ReadFile(tokenPath)
	tok := strings.TrimSpace(string(tokBytes))

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(raglit.Home(root))
	h, err := buildGatHandler(reg, lf, raglit.Home(root), 8, nil, raglit.GCPolicy{}, nil, ingestFragParams{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mw(h))
	t.Cleanup(srv.Close)
	return srv, tok
}

func TestMergeTagsRoute_RecordsTheRulingInTheTrail(t *testing.T) {
	root, reg, corpusRoot := taggedDocsCorpus(t)
	srv, tok := startTagsAPI(t, reg, root)

	// Positive case: merge "lead paint" into "lead-based paint".
	// Two documents (a, b) carry the from-tag.
	body := `{"merges":["lead paint=>lead-based paint"],"by":"operator"}`
	u := srv.URL + "/api/tags/merge?" + url.Values{"index": {"case"}}.Encode()
	code, resp := doAs(t, "POST", u, tok, body)
	if code != http.StatusOK {
		t.Fatalf("merge: %d %s", code, clip(resp, 400))
	}
	var got struct {
		Merges   []raglit.TagMergeResult `json:"merges"`
		Recorded bool                    `json:"recorded"`
	}
	must(t, json.Unmarshal([]byte(resp), &got))
	if !got.Recorded {
		t.Fatalf("ruling not recorded: %s", resp)
	}
	if len(got.Merges) != 1 {
		t.Fatalf("merges: %d, want 1", len(got.Merges))
	}
	if got.Merges[0].Documents != 2 {
		t.Errorf("documents: %d, want 2", got.Merges[0].Documents)
	}

	// Read the trail: exactly one tag.merge entry naming the pair and the docs.
	auditPath := raglit.AuditPath(corpusRoot)
	events, err := raglit.ReadAudit(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	var merges []raglit.AuditEvent
	for _, e := range events {
		if e.Op == raglit.OpTagMerge {
			merges = append(merges, e)
		}
	}
	if len(merges) != 1 {
		t.Fatalf("trail has %d tag.merge entries, want 1", len(merges))
	}
	ev := merges[0]
	if ev.By != "operator" {
		t.Errorf("by: %q, want operator", ev.By)
	}
	if ev.TagMerge == nil {
		t.Fatal("no tag_merge payload in event")
	}
	if ev.TagMerge.To != "lead-based paint" {
		t.Errorf("to: %q", ev.TagMerge.To)
	}
	if len(ev.TagMerge.From) != 1 || ev.TagMerge.From[0] != "lead paint" {
		t.Errorf("from: %v", ev.TagMerge.From)
	}
	// The entry names the documents whose tags were rewritten.
	if len(ev.TagMerge.Documents) != 2 {
		t.Errorf("documents in ruling: %v, want 2", ev.TagMerge.Documents)
	}

	// Empty case: a merge whose From tags no document carries still records the
	// ruling. "ghost tag" is not in the index.
	body2 := `{"merges":["ghost tag=>real tag"],"by":"operator"}`
	code2, resp2 := doAs(t, "POST", u, tok, body2)
	if code2 != http.StatusOK {
		t.Fatalf("empty merge: %d %s", code2, clip(resp2, 400))
	}
	var got2 struct {
		Merges   []raglit.TagMergeResult `json:"merges"`
		Recorded bool                    `json:"recorded"`
	}
	must(t, json.Unmarshal([]byte(resp2), &got2))
	if !got2.Recorded {
		t.Fatalf("empty merge not recorded: %s", resp2)
	}
	if len(got2.Merges) != 1 || got2.Merges[0].Documents != 0 {
		t.Errorf("empty merge result: %+v", got2.Merges)
	}

	// The trail now has exactly two tag.merge entries.
	events2, err := raglit.ReadAudit(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	var merges2 []raglit.AuditEvent
	for _, e := range events2 {
		if e.Op == raglit.OpTagMerge {
			merges2 = append(merges2, e)
		}
	}
	if len(merges2) != 2 {
		t.Fatalf("trail has %d tag.merge entries, want 2", len(merges2))
	}
	// The empty ruling records the pair but names no documents.
	if merges2[1].TagMerge.To != "real tag" {
		t.Errorf("second ruling to: %q", merges2[1].TagMerge.To)
	}
	if len(merges2[1].TagMerge.Documents) != 0 {
		t.Errorf("second ruling documents: %v, want empty", merges2[1].TagMerge.Documents)
	}

	// The timestamp in the ruling is present and in the past.
	if ev.TagMerge.At == "" {
		t.Error("ruling has no timestamp")
	} else if _, perr := time.Parse(time.RFC3339, ev.TagMerge.At); perr != nil {
		t.Errorf("bad timestamp %q: %v", ev.TagMerge.At, perr)
	}
}

// TestMergeTagsRoute_TrailFirst_RecordsBeforeApply proves the ruling is
// recorded BEFORE the merge is applied. It acquires the SQLite write lock so
// MergeTags blocks on its first write. In trail-first code, RecordTagMerge
// writes to the JSONL audit trail (not SQLite) and completes before the block,
// so the trail entry exists when the client times out. In apply-first code,
// MergeTags blocks first and RecordTagMerge is never reached — no trail entry.
func TestMergeTagsRoute_TrailFirst_RecordsBeforeApply(t *testing.T) {
	root, reg, corpusRoot := taggedDocsCorpus(t)
	srv, tok := startTagsAPI(t, reg, root)

	// Acquire the SQLite write lock so MergeTags blocks on its first write.
	// In WAL mode, reads (indexView, DocumentPathsCarryingTags, digestRowsLocal)
	// are not blocked by the write lock; only s.db.Begin() in SetDocumentIdentity
	// is. RecordTagMerge writes to a JSONL file, not SQLite, so it is unaffected.
	dbPath := filepath.Join(root, "indexes", "case", "index.sqlite")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rawDB.Close() })
	if _, err := rawDB.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	// Call the merge route with a short client timeout. The server handler
	// records the ruling (JSONL write, succeeds) then blocks in MergeTags
	// (SQLite write, locked). The client times out; the trail entry is already
	// on disk.
	body := `{"merges":["lead paint=>lead-based paint"],"by":"operator"}`
	u := srv.URL + "/api/tags/merge?" + url.Values{"index": {"case"}}.Encode()
	req, err := http.NewRequest("POST", u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Expected: the server is blocked on the write lock, so the client
		// times out. This is the signal that MergeTags is blocked.
		if !strings.Contains(strings.ToLower(err.Error()), "deadline") && !strings.Contains(strings.ToLower(err.Error()), "timeout") {
			t.Fatalf("unexpected error (not a timeout): %v", err)
		}
	} else {
		// If the server responded before the timeout, the write lock did not
		// block MergeTags (e.g. WAL mode allowed the write). The test cannot
		// discriminate in this case; skip rather than false-fail.
		resp.Body.Close()
		t.Skip("server responded before timeout; write lock did not block MergeTags")
	}

	// The trail entry was recorded BEFORE the (blocked) merge was attempted.
	// This is the discriminating assertion: in apply-first code, MergeTags
	// blocks first and RecordTagMerge is never reached, so no entry exists.
	auditPath := raglit.AuditPath(corpusRoot)
	events, err := raglit.ReadAudit(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	var merges []raglit.AuditEvent
	for _, e := range events {
		if e.Op == raglit.OpTagMerge {
			merges = append(merges, e)
		}
	}
	if len(merges) != 1 {
		t.Fatalf("trail has %d tag.merge entries, want 1 (trail-first: recorded before the blocked merge)", len(merges))
	}
	if merges[0].TagMerge == nil {
		t.Fatal("no tag_merge payload in event")
	}
	if merges[0].TagMerge.To != "lead-based paint" {
		t.Errorf("to: %q", merges[0].TagMerge.To)
	}
	if len(merges[0].TagMerge.From) != 1 || merges[0].TagMerge.From[0] != "lead paint" {
		t.Errorf("from: %v", merges[0].TagMerge.From)
	}
	if merges[0].By != "operator" {
		t.Errorf("by: %q, want operator", merges[0].By)
	}
	// The ruling names the documents (captured before the merge, via a read
	// that is not blocked by the write lock in WAL mode).
	if len(merges[0].TagMerge.Documents) != 2 {
		t.Errorf("documents in ruling: %v, want 2", merges[0].TagMerge.Documents)
	}
}
