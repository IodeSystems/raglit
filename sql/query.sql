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
SELECT d.id, d.path, d.title, d.added_at,
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
INSERT INTO fragments(doc_id, page, ord, text) VALUES(?, ?, ?, ?) RETURNING id;

-- name: ListFragmentTextByPage :many
SELECT text FROM fragments WHERE doc_id = ? AND page = ? ORDER BY ord;

-- name: ListFragmentsForDoc :many
SELECT page, ord, text FROM fragments WHERE doc_id = ? ORDER BY page, ord;

-- ===== fragment_vectors =====
-- name: InsertVector :exec
INSERT INTO fragment_vectors(fragment_id, dim, vec) VALUES(?, ?, ?);

-- ===== ingest_jobs =====
-- name: EnqueueJob :one
INSERT INTO ingest_jobs(url, title, state, enqueued_at) VALUES(?, ?, 'pending', ?) RETURNING id;

-- name: GetJob :one
SELECT id, url, title, state, error, fragments, mode, enqueued_at, started_at, finished_at
FROM ingest_jobs WHERE id = ?;

-- name: ListJobs :many
SELECT id, url, title, state, error, fragments, mode, enqueued_at, started_at, finished_at
FROM ingest_jobs;

-- name: GetOldestPendingJob :one
SELECT id, url, title, enqueued_at FROM ingest_jobs WHERE state='pending' ORDER BY id LIMIT 1;

-- name: SetJobRunning :exec
UPDATE ingest_jobs SET state='running', started_at=? WHERE id=?;

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
SELECT seq, name, engine, state, detail, at FROM job_stages WHERE job_id = ? ORDER BY seq;

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

-- name: ExportFragments :many
SELECT f.page, f.ord, f.text, fv.vec
FROM fragments f LEFT JOIN fragment_vectors fv ON fv.fragment_id = f.id
WHERE f.doc_id = ? ORDER BY f.page, f.ord;
