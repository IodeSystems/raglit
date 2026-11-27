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

// A code ceiling sizes code/section fragments, so the KindCode pool key must
// change when it does — a pooled cut at N would otherwise replay into the index
// under M. An unset ceiling keeps the old key, so the pool keeps hitting.
func TestCodeCeilingIsInTheKindCodePoolKey(t *testing.T) {
	w := &Worker{RecipeHash: "abc123", Code: &fakeCode{}}
	noCeiling := w.poolRecipe(KindCode)

	w.Frag.CodeCeiling = 600
	withCeiling := w.poolRecipe(KindCode)
	if withCeiling == noCeiling {
		t.Fatal("a code ceiling does not change the KindCode pool key — a pooled cut would survive the change")
	}
	w.Frag.CodeCeiling = 300
	if w.poolRecipe(KindCode) == withCeiling {
		t.Error("two ceilings produce the same KindCode pool key")
	}

	// Unset → the old key.
	w.Frag.CodeCeiling = 0
	if got := w.poolRecipe(KindCode); got != noCeiling {
		t.Errorf("an unset ceiling re-keyed KindCode: %q != %q — the pool would miss for nothing", got, noCeiling)
	}

	// The ceiling is code-fragment sizing: it does not re-key the other kinds.
	// (KindText has a |lines=1 suffix tested in TestTextKindsRekeyedForLines.)
	for _, k := range []DocKind{KindPDF, KindImage, KindOffice, KindEmail, KindSpreadsheet} {
		if w.poolRecipe(k) != HashHex([]byte("abc123|kind="+k.String())) {
			t.Errorf("%s: the ceiling re-keyed a non-code kind", k)
		}
	}
}

// A text file's pool key changed when fragments gained line spans, so a payload
// pooled before them (0/0) is a miss; other kinds keep their key.
func TestTextKindsRekeyedForLines(t *testing.T) {
	w := &Worker{RecipeHash: "abc123"}
	for _, k := range []DocKind{KindText, KindUnknown} {
		if w.poolRecipe(k) == HashHex([]byte("abc123|kind="+k.String())) {
			t.Errorf("%s: pre-lines pool key still in use", k)
		}
	}
	for _, k := range []DocKind{KindPDF, KindOffice, KindImage, KindEmail, KindSpreadsheet} {
		if w.poolRecipe(k) != HashHex([]byte("abc123|kind="+k.String())) {
			t.Errorf("%s: key changed; this kind gained no lines", k)
		}
	}
}
