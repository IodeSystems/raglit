package raglit

import (
	"context"
	"database/sql"
	"fmt"
)

// What is wrong with this corpus, in one answer.
//
// Every fact here was already recorded — in documents, in ingest_jobs, in the
// stage rows a job leaves behind. What was missing is a place that ASKS. The
// failures this exists to surface all shared one shape: nothing errored where
// anybody was looking. Nine documents were absent from an index for a week
// because their jobs failed and no view listed failed jobs. A page came back as
// one undivided block and the stage said "done". A stage spent an hour absorbing
// backpressure and reported one call's error.
//
// A document with no fragments is the sharpest case and the reason for the
// ordering below: it is IN the index, it has a row, `documents` counts it — and
// it can never be returned by a search. Nothing about the corpus looks wrong
// until somebody searches for a phrase they know is in it.

// ProblemKind names a class of trouble, stable enough to filter on.
type ProblemKind string

const (
	// ProblemNoFragments — indexed and unsearchable. The worst kind: it looks
	// like a document from every angle except the one that matters.
	ProblemNoFragments ProblemKind = "no-fragments"
	// ProblemNoPages — a paged document whose pages were never recorded, so page
	// images, the transcript and every page-level citation have nothing behind
	// them.
	ProblemNoPages ProblemKind = "no-pages"
	// ProblemJobFailed — an ingest job that errored. Carries the stage it died in,
	// because "failed" without a stage sends the reader to the wrong half.
	ProblemJobFailed ProblemKind = "job-failed"
	// ProblemDegraded — the model would not segment a page and it was stored
	// whole. Not an error: the text is there and searchable, but it is one
	// undivided block where retrieval expects fragments.
	ProblemDegraded ProblemKind = "segment-degraded"
	// ProblemRetries — a job that only completed after the endpoint made it fight
	// for it. Not a failure, and the earliest warning that one is coming.
	ProblemRetries ProblemKind = "llm-retries"
	// ProblemWithdrawn — ruled out of the corpus on purpose. Reported so a
	// document that is absent BY DECISION is never mistaken for one that is
	// absent by accident, which is the confusion withdrawal exists to end.
	ProblemWithdrawn ProblemKind = "withdrawn"
)

// Problem is one thing worth a person's attention.
type Problem struct {
	Kind ProblemKind `json:"kind"`
	// Subject is the document path, or the job's target when no document exists.
	Subject string `json:"subject"`
	JobID   int64  `json:"job_id,omitempty"`
	// Stage is where it went wrong, when a stage recorded it.
	Stage string `json:"stage,omitempty"`
	// Detail is the recorded evidence, verbatim — the endpoint's own words where
	// there are any. Summarising it here is how "increase the physical batch
	// size" became "status 500" and cost a week.
	Detail string `json:"detail,omitempty"`
	// Fix is the command that addresses it, ready to run.
	Fix string `json:"fix,omitempty"`
}

// Problems reports everything wrong with this index, worst first.
//
// Ordered by consequence, not by table: a document that cannot be found matters
// more than one that was retried into place. A caller rendering only the first
// screen still sees the things that lose data.
func (s *Store) Problems(ctx context.Context) ([]Problem, error) {
	var out []Problem
	for _, q := range problemQueries {
		ps, err := s.problemsFrom(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("raglit: problems (%s): %w", q.kind, err)
		}
		out = append(out, ps...)
	}
	return out, nil
}

type problemQuery struct {
	kind ProblemKind
	sql  string
	fix  func(p Problem) string
}

// problemQueries are in report order. Each yields (subject, job_id, stage,
// detail); a missing column is selected as an empty value so one scan serves all.
var problemQueries = []problemQuery{
	{
		kind: ProblemNoFragments,
		// A document row with nothing under it. Slices (#pN-M children) are
		// excluded: an unmaterialized slice has no fragments BY DESIGN until
		// somebody builds it, and reporting those as broken buries the real ones.
		sql: `SELECT d.path, 0, '', ''
		        FROM documents d
		       WHERE d.path NOT LIKE '%#p%'
		         AND NOT EXISTS (SELECT 1 FROM fragments f WHERE f.doc_id = d.id)
		       ORDER BY d.path`,
		fix: func(p Problem) string { return "raglit ingest --fresh " + p.Subject },
	},
	{
		kind: ProblemNoPages,
		// Paged formats only. A markdown file having no page rows is not a fault;
		// a scanned PDF having none means the pagify/OCR half never happened.
		sql: `SELECT d.path, 0, '', ''
		        FROM documents d
		       WHERE lower(d.path) LIKE '%.pdf'
		         AND d.path NOT LIKE '%#p%'
		         AND NOT EXISTS (SELECT 1 FROM ocr_pages o WHERE o.doc_id = d.id)
		       ORDER BY d.path`,
		fix: func(p Problem) string { return "raglit reread " + p.Subject },
	},
	{
		kind: ProblemJobFailed,
		// The stage it died in comes from the job's own last errored stage, so the
		// reader is not left guessing between "could not fetch it" and "the model
		// refused it".
		//
		// A failure whose document is now indexed is HISTORY, not a problem, and
		// listing it is how a report stops being read. Caught by this view against
		// its own corpus: forty-one failed jobs, and the documents behind most of
		// them had since been re-ingested successfully — the nine that mattered
		// were buried under thirty-two that did not.
		//
		// The test is the document, not a later job: a job that succeeded proves
		// nothing if its output was withdrawn or replaced, and a document present
		// with fragments is the outcome anybody actually wants. Withdrawn paths are
		// excluded for the same reason — absent on purpose is not absent by
		// accident, and it is reported under its own kind.
		sql: `SELECT j.url, j.id,
		             COALESCE((SELECT s.name FROM job_stages s
		                        WHERE s.job_id = j.id AND s.state = 'error'
		                        ORDER BY s.seq DESC LIMIT 1), ''),
		             j.error
		        FROM ingest_jobs j
		       WHERE j.state = 'error'
		         AND NOT EXISTS (
		               SELECT 1 FROM documents d
		                WHERE d.path = j.url
		                  AND EXISTS (SELECT 1 FROM fragments f WHERE f.doc_id = d.id))
		         AND NOT EXISTS (SELECT 1 FROM withdrawals w WHERE w.path = j.url)
		       ORDER BY j.id DESC`,
		fix: func(p Problem) string { return fmt.Sprintf("raglit retry --match %s", p.Subject) },
	},
	{
		kind: ProblemDegraded,
		sql: `SELECT j.url, j.id, s.name, s.detail
		        FROM job_stages s JOIN ingest_jobs j ON j.id = s.job_id
		       WHERE s.state = 'degraded'
		       ORDER BY s.job_id DESC`,
		fix: func(p Problem) string { return "raglit reread " + p.Subject },
	},
	{
		kind: ProblemRetries,
		sql: `SELECT j.url, j.id, s.name, s.detail
		        FROM job_stages s JOIN ingest_jobs j ON j.id = s.job_id
		       WHERE s.name = 'llm-retries' AND s.state = 'warn'
		       ORDER BY s.job_id DESC`,
	},
	{
		kind: ProblemWithdrawn,
		sql:  `SELECT path, 0, '', reason FROM withdrawals ORDER BY path`,
	},
}

func (s *Store) problemsFrom(ctx context.Context, q problemQuery) ([]Problem, error) {
	rows, err := s.db.QueryContext(ctx, q.sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Problem
	for rows.Next() {
		p := Problem{Kind: q.kind}
		var jobID sql.NullInt64
		if err := rows.Scan(&p.Subject, &jobID, &p.Stage, &p.Detail); err != nil {
			return nil, err
		}
		p.JobID = jobID.Int64
		if q.fix != nil {
			p.Fix = q.fix(p)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
