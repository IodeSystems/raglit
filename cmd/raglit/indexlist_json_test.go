package main

import (
	"encoding/json"
	"testing"
)

// `raglit index --list --json` is what a picker reads: every index of every
// project, each with the selector that reads it, and the daemon's own fields
// passed through untouched.
func TestIndexListJSON(t *testing.T) {
	daemon := []byte(`{"indexes":[
		{"name":"dun__dun-main","documents":583,"root":"/src/dun","meaning":true,"scopes":["/src/dun"]},
		{"name":"code","documents":0,"meaning":true,"ambient":false,"about":"Go sources"}
	]}`)
	out, err := indexListJSON(daemon)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Indexes []map[string]any `json:"indexes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(got.Indexes) != 2 {
		t.Fatalf("got %d indexes, want 2", len(got.Indexes))
	}
	own, global := got.Indexes[0], got.Indexes[1]
	for k, want := range map[string]any{"project": "dun", "index": "dun-main", "selector": "dun:dun-main", "documents": 583.0, "root": "/src/dun"} {
		if own[k] != want {
			t.Errorf("project index %s = %v, want %v", k, own[k], want)
		}
	}
	for k, want := range map[string]any{"project": "", "index": "code", "selector": ":code", "about": "Go sources"} {
		if global[k] != want {
			t.Errorf("global index %s = %v, want %v", k, global[k], want)
		}
	}
	if sel, ok := nsNamedIndex(own["selector"].(string)); !ok || sel != "dun__dun-main" {
		t.Errorf("the selector does not read the index back: %q", sel)
	}
	if sel, ok := nsNamedIndex(global["selector"].(string)); !ok || sel != "code" {
		t.Errorf("the global selector does not read the index back: %q", sel)
	}
}
