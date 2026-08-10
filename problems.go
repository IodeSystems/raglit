package raglit

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	// ProblemGenerated — a document that is raglit's OWN output: a transcription
	// or a region record written beside a real document and then indexed as if it
	// were one. Never a judgement call, always a mistake, and self-feeding —
	// indexing a transcription writes a transcription of it.
	ProblemGenerated ProblemKind = "generated-indexed"
	// ProblemWithdrawn — ruled out of the corpus on purpose. Reported so a
	// document that is absent BY DECISION is never mistaken for one that is
	// absent by accident, which is the confusion withdrawal exists to end.
	ProblemWithdrawn ProblemKind = "withdrawn"
	// ProblemMissingFile — an indexed document whose file is no longer at the
	// path the row records.
	//
	// The quietest failure in this list, because search keeps working: the
	// fragments live in SQLite and answer queries exactly as before. What breaks
	// is everything that goes back to the FILE — download the original, re-read
	// it, show a page image, write a reading so it can be reviewed — and each of
	// those fails on its own, far from anything that would name the cause.
	//
	// Found on the corpus this was written against, and only because a review
	// sweep tried to open 355 documents and 43 were not there. Nothing else had
	// noticed. Most were a deliberate archival move (documents/ → legacy/) and
	// several were content-preserving renames whose new path was never ingested,
	// so four court filings existed on disk under better names and were absent
	// from the corpus entirely.
	//
	// Usually RECOVERABLE, which is why the fix is not simply "forget": the bytes
	// are generally still in the tree. 39 of those 43 were found on disk at
	// another path. A row is a pointer, and a moved file breaks the pointer, not
	// the document.
	ProblemMissingFile ProblemKind = "missing-file"
	// ProblemUnreadPage — a page the OCR could not read, in a document that was
	// otherwise indexed. Created deliberately by the ingest salvage: one failed
	// page used to discard the whole document, and now it is recorded as a hole
	// so the rest can be kept. That trade is only sound if the hole is FINDABLE —
	// a partial document nobody knows is partial is the more dangerous object,
	// because search returns it and a reader assumes the absence is the record's.
	ProblemUnreadPage ProblemKind = "page-unread"
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
	// keep drops rows the query cannot exclude, because the test is not in the
	// database. A failed job whose file no longer exists is the case: SQL knows
	// the row, only the filesystem knows whether it still means anything.
	keep func(p Problem) bool
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
		kind: ProblemUnreadPage,
		// Reported per DOCUMENT, not per page: five holes in one scan is one thing
		// to go and look at, and five lines of the same path is how a report stops
		// being read.
		//
		// Withdrawn paths are excluded for the same reason they are elsewhere —
		// absent on purpose is not absent by accident.
		sql: `SELECT d.path, 0, 'ocr',
		             'unread page(s): ' || group_concat(o.page, ' ')
		        FROM ocr_pages o JOIN documents d ON d.id = o.doc_id
		       WHERE o.engine = 'failed'
		         AND NOT EXISTS (SELECT 1 FROM withdrawals w WHERE w.path = d.path)
		       GROUP BY d.id
		       ORDER BY d.path`,
		// reread re-attempts only what has no cached text, so it costs the holes
		// and not the document. When a page fails DETERMINISTICALLY — the
		// repetition guard on a dense table, at temp 0 — reread returns the same
		// refusal, and the page needs `raglit regions` to be read in pieces
		// instead. Both are one command; the cheap one is offered first.
		fix: func(p Problem) string { return "raglit reread " + p.Subject },
	},
	{
		kind: ProblemGenerated,
		// Suffix-matched in SQL rather than in Go so the report costs one query
		// like every other. Kept in step with generatedSuffixes by the test that
		// asserts one query per suffix.
		sql: `SELECT d.path, 0, '', ''
		        FROM documents d
		       WHERE d.path LIKE '%.raglit-transcription.md'
		          OR d.path LIKE '%.raglit-regions.json'
		       ORDER BY d.path`,
		fix: func(p Problem) string { return "raglit forget " + p.Subject },
	},
	{
		kind: ProblemMissingFile,
		// Before no-pages on purpose. That kind's fix is `raglit reread <path>`,
		// which cannot work when there is nothing at the path — a reader who sees
		// "no pages" first is sent to run a command that fails for a reason this
		// row is the only one that states.
		//
		// Three exclusions, each for a different reason. Slices (#pN-M) name a
		// PAGE RANGE of a parent, not a file, so stat can never succeed on one —
		// they are the false positive anybody writing this check hits first.
		// Withdrawn paths are absent on purpose and reported under their own kind.
		// raglit's own generated output is reported as generated-indexed, whose
		// fix is the same `forget`, and listing it twice is noise.
		// The detail is a fixed sentence rather than recorded evidence, because
		// there is none to quote: the finding IS the absence. It says what the
		// report cannot work out — where the file went — so the reader does not
		// read `forget` as "this document is lost".
		sql: `SELECT d.path, 0, '',
		             'The row is a pointer and the file is not at the end of it. ' ||
		             'Usually the file MOVED and is still in the tree: ingest it at its ' ||
		             'new path first, then forget this row. Forgetting alone drops the ' ||
		             'document from the corpus and leaves the copy that still exists unindexed.'
		        FROM documents d
		       WHERE d.path NOT LIKE '%#p%'
		         AND d.path NOT LIKE '%.raglit-transcription.md'
		         AND d.path NOT LIKE '%.raglit-regions.json'
		         AND NOT EXISTS (SELECT 1 FROM withdrawals w WHERE w.path = d.path)
		       ORDER BY d.path`,
		keep: documentFileIsGone,
		// `forget`, and not an ingest of the containing directory. The directory
		// is a guess: the case this was built from moved files from documents/ to
		// legacy/, so re-ingesting the old parent finds nothing. Worse,
		// filepath.Dir of a top-level path is "/", and offering to re-ingest the
		// filesystem root is not a suggestion anybody should be one click from.
		fix: func(p Problem) string { return "raglit forget " + p.Subject },
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
		fix:  func(p Problem) string { return fmt.Sprintf("raglit retry --match %s", p.Subject) },
		keep: jobTargetIsStillReachable,
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
		if q.keep != nil && !q.keep(p) {
			continue
		}
		if q.fix != nil {
			p.Fix = q.fix(p)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// jobTargetIsStillReachable reports whether a failed job's target could even be
// retried — which is what decides whether its failure is a PROBLEM or a dead row.
//
// A rename outlives the row. `raglit retry` has always skipped jobs whose file is
// gone; the health report has to agree, or it reports work nobody can do and an
// empty report stops being achievable. On the corpus this was written against,
// four of the last four "failures" were dead rows: two naming a file that had
// been renamed, two naming a RELATIVE path from the bug that made relative paths
// possible at all.
//
// A relative path is unreachable by construction. It resolves against the
// working directory of whichever process opens it, which is not the one that
// wrote the row — that is the whole reason relative paths are refused at the
// door now. Nothing can retry it, so nothing should be asked to.
//
// A remote URL is kept: it cannot be stat'ed, and a fetch that failed against a
// server is a real failure until somebody says otherwise. Guessing "gone" for
// something unreachable-right-now would hide the case this report is for.
//
// The rows stay in ingest_jobs either way. That table is a log of what was
// attempted; this report is about the state of the corpus, and the two answer
// different questions.
func jobTargetIsStillReachable(p Problem) bool {
	raw := p.Subject
	u, err := url.Parse(raw)
	path := raw
	if err == nil {
		switch u.Scheme {
		case "http", "https":
			return true
		case "file":
			path = fileURLPath(u)
		case "":
			// A bare local path, which is what nearly every row is.
		default:
			return true // an unfamiliar scheme is not ours to call dead
		}
	}
	if !filepath.IsAbs(path) {
		return false
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

// documentFileIsGone reports whether an indexed document's local file is no
// longer where its row says it is.
//
// The mirror of jobTargetIsStillReachable, and it errs the other way on purpose.
// That function decides whether a FAILURE is still worth acting on and keeps
// anything it cannot rule out; this one decides whether to accuse a corpus of
// having lost a document, so it only says so when it actually looked and the
// file was not there.
//
// Hence what is NOT reported. A remote URL cannot be stat'ed, and calling a
// document missing because a server is unreachable would be a false alarm that
// arrives every time the network does. A relative path cannot be resolved from
// here at all — it means whatever the working directory of the process opening
// it happens to be, which is not this one — so it is a broken row of a different
// kind, and guessing at it would put a path in the report that nobody can check.
func documentFileIsGone(p Problem) bool {
	raw := p.Subject
	path := raw
	if u, err := url.Parse(raw); err == nil {
		switch u.Scheme {
		case "http", "https":
			return false
		case "file":
			path = fileURLPath(u)
		case "":
			// A bare local path, which is what nearly every row is.
		default:
			return false
		}
	}
	if !filepath.IsAbs(path) {
		return false
	}
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}
