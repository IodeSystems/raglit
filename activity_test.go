package raglit

import (
	"context"
	"fmt"
	"testing"
)

func TestActivity_MergedFeedCombinesBothTables(t *testing.T) {
	s := testStore(t)

	// Enqueue one ingest job and one identity job.
	if _, err := s.Enqueue("file:///a.pdf", "A"); err != nil {
		t.Fatal(err)
	}
	// Ingest a document so we can enqueue an identity job.
	if err := s.Ingest(context.Background(), Document{
		Path:      "/corpus/doc.pdf",
		Title:     "doc.pdf",
		Fragments: []Fragment{{Page: 1, Ord: 0, Text: "hello world"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueIdentity("/corpus/doc.pdf", false); err != nil {
		t.Fatal(err)
	}

	items, err := s.Activity("", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("Activity returned %d items; want 2 (one ingest + one identity)", len(items))
	}

	// Check both kinds are present.
	kinds := map[ActivityKind]bool{}
	for _, it := range items {
		kinds[it.Kind] = true
	}
	if !kinds[ActivityIngest] {
		t.Error("no ingest item in merged feed")
	}
	if !kinds[ActivityCaption] {
		t.Error("no caption (identity) item in merged feed")
	}
}

func TestActivity_OrderedByEnqueuedAtDescending(t *testing.T) {
	s := testStore(t)

	// Enqueue three ingest jobs.
	for i, url := range []string{"file:///1.pdf", "file:///2.pdf", "file:///3.pdf"} {
		if _, err := s.Enqueue(url, fmt.Sprintf("doc %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	items, err := s.Activity("", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items; want 3", len(items))
	}
	// enqueued_at DESC: last enqueued should be first.
	if items[0].EnqueuedAt < items[1].EnqueuedAt || items[1].EnqueuedAt < items[2].EnqueuedAt {
		t.Errorf("items not in enqueued_at DESC order: %d, %d, %d",
			items[0].EnqueuedAt, items[1].EnqueuedAt, items[2].EnqueuedAt)
	}
}

func TestActivity_KindFilterNarrowsFeed(t *testing.T) {
	s := testStore(t)

	// One ingest job.
	if _, err := s.Enqueue("file:///a.pdf", "A"); err != nil {
		t.Fatal(err)
	}
	// One identity job (caption mode).
	if err := s.Ingest(context.Background(), Document{
		Path:      "/corpus/doc.pdf",
		Title:     "doc.pdf",
		Fragments: []Fragment{{Page: 1, Ord: 0, Text: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueIdentity("/corpus/doc.pdf", false); err != nil {
		t.Fatal(err)
	}

	// Filter to ingest only.
	ingestItems, err := s.Activity("ingest", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ingestItems) != 1 {
		t.Fatalf("ingest filter: got %d items; want 1", len(ingestItems))
	}
	if ingestItems[0].Kind != ActivityIngest {
		t.Errorf("ingest filter returned kind %q; want ingest", ingestItems[0].Kind)
	}

	// Filter to caption only.
	captionItems, err := s.Activity("caption", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(captionItems) != 1 {
		t.Fatalf("caption filter: got %d items; want 1", len(captionItems))
	}
	if captionItems[0].Kind != ActivityCaption {
		t.Errorf("caption filter returned kind %q; want caption", captionItems[0].Kind)
	}

	// Filter to tags: should be empty.
	tagsItems, err := s.Activity("tags", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(tagsItems) != 0 {
		t.Fatalf("tags filter: got %d items; want 0", len(tagsItems))
	}

	// Filter to extract: should be empty.
	extractItems, err := s.Activity("extract", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(extractItems) != 0 {
		t.Fatalf("extract filter: got %d items; want 0", len(extractItems))
	}
}

func TestActivity_StateFilterComposesWithKind(t *testing.T) {
	s := testStore(t)

	// Enqueue two ingest jobs, finish one.
	j1, err := s.Enqueue("file:///a.pdf", "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("file:///b.pdf", "B"); err != nil {
		t.Fatal(err)
	}
	// Mark job 1 as done.
	if err := s.completeJob(j1, 1, ""); err != nil {
		t.Fatal(err)
	}

	// All items: 2.
	all, err := s.Activity("", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d; want 2", len(all))
	}

	// Filter to done ingest: 1.
	doneItems, err := s.Activity("ingest", "done", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(doneItems) != 1 {
		t.Fatalf("done+ingest: got %d; want 1", len(doneItems))
	}
	if doneItems[0].ID != j1 {
		t.Errorf("done+ingest returned job %d; want %d", doneItems[0].ID, j1)
	}

	// Filter to pending ingest: 1 (the unfinished one).
	pendingItems, err := s.Activity("ingest", "pending", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingItems) != 1 {
		t.Fatalf("pending+ingest: got %d; want 1", len(pendingItems))
	}
	if pendingItems[0].ID == j1 {
		t.Error("pending+ingest returned the finished job")
	}

	// Filter to done caption: 0 (no identity jobs).
	doneCaption, err := s.Activity("caption", "done", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(doneCaption) != 0 {
		t.Fatalf("done+caption: got %d; want 0", len(doneCaption))
	}
}
