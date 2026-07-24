package raglit

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	gen "github.com/iodesystems/raglit/internal/db"
)

// Per-job pipeline stages — the "series of tasks" an ingest item goes through.
// The path depends on the source: a scanned page runs fetch → extract → ocr →
// segment → embed → commit; a code/text file skips ocr (fetch → extract →
// segment → embed → commit); a born-digital PDF page uses its text layer, so its
// pages need no ocr. Each stage is tagged with the engine/mode that handled it
// (text-layer, pandoc, tesseract, vision, llm, offline, …). The worker records
// them as it runs (see worker.go); the review UI shows them per job.

// StageLog records stages for one job. A nil *StageLog is a no-op, so the ingest
// pipeline can be driven with no job (CLI) or with one (the worker/daemon).
type StageLog struct {
	store *Store
	jobID int64
	seq   int
}

// NewStageLog returns a recorder bound to a job. Returns nil for jobID ≤ 0 so
// callers without a job (the CLI IngestPDF path) record nothing.
func (s *Store) NewStageLog(jobID int64) *StageLog {
	if jobID <= 0 {
		return nil
	}
	return &StageLog{store: s, jobID: jobID}
}

// Record appends one stage. Errors are swallowed — stage recording is
// observability, and must never fail an ingest.
func (sl *StageLog) Record(name, engine, state, detail string) {
	if sl == nil {
		return
	}
	sl.seq++
	_ = sl.store.addStage(sl.jobID, sl.seq, name, engine, state, detail)
}

// Done / Skip / Fail are shorthands for the common states.
func (sl *StageLog) Done(name, engine, detail string) { sl.Record(name, engine, "done", detail) }
func (sl *StageLog) Skip(name, detail string)         { sl.Record(name, "", "skipped", detail) }
func (sl *StageLog) Fail(name, engine string, err error) {
	if err != nil {
		sl.Record(name, engine, "error", err.Error())
	}
}

func (s *Store) addStage(jobID int64, seq int, name, engine, state, detail string) error {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	return s.q.InsertStage(context.Background(), gen.InsertStageParams{
		JobID: jobID, Seq: int64(seq), Name: name, Engine: engine, State: state,
		Detail: detail, At: time.Now().UnixNano(),
	})
}

// JobStage is one recorded pipeline step.
type JobStage struct {
	Seq    int    `json:"seq"`
	Name   string `json:"name"`
	Engine string `json:"engine"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	At     int64  `json:"at"`
}

// JobStages returns a job's stages in order.
func (s *Store) JobStages(jobID int64) ([]JobStage, error) {
	rows, err := s.q.ListJobStages(context.Background(), jobID)
	if err != nil {
		return nil, err
	}
	out := make([]JobStage, len(rows))
	for i, r := range rows {
		out[i] = JobStage{Seq: int(r.Seq), Name: r.Name, Engine: r.Engine, State: r.State, Detail: r.Detail, At: r.At}
	}
	return out, nil
}

// engineSummary renders an engine→count map as a stable "vision×2, tesseract×1"
// string for a stage's engine/detail field.
func engineSummary(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s×%d", k, m[k])
	}
	return strings.Join(parts, ", ")
}
