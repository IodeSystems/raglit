package raglit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// recordingVecClient counts what it is asked to embed and returns a vector
// that is a function of the input alone, so a reused vector and a fresh one
// for the same text can be compared byte for byte.
type recordingVecClient struct {
	mu     sync.Mutex
	inputs []string
}

func (c *recordingVecClient) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	c.mu.Lock()
	c.inputs = append(c.inputs, input...)
	c.mu.Unlock()
	out := make([][]float32, len(input))
	for i, t := range input {
		sum := sha256.Sum256([]byte(t))
		v := make([]float32, 8)
		for j := range v {
			v[j] = float32(sum[j]) - 127.5
		}
		// A topic axis, so a query can rank.
		if strings.Contains(t, "walrus") {
			v[0] += 500
		}
		out[i] = v
	}
	return out, nil
}

// take returns what was embedded since the last take, with the doc prefix cut.
func (c *recordingVecClient) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.inputs))
	for i, t := range c.inputs {
		out[i] = strings.TrimPrefix(t, "search_document: ")
	}
	c.inputs = nil
	return out
}

// paragraphs is a text of n paragraphs of the same length, so a same-length
// edit of one moves no window boundary.
func paragraphs(n int, edit int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		word := "steady"
		if i == edit {
			word = "CHANGE"
		}
		fmt.Fprintf(&b, "Paragraph %03d %s %s.\n\n", i, word, strings.Repeat("walrus text ", 30))
	}
	return b.String()
}

// fragmentTexts is the text of each own-words fragment of path, in order.
func fragmentTexts(t *testing.T, s *Store, path string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT f.text FROM fragments f JOIN documents d ON d.id = f.doc_id
		WHERE d.path = ? AND `+SQLOwnWordsF+` ORDER BY f.id`, path)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var x string
		must(t, rows.Scan(&x))
		out = append(out, x)
	}
	return out
}

// vectorsByText is every stored fragment vector, keyed by path and text.
func vectorsByText(t *testing.T, s *Store) map[string][]byte {
	t.Helper()
	rows, err := s.db.Query(`SELECT d.path, f.text, fv.vec FROM fragments f
		JOIN documents d ON d.id = f.doc_id JOIN fragment_vectors fv ON fv.fragment_id = f.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var p, x string
		var v []byte
		must(t, rows.Scan(&p, &x, &v))
		out[p+"\x00"+x] = v
	}
	return out
}

func lastEmbedDetail(t *testing.T, s *Store, jobID int64) string {
	t.Helper()
	stages, err := s.JobStages(jobID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(stages) - 1; i >= 0; i-- {
		if stages[i].Name == "embed" {
			return stages[i].Detail
		}
	}
	t.Fatalf("no embed stage in %+v", stages)
	return ""
}

func ingestFile(t *testing.T, s *Store, path, body string) int64 {
	t.Helper()
	must(t, os.WriteFile(path, []byte(body), 0o644))
	id, err := s.Enqueue(path, "")
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: s}
	if _, err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	return id
}

// newTexts is the texts of after that before does not hold.
func newTexts(before, after []string) map[string]bool {
	old := map[string]bool{}
	for _, x := range before {
		old[x] = true
	}
	out := map[string]bool{}
	for _, x := range after {
		if !old[x] {
			out[x] = true
		}
	}
	return out
}

// A re-ingest with one paragraph changed embeds only the fragments whose text
// changed; the rest copy the vectors they had. The stage log says so.
func TestReuse_ReingestEmbedsOnlyChangedFragments(t *testing.T) {
	s := openMem(t)
	c := &recordingVecClient{}
	s.SetEmbedder(NewEmbedder(c, "m"))
	path := filepath.Join(t.TempDir(), "doc.txt")

	id := ingestFile(t, s, path, paragraphs(12, -1))
	before := fragmentTexts(t, s, path)
	if got := c.take(); len(got) != len(before) || len(before) < 4 {
		t.Fatalf("first ingest embedded %d of %d fragment(s)", len(got), len(before))
	}
	if d := lastEmbedDetail(t, s, id); !strings.Contains(d, "reused 0, embedded") {
		t.Fatalf("first embed detail = %q", d)
	}

	id = ingestFile(t, s, path, paragraphs(12, 6))
	after := fragmentTexts(t, s, path)
	want := newTexts(before, after)
	got := c.take()
	if len(want) == 0 || len(want) >= len(after) {
		t.Fatalf("the edit changed %d of %d fragment(s); the test needs some, not all", len(want), len(after))
	}
	if len(got) != len(want) {
		t.Fatalf("re-ingest embedded %d text(s), want the %d changed", len(got), len(want))
	}
	for _, x := range got {
		if !want[x] {
			t.Fatalf("embedded unchanged text %.40q", x)
		}
	}
	d := lastEmbedDetail(t, s, id)
	if wantD := fmt.Sprintf("reused %d, embedded %d", len(after)-len(want), len(want)); !strings.Contains(d, wantD) {
		t.Fatalf("embed detail = %q, want %q", d, wantD)
	}
	if n, err := s.MissingVectors(); err != nil || n != 0 {
		t.Fatalf("missing vectors = %d, %v", n, err)
	}
}

// A second document with the same text embeds nothing.
func TestReuse_IdenticalDocumentEmbedsNothing(t *testing.T) {
	s := openMem(t)
	c := &recordingVecClient{}
	s.SetEmbedder(NewEmbedder(c, "m"))
	dir := t.TempDir()
	ingestFile(t, s, filepath.Join(dir, "a.txt"), paragraphs(6, -1))
	c.take()
	id := ingestFile(t, s, filepath.Join(dir, "b.txt"), paragraphs(6, -1))
	if got := c.take(); len(got) != 0 {
		t.Fatalf("identical document embedded %d text(s)", len(got))
	}
	if d := lastEmbedDetail(t, s, id); !strings.Contains(d, "embedded 0") {
		t.Fatalf("embed detail = %q", d)
	}

	// Store.Ingest reuses too, including its own document's old fragments.
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "x.md", Fragments: []Fragment{{Text: "one walrus"}, {Text: "two walrus"}}}))
	if got := c.take(); len(got) != 2 {
		t.Fatalf("Ingest embedded %v", got)
	}
	must(t, s.Ingest(ctx, Document{Path: "x.md", Fragments: []Fragment{{Text: "one walrus"}, {Text: "three walrus"}}}))
	if got := c.take(); len(got) != 1 || got[0] != "three walrus" {
		t.Fatalf("Ingest re-ingest embedded %v, want only the new text", got)
	}
	if n, err := s.MissingVectors(); err != nil || n != 0 {
		t.Fatalf("missing vectors = %d, %v", n, err)
	}
}

// Vectors are not reused across models: an index whose vectors are recorded
// as another model's embeds everything again.
func TestReuse_NotAcrossEmbedModels(t *testing.T) {
	s := openMem(t)
	c := &recordingVecClient{}
	s.SetEmbedder(NewEmbedder(c, "old"))
	if _, err := s.SyncEmbedModel("old", 8); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ingestFile(t, s, filepath.Join(dir, "a.txt"), paragraphs(6, -1))
	c.take()

	// The embedder changed and the record was not synced yet.
	s.SetEmbedder(NewEmbedder(c, "new"))
	ingestFile(t, s, filepath.Join(dir, "b.txt"), paragraphs(6, -1))
	if got := c.take(); len(got) == 0 {
		t.Fatal("a document under another model reused the old model's vectors")
	}
	// Nor of another length under the same model id.
	s2 := openMem(t)
	s2.SetEmbedder(NewEmbedder(c, "m"))
	must(t, s2.Ingest(context.Background(), Document{Path: "a.md", Fragments: []Fragment{{Text: "walrus"}}}))
	must(t, s2.SetMeta(metaEmbedDim, "4", 1))
	c.take()
	must(t, s2.Ingest(context.Background(), Document{Path: "b.md", Fragments: []Fragment{{Text: "walrus"}}}))
	if got := c.take(); len(got) != 1 {
		t.Fatalf("a vector of another length was reused: embedded %v", got)
	}
}

// BackfillVectors copies a vector for text that has one before it embeds.
func TestReuse_BackfillVectors(t *testing.T) {
	s := openMem(t)
	c := &recordingVecClient{}
	s.SetEmbedder(NewEmbedder(c, "m"))
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "a.md", Fragments: []Fragment{{Text: "alpha walrus"}, {Text: "beta walrus"}}}))
	s.SetEmbedder(nil)
	must(t, s.Ingest(ctx, Document{Path: "b.md", Fragments: []Fragment{
		{Text: "alpha walrus"}, {Text: "gamma walrus"}, {Text: "gamma walrus"}}}))
	s.SetEmbedder(NewEmbedder(c, "m"))
	c.take()
	n, err := s.BackfillVectors(ctx, 64)
	if err != nil || n != 3 {
		t.Fatalf("BackfillVectors = %d, %v; want 3", n, err)
	}
	if got := c.take(); len(got) != 1 || got[0] != "gamma walrus" {
		t.Fatalf("backfill embedded %v, want only the one new text, once", got)
	}
	if m, err := s.MissingVectors(); err != nil || m != 0 {
		t.Fatalf("missing vectors = %d, %v", m, err)
	}
	vs := vectorsByText(t, s)
	if !bytes.Equal(vs["a.md\x00alpha walrus"], vs["b.md\x00alpha walrus"]) {
		t.Fatal("the backfilled copy differs from the vector it copied")
	}
}

// A worktree branch embeds only the text its change made; the rest comes from
// the parent's vectors.
func TestReuse_BranchEmbedsOnlyItsNewText(t *testing.T) {
	base := t.TempDir()
	mainRoot, wtRoot := filepath.Join(base, "main"), filepath.Join(base, "wt")
	must(t, os.MkdirAll(mainRoot, 0o755))
	must(t, os.MkdirAll(wtRoot, 0o755))
	parentHome := Home(filepath.Join(base, "homes", "main"))
	c := &recordingVecClient{}

	p, err := OpenHome(parentHome)
	if err != nil {
		t.Fatal(err)
	}
	p.SetEmbedder(NewEmbedder(c, "m"))
	ingestFile(t, p, filepath.Join(mainRoot, "doc.txt"), paragraphs(12, -1))
	before := fragmentTexts(t, p, filepath.Join(mainRoot, "doc.txt"))
	must(t, p.Close())
	c.take()

	br, err := OpenBranch(parentHome, Home(filepath.Join(base, "homes", "wt")), wtRoot, mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = br.Close() })
	br.SetEmbedder(NewEmbedder(c, "m"))
	wtPath := filepath.Join(wtRoot, "doc.txt")
	id := ingestFile(t, br, wtPath, paragraphs(12, 3))
	after := fragmentTexts(t, br, wtPath)
	want := newTexts(before, after)
	got := c.take()
	if len(after) == 0 || len(want) == 0 || len(got) != len(want) {
		t.Fatalf("branch embedded %d text(s), want the %d new of %d", len(got), len(want), len(after))
	}
	for _, x := range got {
		if !want[x] {
			t.Fatalf("branch embedded the parent's text %.40q", x)
		}
	}
	if d := lastEmbedDetail(t, br, id); !strings.Contains(d, fmt.Sprintf("reused %d", len(after)-len(want))) {
		t.Fatalf("branch embed detail = %q", d)
	}
}

// An index from before text_key gains the column and its index at open, every
// fragment gets its key, and the old vectors are reused from then on.
func TestReuse_MigratesIndexWithoutTextKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &recordingVecClient{}
	s.SetEmbedder(NewEmbedder(c, "m"))
	ctx := context.Background()
	must(t, s.Ingest(ctx, Document{Path: "a.md", Fragments: []Fragment{{Text: "alpha walrus"}, {Text: "beta walrus"}}}))
	// Back to the schema before R12a.
	for _, q := range []string{`DROP INDEX fragments_text_key`, `ALTER TABLE fragments DROP COLUMN text_key`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	must(t, s.Close())

	s, err = Open(path)
	if err != nil {
		t.Fatalf("opening an index without text_key: %v", err)
	}
	defer s.Close()
	var n int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM fragments WHERE text_key = ''`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d fragment(s) left without a text key", n)
	}
	var key string
	must(t, s.db.QueryRow(`SELECT text_key FROM fragments WHERE text = 'alpha walrus'`).Scan(&key))
	if key != textKey("alpha walrus") {
		t.Fatalf("backfilled key %q", key)
	}
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='fragments_text_key'`).Scan(&n))
	if n != 1 {
		t.Fatal("no index on text_key after migrate")
	}
	s.SetEmbedder(NewEmbedder(c, "m"))
	c.take()
	must(t, s.Ingest(ctx, Document{Path: "b.md", Fragments: []Fragment{{Text: "beta walrus"}}}))
	if got := c.take(); len(got) != 0 {
		t.Fatalf("migrated index embedded %v", got)
	}
}

// Reuse changes no result: an index built by edits with reuse holds the same
// vectors, and answers a query the same, as one embedded fresh.
func TestReuse_SearchMatchesFreshEmbed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	reused := openMem(t)
	reused.SetEmbedder(NewEmbedder(&recordingVecClient{}, "m"))
	ingestFile(t, reused, path, paragraphs(12, -1))
	ingestFile(t, reused, path, paragraphs(12, 6))
	ingestFile(t, reused, filepath.Join(dir, "copy.txt"), paragraphs(12, 6))

	fresh := openMem(t)
	fresh.SetEmbedder(NewEmbedder(&recordingVecClient{}, "m"))
	ingestFile(t, fresh, path, paragraphs(12, 6))
	ingestFile(t, fresh, filepath.Join(dir, "copy.txt"), paragraphs(12, 6))

	rv, fv := vectorsByText(t, reused), vectorsByText(t, fresh)
	if len(rv) == 0 || len(rv) != len(fv) {
		t.Fatalf("%d vector(s) with reuse, %d fresh", len(rv), len(fv))
	}
	for k, v := range fv {
		if !bytes.Equal(rv[k], v) {
			t.Fatalf("vector for %.60q differs", k)
		}
	}
	ctx := context.Background()
	key := func(hs []Hit) []string {
		var out []string
		for _, h := range hs {
			out = append(out, fmt.Sprintf("%s|%s|%.6f", h.Path, h.Text, h.Score))
		}
		sort.Strings(out)
		return out
	}
	for _, q := range []string{"walrus", "Paragraph 006 CHANGE"} {
		a, err := reused.VecSearch(ctx, q, 50)
		if err != nil {
			t.Fatal(err)
		}
		b, err := fresh.VecSearch(ctx, q, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(a) == 0 || strings.Join(key(a), "\n") != strings.Join(key(b), "\n") {
			t.Fatalf("query %q: %d hit(s) with reuse, %d fresh, or they differ", q, len(a), len(b))
		}
	}
}
