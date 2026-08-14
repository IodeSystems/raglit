-- Relational CRUD for the raglit Store. FTS5 search + vector cosine are NOT here
-- (sqlc can't parse fts5); they stay as raw SQL in store.go.

-- ===== documents =====
-- name: GetDocumentByPath :one
SELECT id, path, title, added_at FROM documents WHERE path = ?;

-- name: CountDocuments :one
SELECT COUNT(*) AS n FROM documents;

-- name: UpsertDocument :one
INSERT INTO documents(path, title, added_at) VALUES(?, ?, ?)
ON CONFLICT(path) DO UPDATE SET title=excluded.title, added_at=excluded.added_at
RETURNING id;

-- name: ListDocumentSummaries :many
SELECT d.id, d.path, d.title, d.added_at, d.frag_mode,
       (SELECT COUNT(*) FROM fragments f WHERE f.doc_id = d.id) AS fragments
FROM documents d ORDER BY d.added_at DESC;

-- name: MatchDocumentsLike :many
SELECT path, title FROM documents
WHERE lower(path) LIKE ? OR lower(title) LIKE ?
ORDER BY added_at DESC;

-- ===== fragments =====
-- name: CountFragments :one
SELECT COUNT(*) AS n FROM fragments;

-- name: DeleteFragmentsByDoc :exec
DELETE FROM fragments WHERE doc_id = ?;

-- name: InsertFragment :one
-- origin marks text nobody wrote: 'described' for a model's account of an image
-- (indextext.go), 'identity' for a generated caption (identity.go). Empty is
-- transcription, and the only kind that may be quoted as the record.
INSERT INTO fragments(doc_id, page, ord, text, start_off, end_off, page_spans, origin)
VALUES(?, ?, ?, ?, ?, ?, ?, ?) RETURNING id;

-- name: ListFragmentTextByPage :many
SELECT text FROM fragments WHERE doc_id = ? AND page = ? ORDER BY ord;

-- name: ListFragmentsForDoc :many
SELECT page, ord, text, start_off, end_off FROM fragments WHERE doc_id = ? ORDER BY page, ord;

-- ===== fragment_vectors =====
-- name: InsertVector :exec
INSERT INTO fragment_vectors(fragment_id, dim, vec) VALUES(?, ?, ?);

-- ===== ingest_jobs =====
-- name: EnqueueJob :one
INSERT INTO ingest_jobs(url, title, state, enqueued_at) VALUES(?, ?, 'pending', ?) RETURNING id;

-- name: GetJob :one
SELECT id, url, title, state, error, fragments, mode, enqueued_at, started_at, finished_at, owner_pid, lane
FROM ingest_jobs WHERE id = ?;

-- name: ListJobs :many
-- The projection must carry EVERY column of ingest_jobs. This is scanned into
-- the full-table IngestJob struct through metaquery, which validates the two
-- against each other and refuses a mismatch -- so adding a column to the table
-- and not to this list does not silently drop a field, it breaks the jobs list
-- outright with "shape mismatch: field IngestJob.Lane not in projection".
SELECT id, url, title, state, error, fragments, mode, enqueued_at, started_at, finished_at, owner_pid, lane
FROM ingest_jobs;

-- name: GetOldestPendingJob :one
SELECT id, url, title, enqueued_at FROM ingest_jobs WHERE state='pending' ORDER BY id LIMIT 1;

-- name: GetOldestPendingJobInLane :one
SELECT id, url, title, enqueued_at FROM ingest_jobs
WHERE state='pending' AND lane = ? ORDER BY id LIMIT 1;

-- name: SetJobLane :exec
UPDATE ingest_jobs SET lane = ? WHERE id = ?;

-- name: ListUnlanedPendingJobs :many
SELECT id, url FROM ingest_jobs WHERE state='pending' AND lane = '';

-- name: LaneQueueCounts :many
SELECT lane, state, COUNT(*) AS n FROM ingest_jobs
WHERE state IN ('pending','running') GROUP BY lane, state;

-- name: SetJobRunning :exec
UPDATE ingest_jobs SET state='running', started_at=?, owner_pid=? WHERE id=?;

-- name: ListRunningJobOwners :many
SELECT id, url, owner_pid, started_at FROM ingest_jobs WHERE state='running';

-- name: AbortJob :execrows
UPDATE ingest_jobs SET state='error', error=?, finished_at=?
WHERE id=? AND state='running';

-- name: CompleteJob :exec
UPDATE ingest_jobs SET state='done', fragments=?, mode=?, error='', finished_at=? WHERE id=?;

-- name: FailJob :exec
UPDATE ingest_jobs SET state='error', error=?, finished_at=? WHERE id=?;

-- name: RetryJob :execrows
UPDATE ingest_jobs SET state='pending', error='', started_at=0, finished_at=0, fragments=0
WHERE id=? AND state IN ('error','done');

-- name: CancelJob :execrows
DELETE FROM ingest_jobs WHERE id=? AND state='pending';

-- name: JobStateCounts :many
SELECT state, COUNT(*) AS n FROM ingest_jobs GROUP BY state;

-- name: ListActiveJobs :many
SELECT id, url, state FROM ingest_jobs
WHERE state IN ('running','pending')
ORDER BY CASE state WHEN 'running' THEN 0 ELSE 1 END, id;

-- name: RecentDoneDurations :many
SELECT started_at, finished_at FROM ingest_jobs
WHERE state='done' AND started_at>0 AND finished_at>=started_at
ORDER BY finished_at DESC LIMIT 10;

-- ===== job_stages =====
-- name: InsertStage :exec
INSERT INTO job_stages(job_id, seq, name, engine, state, detail, at) VALUES(?,?,?,?,?,?,?);

-- name: ListJobStages :many
-- ORDER BY id, not seq. A retried job appends a WHOLE SECOND RUN of stages under
-- the same job_id, and seq restarts at 1 each time — so ordering by seq
-- interleaves the runs and the account becomes unreadable. Job 927 on the delano
-- index is four runs, and read as "fetch, fetch, fetch, fetch, extract, extract,
-- …" with no way to tell which failure belonged to which attempt. id is
-- insertion order, which is chronological, which keeps a run together — and it
-- is the only stable key a stage has, since (job_id, seq) names up to four rows.
SELECT id, seq, name, engine, state, detail, at FROM job_stages WHERE job_id = ? ORDER BY id;

-- ===== ocr_pages =====
-- name: UpsertOcrPage :exec
INSERT INTO ocr_pages(doc_id, page, engine, image_path) VALUES(?,?,?,?)
ON CONFLICT(doc_id, page) DO UPDATE SET engine=excluded.engine, image_path=excluded.image_path;

-- name: DeleteOcrPagesByDoc :exec
DELETE FROM ocr_pages WHERE doc_id = ?;

-- name: ListOcrPagesByDoc :many
SELECT page, engine, image_path FROM ocr_pages WHERE doc_id = ? ORDER BY page;

-- name: OcrEngineCountsByDoc :many
SELECT engine, COUNT(*) AS n FROM ocr_pages WHERE doc_id = ? GROUP BY engine;

-- name: GetPageImagePath :one
SELECT p.image_path FROM ocr_pages p JOIN documents d ON d.id = p.doc_id
WHERE d.path = ? AND p.page = ?;

-- ===== tombstones (branch storage) =====
-- name: InsertTombstone :exec
INSERT OR IGNORE INTO tombstones(path) VALUES(?);

-- name: DeleteTombstone :exec
DELETE FROM tombstones WHERE path = ?;

-- name: ListTombstones :many
SELECT path FROM tombstones;

-- name: ListDocumentPaths :many
SELECT path FROM documents;

-- name: DeleteDocumentByPath :exec
DELETE FROM documents WHERE path = ?;

-- ===== content-hash dedup =====
-- name: GetDocumentHash :one
SELECT content_hash FROM documents WHERE path = ?;

-- name: SetDocumentHash :exec
UPDATE documents SET content_hash = ? WHERE path = ?;

-- name: SetDocumentFrag :exec
UPDATE documents SET frag_mode = ?, frag_recipe = ? WHERE id = ?;

-- name: GetDocumentFrag :one
SELECT frag_mode, frag_recipe FROM documents WHERE id = ?;

-- name: ExportFragments :many
SELECT f.page, f.ord, f.text, f.start_off, f.end_off, f.page_spans, fv.vec
FROM fragments f LEFT JOIN fragment_vectors fv ON fv.fragment_id = f.id
WHERE f.doc_id = ? ORDER BY f.page, f.ord;

-- ===== media (figures explained into fragments) =====
-- name: InsertMedia :one
INSERT INTO media(doc_id, page, ord, kind, image_path, bbox, description, fragment_id)
VALUES(?,?,?,?,?,?,?,?) RETURNING id;

-- name: InsertMediaVector :exec
INSERT INTO media_vectors(media_id, dim, vec, space) VALUES(?,?,?,?);

-- name: DeleteMediaByDoc :exec
DELETE FROM media WHERE doc_id = ?;

-- name: ListMediaByDoc :many
SELECT page, ord, kind, image_path, bbox, description, fragment_id
FROM media WHERE doc_id = ? ORDER BY page, ord;

-- name: ListMediaByFragment :many
SELECT page, ord, kind, image_path, bbox, description
FROM media WHERE fragment_id = ? ORDER BY ord;

-- ===== readings =====
-- name: UpsertReading :exec
INSERT INTO readings(source_sha256, source_path, doc_path, method, level, produced_by, ruled_by, at, text, data)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(doc_path) DO UPDATE SET
  source_sha256=excluded.source_sha256, source_path=excluded.source_path,
  method=excluded.method, level=excluded.level, produced_by=excluded.produced_by,
  ruled_by=excluded.ruled_by, at=excluded.at, text=excluded.text, data=excluded.data;

-- name: ListReadingsOfSource :many
SELECT id, source_sha256, source_path, doc_path, method, level, produced_by, ruled_by, at, text, data
FROM readings WHERE source_sha256 = ? AND source_sha256 <> '' ORDER BY at, id;

-- name: GetReadingForDoc :one
SELECT id, source_sha256, source_path, doc_path, method, level, produced_by, ruled_by, at, text, data
FROM readings WHERE doc_path = ?;

-- name: ListAllReadings :many
SELECT id, source_sha256, source_path, doc_path, method, level, produced_by, ruled_by, at, text, data
FROM readings ORDER BY source_sha256, at, id;
