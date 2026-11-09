package raglit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iodesystems/agentkit/llm"
	"github.com/iodesystems/raglit"
)

// This file is the embedder surface (see the package comment): everything an
// embedding program such as dund calls, and nothing else. It fails to compile
// when a signature in that surface changes, and fails to run when its behavior
// does. Do not widen it casually: each line is a promise under semver.

// Signatures, pinned by assignment. A change to any of them is a compile error
// here before it is one in an embedder.
var (
	_ func(raglit.Home) (*raglit.Store, error)                        = raglit.OpenHome
	_ func(raglit.Home, string) (*raglit.Store, error)                = raglit.OpenIndex
	_ func(*raglit.Store, raglit.Models, *raglit.Pool) *raglit.Worker = raglit.NewWorker

	_ func(*raglit.Store, string, string) (int64, error)                                           = (*raglit.Store).Enqueue
	_ func(*raglit.Store, context.Context, raglit.Document) error                                  = (*raglit.Store).Ingest
	_ func(*raglit.Store, string, int) ([]raglit.Hit, error)                                       = (*raglit.Store).Search
	_ func(*raglit.Store, string, string, int) ([]raglit.Hit, error)                               = (*raglit.Store).SearchPath
	_ func(*raglit.Store, string, raglit.SearchFilter, int) ([]raglit.Hit, error)                  = (*raglit.Store).SearchFiltered
	_ func(*raglit.Store, context.Context, string, int) ([]raglit.Hit, error)                      = (*raglit.Store).VecSearch
	_ func(*raglit.Store, context.Context, string, string, int) ([]raglit.Hit, error)              = (*raglit.Store).VecSearchPath
	_ func(*raglit.Store, context.Context, string, raglit.SearchFilter, int) ([]raglit.Hit, error) = (*raglit.Store).VecSearchFiltered
	_ func(*raglit.Store, context.Context, string, int) ([]raglit.Hit, error)                      = (*raglit.Store).HybridSearch
	_ func(*raglit.Store, context.Context, string, string, int) ([]raglit.Hit, error)              = (*raglit.Store).HybridSearchPath
	_ func(*raglit.Store, context.Context, string, raglit.SearchFilter, int) ([]raglit.Hit, error) = (*raglit.Store).HybridSearchFiltered
	_ func(*raglit.Store, *raglit.Embedder)                                                        = (*raglit.Store).SetEmbedder
	_ func(*raglit.Store, *raglit.Identifier)                                                      = (*raglit.Store).SetIdentifier
	_ func(*raglit.Store, *raglit.Store)                                                           = (*raglit.Store).SetParent
	_ func(*raglit.Store) ([]raglit.BranchChange, error)                                           = (*raglit.Store).BranchDiff
	_ func(*raglit.Store, string, int) ([]raglit.JobInfo, error)                                   = (*raglit.Store).Jobs
	_ func(*raglit.Store, int64) (raglit.JobInfo, error)                                           = (*raglit.Store).Job
	_ func(*raglit.Store) (*raglit.Job, error)                                                     = (*raglit.Store).ClaimNext

	// R6a, R6f, R7a: embedding, vectors, metadata, segmentation, listing, tags.
	// Prune is what dund calls to collect originals: Prune(false, true).
	_ func(raglit.VectorClient, string) *raglit.Embedder                                 = raglit.NewEmbedder
	_ func(raglit.Models) *raglit.Identifier                                             = raglit.Models.Identifier
	_ func(*raglit.Store) string                                                         = (*raglit.Store).EmbedModel
	_ func(*raglit.Store, string, int) (bool, error)                                     = (*raglit.Store).SyncEmbedModel
	_ func(*raglit.Store) (int, error)                                                   = (*raglit.Store).MissingVectors
	_ func(*raglit.Store, context.Context, int) (int, error)                             = (*raglit.Store).BackfillVectors
	_ func(*raglit.Store) (int, error)                                                   = (*raglit.Store).DropVectors
	_ func(*raglit.Store, string) (string, bool)                                         = (*raglit.Store).Meta
	_ func(*raglit.Store, string, string, int64) error                                   = (*raglit.Store).SetMeta
	_ func(raglit.Chatter) *raglit.Segmenter                                             = raglit.NewSegmenter
	_ func(*raglit.Segmenter, context.Context, string, string) (raglit.SegResult, error) = (*raglit.Segmenter).SegmentText
	_ func(*raglit.Store) ([]raglit.DocSummary, error)                                   = (*raglit.Store).Documents
	_ func(*raglit.Store, string) (string, error)                                        = (*raglit.Store).DocumentHash
	_ func(*raglit.Store, string, bool) error                                            = (*raglit.Store).RemoveDocument
	_ func() *raglit.Channels                                                            = raglit.NewChannels
	_ func(*raglit.Store) error                                                          = (*raglit.Store).Close
	_ func(raglit.Chatter, string) *raglit.Identifier                                    = raglit.NewIdentifier
	_ func(*raglit.Store, bool, bool) (raglit.PruneResult, error)                        = (*raglit.Store).Prune
	_ func(*raglit.Store, string, []string) error                                        = (*raglit.Store).SetTags

	// R8: stable fragment ids and the code segmenter.
	_ func(*raglit.Store, string) (bool, error) = (*raglit.Store).FragmentExists

	_ func(*raglit.Runner, context.Context) (context.Context, context.Context) = (*raglit.Runner).Start
	_ func(*raglit.Runner, time.Duration) bool                                 = (*raglit.Runner).Drain
	_ func(*raglit.Runner)                                                     = (*raglit.Runner).CutOff
	_ func(*raglit.Runner) <-chan struct{}                                     = (*raglit.Runner).Done

	_ func(raglit.Home) string = raglit.Home.IndexPath

	// R11: worktree branches.
	_ func(raglit.Home, raglit.Home, string, string) (*raglit.Store, error) = raglit.OpenBranch
	_ func(string) (string, bool)                                           = raglit.WorktreeOf
	_ func([]raglit.Home, func(string) bool) (int, error)                   = raglit.GCBranches
	_ error                                                                 = raglit.ErrParentOnly
)

// Types dund constructs or reads, named field by field so a rename fails here.
var (
	_ = raglit.Document{Path: "p", Title: "t", Fragments: []raglit.Fragment{{Page: 1, Ord: 0, Text: "x"}}}
	_ = raglit.DocSummary{}.ContentHash // the document's source hash, same value as DocumentHash
	_ = raglit.DocSummary{}.AddedAt
	_ = raglit.SegResult{}.Degraded
	_ = raglit.BranchMeta{Parent: "p", Root: "r", ParentRoot: "pr"}

	// R8: line spans (half-open, 1-based), symbol path and stable id.
	_ = raglit.Fragment{StartLine: 1, EndLine: 2, Symbol: "S.m"}
	_ = raglit.Hit{}.StartLine
	_ = raglit.Hit{}.EndLine
	_ = raglit.Hit{}.Symbol
	_ = raglit.Hit{}.ContentID

	// R8: usage hook and code segmenter, nil by default.
	_                  = raglit.Models{OnUsage: func(role, model string, u llm.Usage) {}, Code: nil}
	_                  = raglit.ModelRole{BaseURL: "u", APIKey: "k", Model: "m"}
	_                  = raglit.Models{Embed: raglit.ModelRole{}, Vision: raglit.ModelRole{}, Segment: raglit.ModelRole{}, Identity: raglit.ModelRole{}}
	_ int64            = raglit.Hit{}.ID
	_ string           = raglit.Hit{}.Title
	_ int              = raglit.Hit{}.Page
	_ int              = raglit.Hit{}.Ord
	_ float64          = raglit.Hit{}.Score
	_ string           = raglit.Hit{}.Origin
	_ *float64         = raglit.Hit{}.Similarity
	_ []string         = raglit.Hit{}.Matched
	_ int              = raglit.Hit{}.QueryTerms
	_ string           = raglit.Hit{}.Text
	_ string           = raglit.Hit{}.Path
	_ int64            = raglit.JobInfo{}.ID
	_ string           = raglit.JobInfo{}.URL
	_ string           = raglit.JobInfo{}.Title
	_ int              = raglit.JobInfo{}.Fragments
	_ []raglit.Segment = raglit.SegResult{}.Fragments
	_                  = raglit.CodeSpan{Sym: "s", Class: "c", StartLine: 1, EndLine: 2, CommentStartLine: 0, BodyStartLine: 0}
)

// fakeCode pins the CodeSegmenter method set.
type fakeCode struct{}

func (fakeCode) ForPath(string) (string, bool)                     { return "", false }
func (fakeCode) Symbols(string, []byte) ([]raglit.CodeSpan, error) { return nil, nil }
func (fakeCode) Sections([]byte) ([]raglit.CodeSpan, error)        { return nil, nil }
func (fakeCode) Runtime() string                                   { return "fake" }

var _ raglit.CodeSegmenter = fakeCode{}

// oneStore is the smallest StoreSet: what an embedder without a Registry passes.
type oneStore struct {
	name string
	st   *raglit.Store
}

func (o oneStore) Names() []string { return []string{o.name} }
func (o oneStore) Existing(name string) (*raglit.Store, error) {
	if name != o.name {
		return nil, os.ErrNotExist
	}
	return o.st, nil
}

var _ raglit.StoreSet = oneStore{}

func TestEmbedderSurface(t *testing.T) {
	dir := t.TempDir()
	home := raglit.Home(filepath.Join(dir, "idx"))
	st, err := raglit.OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	src := filepath.Join(dir, "docs", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("The harbor pilot logged a zeppelin sighting.\n\nUnrelated line about gardening.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := st.Enqueue("file://"+src, "notes")
	if err != nil {
		t.Fatal(err)
	}

	// Batch enqueue: signature and field names pinned; two more files, one pass.
	var enqMany func([]raglit.EnqueueItem) ([]raglit.EnqueueResult, error) = st.EnqueueMany
	_ = raglit.EnqueueItem{URL: "", Title: "", Fresh: false}
	_ = raglit.EnqueueResult{ID: 0, Err: nil}
	var manyIDs []int64
	var manyPaths []string
	for _, name := range []string{"one.txt", "two.txt"} {
		p := filepath.Join(dir, "docs", name)
		if err := os.WriteFile(p, []byte("Batch file "+name+" mentions a narwhal.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		manyPaths = append(manyPaths, p)
	}
	mres, err := enqMany([]raglit.EnqueueItem{
		{URL: "file://" + manyPaths[0], Title: "one"},
		{URL: "file://" + manyPaths[1], Title: "two", Fresh: true},
	})
	if err != nil || len(mres) != 2 {
		t.Fatalf("EnqueueMany: %v, err %v", mres, err)
	}
	for _, r := range mres {
		if r.Err != nil || r.ID == 0 {
			t.Fatalf("EnqueueMany result: %+v", r)
		}
		manyIDs = append(manyIDs, r.ID)
	}

	models := raglit.Models{Channels: raglit.NewChannels()} // offline: no model of any role
	r := &raglit.Runner{
		Stores: oneStore{"default", st},
		NewWorker: func(s *raglit.Store) (*raglit.Worker, error) {
			return raglit.NewWorker(s, models, nil), nil
		},
		Runners: 2,
		Logf:    t.Logf,
		Notef:   t.Logf,
	}
	r.Start(context.Background())

	var ji raglit.JobInfo
	deadline := time.Now().Add(30 * time.Second)
	for {
		if ji, err = st.Job(id); err != nil {
			t.Fatal(err)
		}
		if ji.State == string(raglit.JobDone) || ji.State == string(raglit.JobError) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still %q after 30s", ji.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ji.State != string(raglit.JobDone) {
		t.Fatalf("job ended %q: %s", ji.State, ji.Error)
	}
	for _, mid := range manyIDs {
		for {
			mji, err := st.Job(mid)
			if err != nil {
				t.Fatal(err)
			}
			if mji.State == string(raglit.JobDone) {
				break
			}
			if mji.State == string(raglit.JobError) || time.Now().After(deadline) {
				t.Fatalf("batch job %d ended %q: %s", mid, mji.State, mji.Error)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if hs, err := st.Search("narwhal", 5); err != nil || len(hs) == 0 {
		t.Fatalf("batch-enqueued files not searchable: %d hits, err %v", len(hs), err)
	}

	var _ raglit.Job // the claimed-job type, named so a rename breaks here
	hits, err := st.Search("zeppelin", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("search found nothing after the job finished")
	}
	var h raglit.Hit = hits[0]
	if h.Text == "" || h.Path == "" {
		t.Fatalf("hit has no text or path: %+v", h)
	}

	// A filter with no tags and no fields, then a path scope.
	if hs, err := st.SearchFiltered("zeppelin", raglit.SearchFilter{}, 5); err != nil || len(hs) == 0 {
		t.Fatalf("empty filter: %d hits, err %v", len(hs), err)
	}
	if hs, err := st.SearchFiltered("zeppelin", raglit.SearchFilter{PathPrefix: h.Path}, 5); err != nil || len(hs) == 0 {
		t.Fatalf("path filter: %d hits, err %v", len(hs), err)
	}
	if hs, err := st.SearchFiltered("zeppelin", raglit.SearchFilter{PathPrefix: "/nowhere/"}, 5); err != nil || len(hs) != 0 {
		t.Fatalf("path filter that excludes: %d hits, err %v", len(hs), err)
	}
	// Tags and fields compile and run; nothing here carries either.
	if hs, err := st.SearchFiltered("zeppelin", raglit.SearchFilter{Tags: []string{"absent"}, Fields: map[string]string{"k": "v"}}, 5); err != nil || len(hs) != 0 {
		t.Fatalf("tag+field filter: %d hits, err %v", len(hs), err)
	}
	// Vector and hybrid variants take the same filter. No embedder is set, so
	// only the call shape is checked.
	ctx := context.Background()
	_, _ = st.VecSearchFiltered(ctx, "zeppelin", raglit.SearchFilter{}, 5)
	_, _ = st.HybridSearchFiltered(ctx, "zeppelin", raglit.SearchFilter{PathPrefix: h.Path}, 5)

	// Branch: a second index in the same home overlays the first.
	br, err := raglit.OpenIndex(home, "branch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { br.Close() })
	br.SetParent(st)
	if hs, err := br.Search("zeppelin", 5); err != nil || len(hs) == 0 {
		t.Fatalf("branch does not see the parent: %d hits, err %v", len(hs), err)
	}
	if _, err := br.BranchDiff(); err != nil {
		t.Fatal(err)
	}

	jobs, err := st.Jobs("", 10) // notes + the two EnqueueMany files
	if err != nil || len(jobs) != 3 {
		t.Fatalf("jobs: %v, err %v", jobs, err)
	}

	// Direct ingest of local text, tagged by the caller, listed and removed.
	const dpath = "mem://strategy-1"
	if err := st.Ingest(ctx, raglit.Document{Path: dpath, Title: "Strategy", Fragments: []raglit.Fragment{{Page: 0, Ord: 0, Text: "The quokka maneuver wins endgames."}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTags(dpath, []string{"kind:strategy"}); err != nil {
		t.Fatal(err)
	}
	if hs, err := st.SearchFiltered("quokka", raglit.SearchFilter{Tags: []string{"kind:strategy"}}, 5); err != nil || len(hs) != 1 {
		t.Fatalf("tag filter kind:strategy: %d hits, err %v", len(hs), err)
	}
	if hs, err := st.SearchFiltered("quokka", raglit.SearchFilter{Tags: []string{"kind"}}, 5); err != nil || len(hs) != 0 {
		t.Fatalf("tag filter kind (whole tags only): %d hits, err %v", len(hs), err)
	}
	var found *raglit.DocSummary
	docs, err := st.Documents()
	if err != nil {
		t.Fatal(err)
	}
	for i := range docs {
		if docs[i].Path == dpath {
			found = &docs[i]
		}
	}
	if found == nil {
		t.Fatalf("Documents does not list %s", dpath)
	}
	dh, err := st.DocumentHash(dpath)
	if err != nil {
		t.Fatal(err)
	}
	if found.ContentHash != dh {
		t.Fatalf("DocSummary.ContentHash %q != DocumentHash %q", found.ContentHash, dh)
	}
	if err := st.RemoveDocument(dpath, false); err != nil {
		t.Fatal(err)
	}
	if hs, err := st.Search("quokka", 5); err != nil || len(hs) != 0 {
		t.Fatalf("search after remove: %d hits, err %v", len(hs), err)
	}
	if docs, err := st.Documents(); err != nil {
		t.Fatal(err)
	} else {
		for _, d := range docs {
			if d.Path == dpath {
				t.Fatal("Documents still lists the removed document")
			}
		}
	}

	// Caller-given lines and symbol come back on the hit, with a stable id.
	const cpath = "mem://code-1"
	if err := st.Ingest(ctx, raglit.Document{Path: cpath, Title: "Code", Fragments: []raglit.Fragment{{Page: 0, Ord: 0, Text: "func Wombat() {}", StartLine: 10, EndLine: 11, Symbol: "Zoo.Wombat"}}}); err != nil {
		t.Fatal(err)
	}
	hs, err := st.Search("Wombat", 5)
	if err != nil || len(hs) != 1 {
		t.Fatalf("search Wombat: %d hits, err %v", len(hs), err)
	}
	if hs[0].StartLine != 10 || hs[0].EndLine != 11 || hs[0].Symbol != "Zoo.Wombat" || hs[0].ContentID == "" {
		t.Fatalf("hit lost its lines, symbol or content id: %+v", hs[0])
	}
	if ok, err := st.FragmentExists(hs[0].ContentID); err != nil || !ok {
		t.Fatalf("FragmentExists after ingest: %v, err %v", ok, err)
	}
	if err := st.RemoveDocument(cpath, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.FragmentExists(hs[0].ContentID); err != nil || ok {
		t.Fatalf("FragmentExists after remove: %v, err %v", ok, err)
	}

	if !r.Drain(10 * time.Second) {
		t.Fatal("drain did not finish")
	}
}
