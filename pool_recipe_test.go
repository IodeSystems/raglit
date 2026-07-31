package raglit

import "testing"

// The defect this fixes: the pool was keyed by (recipe, file bytes), and the
// recipe captured the models and the fragmenter — everything that shapes the
// output EXCEPT which reader produced it. A PDF once read as text cached
// "%PDF-1.7" under those bytes, and every later ingest of that content replayed
// the garbage on any path in any index. Fixing the router changed nothing.
func TestRoutingChangeIsACacheMiss(t *testing.T) {
	w := &Worker{RecipeHash: "abc123"}
	asText, asPDF := w.poolRecipe(KindText), w.poolRecipe(KindPDF)
	if asText == asPDF {
		t.Fatal("a document read as text and as a PDF share a pool key — a misroute would be permanent")
	}
	if asText == "" || asPDF == "" {
		t.Fatal("a configured recipe must still produce a key")
	}
}

// Same recipe, same kind: still a hit. The fix must not defeat the pool.
func TestTheSameReadStillPools(t *testing.T) {
	w := &Worker{RecipeHash: "abc123"}
	if w.poolRecipe(KindPDF) != w.poolRecipe(KindPDF) {
		t.Error("identical routing produced different keys — the pool would never hit")
	}
}

// No pool configured stays no pool: an empty recipe must not become a real key.
func TestNoRecipeNoKey(t *testing.T) {
	w := &Worker{}
	if got := w.poolRecipe(KindPDF); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

// The kind names are cache-key material, so they are pinned. Renumbering the
// iota or inserting a kind must not silently re-key every cached document.
func TestKindNamesArePinned(t *testing.T) {
	for k, want := range map[DocKind]string{
		KindText: "text", KindPDF: "pdf", KindImage: "image",
		KindOffice: "office", KindEmail: "email",
		KindSpreadsheet: "spreadsheet", KindUnknown: "unknown",
	} {
		if got := k.String(); got != want {
			t.Errorf("kind %d: got %q want %q", int(k), got, want)
		}
	}
}
