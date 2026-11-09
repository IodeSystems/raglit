package raglit

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnqueueManyOrderLanesAndRefusals(t *testing.T) {
	s := testStore(t)
	if _, err := s.SetIgnorePatterns([]string{"**/node_modules/**"}); err != nil {
		t.Fatal(err)
	}
	items := []EnqueueItem{
		{URL: "file:///a.txt", Title: "A"},
		{URL: ""},
		{URL: "file:///b.txt.raglit.md"},
		{URL: "/w/node_modules/x/README.md"},
		{URL: "file:///c.pdf", Title: "C"},
	}
	// Use a real sidecar suffix, whatever it is.
	items[2].URL = "file:///b" + generatedSuffixes[0]
	res, err := s.EnqueueMany(items)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(items) {
		t.Fatalf("got %d results for %d items", len(res), len(items))
	}
	for _, i := range []int{1, 2, 3} {
		if res[i].Err == nil || res[i].ID != 0 {
			t.Errorf("item %d: want refusal, got %+v", i, res[i])
		}
	}
	if !errors.Is(res[3].Err, ErrIgnored) {
		t.Errorf("item 3: want ErrIgnored, got %v", res[3].Err)
	}
	if res[0].Err != nil || res[4].Err != nil || res[0].ID == 0 || res[4].ID <= res[0].ID {
		t.Fatalf("accepted items: %+v %+v", res[0], res[4])
	}
	for _, i := range []int{0, 4} {
		var lane string
		if err := s.db.QueryRow(`SELECT lane FROM ingest_jobs WHERE id=?`, res[i].ID).Scan(&lane); err != nil {
			t.Fatal(err)
		}
		if lane == "" || lane != string(LaneFor(items[i].URL)) {
			t.Errorf("item %d lane %q, want %q", i, lane, LaneFor(items[i].URL))
		}
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM ingest_jobs`).Scan(&n)
	if n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
}

func TestEnqueueManyDedupe(t *testing.T) {
	s := testStore(t)
	existing, err := s.Enqueue("file:///a.txt", "A")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.EnqueueMany([]EnqueueItem{
		{URL: "file:///a.txt"},              // existing pending
		{URL: "file:///b.txt"},              // new
		{URL: "file:///b.txt"},              // within batch
		{URL: "file:///a.txt", Fresh: true}, // fresh: not covered by non-fresh
		{URL: "file:///a.txt", Fresh: true}, // covered by the fresh one just inserted
		{URL: "file:///a.txt"},              // covered by either
	})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].ID != existing {
		t.Errorf("existing: %d want %d", res[0].ID, existing)
	}
	if res[1].ID == 0 || res[2].ID != res[1].ID {
		t.Errorf("in-batch dup: %d %d", res[1].ID, res[2].ID)
	}
	if res[3].ID == existing || res[3].ID == 0 {
		t.Errorf("fresh deduped against non-fresh: %d", res[3].ID)
	}
	if res[4].ID != res[3].ID {
		t.Errorf("fresh dup: %d want %d", res[4].ID, res[3].ID)
	}
	if res[5].ID != existing {
		t.Errorf("non-fresh after fresh: %d want %d", res[5].ID, existing)
	}
}

func TestEnqueueManyEmpty(t *testing.T) {
	res, err := testStore(t).EnqueueMany(nil)
	if err != nil || len(res) != 0 {
		t.Fatalf("%v %v", res, err)
	}
}

func TestEnqueueManyConcurrentWithWorkers(t *testing.T) {
	s := testStore(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errc := make(chan error, 4)
	wg.Add(1)
	go func() { // claim + complete loop, as a worker pool does
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			j, err := s.ClaimNext()
			if err != nil {
				errc <- fmt.Errorf("claim: %w", err)
				return
			}
			if j == nil {
				time.Sleep(time.Millisecond)
				continue
			}
			if err := s.completeJob(j.ID, 1, "text-overlap"); err != nil {
				errc <- fmt.Errorf("complete: %w", err)
				return
			}
		}
	}()
	for round := 0; round < 4; round++ {
		items := make([]EnqueueItem, 500)
		for i := range items {
			items[i] = EnqueueItem{URL: fmt.Sprintf("file:///r%d/f%d.txt", round, i)}
		}
		res, err := s.EnqueueMany(items)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		for i, r := range res {
			if r.Err != nil || r.ID == 0 {
				t.Fatalf("round %d item %d: %+v", round, i, r)
			}
		}
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-errc:
		if strings.Contains(err.Error(), "BUSY") || strings.Contains(err.Error(), "busy") || strings.Contains(err.Error(), "locked") {
			t.Fatal(err)
		}
		t.Fatal(err)
	default:
	}
}

func TestEnqueueManyFasterThanEnqueue(t *testing.T) {
	const n = 1000
	a, b := testStore(t), testStore(t)
	items := make([]EnqueueItem, n)
	for i := range items {
		items[i] = EnqueueItem{URL: fmt.Sprintf("file:///d/f%d.txt", i)}
	}
	t0 := time.Now()
	for _, it := range items {
		if _, err := a.Enqueue(it.URL, it.Title); err != nil {
			t.Fatal(err)
		}
	}
	single := time.Since(t0)
	t0 = time.Now()
	if _, err := b.EnqueueMany(items); err != nil {
		t.Fatal(err)
	}
	many := time.Since(t0)
	t.Logf("%d items: Enqueue loop %v, EnqueueMany %v (%.1fx)", n, single, many, float64(single)/float64(many))
	if many >= single {
		t.Errorf("EnqueueMany (%v) not faster than %d Enqueue calls (%v)", many, n, single)
	}
}
