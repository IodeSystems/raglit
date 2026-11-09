package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/iodesystems/raglit"
)

// POST /api/sync runs IndexTree per configured root: it queues, counts, and
// removes the documents of files that are gone.
func TestSyncAPI_CountsAndRemoves(t *testing.T) {
	d := newMCPDaemon(t)
	proj := t.TempDir()
	home := filepath.Join(proj, ".raglit")
	docs := filepath.Join(proj, "docs")
	must(t, os.MkdirAll(docs, 0o755))
	must(t, os.WriteFile(filepath.Join(docs, "a.md"), []byte("alpha"), 0o644))
	must(t, os.WriteFile(filepath.Join(docs, "b.md"), []byte("bravo"), 0o644))
	must(t, os.WriteFile(filepath.Join(docs, "c.txt"), []byte("not included"), 0o644))
	must(t, raglit.SaveConfig(raglit.Home(home), raglit.Config{
		Project: "proj",
		Indexes: map[string]raglit.IndexConfig{"docs": {Roots: []raglit.Root{{Path: "./docs", Include: []string{"*.md"}}}}},
	}))
	sync := func() syncIndex {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"home": home, "project": "proj"})
		code, out := doAs(t, "POST", d.srv.URL+"/api/sync", d.admin, string(body))
		if code != http.StatusOK {
			t.Fatalf("sync: %d %s", code, out)
		}
		var r syncResult
		must(t, json.Unmarshal([]byte(out), &r))
		if len(r.Indexes) != 1 || r.Indexes[0].Index != "docs" {
			t.Fatalf("sync result %s", out)
		}
		return r.Indexes[0]
	}
	if r := sync(); r.Queued != 2 || r.Removed != 0 {
		t.Fatalf("first sync %+v", r)
	}
	// Index b.md as a document, then delete its file.
	st, err := d.reg.Get("proj__docs")
	if err != nil {
		t.Fatal(err)
	}
	must(t, st.Ingest(t.Context(), raglit.Document{Path: filepath.Join(docs, "b.md"), Title: "b", Fragments: []raglit.Fragment{{Text: "bravo"}}}))
	must(t, os.Remove(filepath.Join(docs, "b.md")))
	if r := sync(); r.Removed != 1 {
		t.Fatalf("sync after delete %+v", r)
	}
	if code, out := doAs(t, "POST", d.srv.URL+"/api/sync", "", `{"home":"`+home+`","project":"proj"}`); code == http.StatusOK {
		t.Errorf("anonymous sync allowed: %s", out)
	}
}
