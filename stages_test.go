package raglit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stageNames returns a job's stage names in order, e.g. ["fetch","extract",…].
func stageNames(t *testing.T, s *Store, jobID int64) []string {
	t.Helper()
	stages, err := s.JobStages(jobID)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(stages))
	for i, st := range stages {
		names[i] = st.Name
	}
	return names
}

func enqueueFile(t *testing.T, s *Store, name, body string) (string, int64) {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "file://" + p
	id, err := s.Enqueue(url, "")
	if err != nil {
		t.Fatal(err)
	}
	return url, id
}

func TestWorker_Stages_OfflineMode(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, id := enqueueFile(t, s, "code.go", "package x\n\nfunc A(){}\n\nfunc B(){}")

	// No Segmenter → the dependency-free offline split.
	if _, err := (&Worker{Store: s}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	jobs, _ := s.Jobs("all", 10)
	if len(jobs) != 1 || jobs[0].State != "done" {
		t.Fatalf("job = %+v, want one done job", jobs)
	}
	if jobs[0].Mode != "offline" {
		t.Fatalf("mode = %q, want offline", jobs[0].Mode)
	}
	// A code/text file → fragments directly, no OCR: fetch → extract → segment → commit.
	got := stageNames(t, s, id)
	want := []string{"fetch", "extract", "segment", "commit"}
	if len(got) != len(want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stages = %v, want %v", got, want)
		}
	}
	// The segment stage is tagged offline.
	stages, _ := s.JobStages(id)
	if seg := stages[2]; seg.Name != "segment" || seg.Engine != "offline" {
		t.Fatalf("segment stage = %+v, want engine offline", seg)
	}
}

func TestWorker_Stages_LLMMode(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, id := enqueueFile(t, s, "notes.md", "some prose that the segmenter will chunk")

	w := &Worker{Store: s, Segmenter: NewSegmenter(&scriptChatter{replies: []string{
		`{"continues_previous":false,"fragments":[{"text":"one coherent chunk of prose"}]}`,
	}})}
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	jobs, _ := s.Jobs("all", 10)
	if jobs[0].Mode != "llm" {
		t.Fatalf("mode = %q, want llm", jobs[0].Mode)
	}
	stages, _ := s.JobStages(id)
	var seg *JobStage
	for i := range stages {
		if stages[i].Name == "segment" {
			seg = &stages[i]
		}
	}
	if seg == nil || seg.Engine != "llm" || seg.State != "done" {
		t.Fatalf("segment stage = %+v, want engine llm done", seg)
	}
}

// TestIngestUnits_OCRSplit_RecordsEngine covers the split: an image unit is OCR'd
// to text FIRST (recording the real cascade engine as the page's provenance),
// then segmented — two distinct tasks.
func TestIngestUnits_OCRSplit_RecordsEngine(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	// Vision-only OCR: PageWithEngine → engine "vision", returning transcribed text.
	ocr := NewOCR(&scriptChatter{replies: []string{"transcribed page text about tokens"}})
	sg := NewSegmenter(&scriptChatter{replies: []string{
		`{"continues_previous":false,"fragments":[{"text":"a coherent fragment about tokens"}]}`,
	}})
	id, _ := s.Enqueue("scan.png", "")
	sl := s.NewStageLog(id)

	units := []ingestUnit{{page: 1, mime: "image/png", data: []byte{0x89, 'P', 'N', 'G'}}}
	n, err := s.ingestUnits(ctx, sg, ocr, "scan.png", "Scan", units, sl)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	// Provenance records the OCR engine actually used (vision), not a fixed tag.
	_, pages, _ := s.DocReview("scan.png")
	if len(pages) != 1 || pages[0].Engine != "vision" || !pages[0].Vision {
		t.Fatalf("page provenance = %+v, want engine vision", pages)
	}
	// Stages include a distinct ocr task then a segment task.
	got := stageNames(t, s, id)
	if !contains(got, "ocr") || !contains(got, "segment") {
		t.Fatalf("stages = %v, want both ocr and segment", got)
	}
	// ocr must come before segment.
	if idxOf(got, "ocr") > idxOf(got, "segment") {
		t.Fatalf("ocr should precede segment: %v", got)
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
func idxOf(ss []string, v string) int {
	for i, s := range ss {
		if s == v {
			return i
		}
	}
	return -1
}
