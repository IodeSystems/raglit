package raglit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

	_ func(*raglit.Runner, context.Context) (context.Context, context.Context) = (*raglit.Runner).Start
	_ func(*raglit.Runner, time.Duration) bool                                 = (*raglit.Runner).Drain
	_ func(*raglit.Runner)                                                     = (*raglit.Runner).CutOff
	_ func(*raglit.Runner) <-chan struct{}                                     = (*raglit.Runner).Done
)

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

	jobs, err := st.Jobs("", 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %v, err %v", jobs, err)
	}

	if !r.Drain(10 * time.Second) {
		t.Fatal("drain did not finish")
	}
}
