-- name: GetDocumentByPath :one
SELECT id, path, title, added_at FROM documents WHERE path = ?;

-- name: ListDocuments :many
SELECT id, path, title, added_at FROM documents ORDER BY added_at DESC;

-- name: UpsertDocument :one
INSERT INTO documents(path, title, added_at) VALUES(?, ?, ?)
ON CONFLICT(path) DO UPDATE SET title=excluded.title, added_at=excluded.added_at
RETURNING id;

-- name: DeleteFragmentsByDoc :exec
DELETE FROM fragments WHERE doc_id = ?;

-- name: GetJob :one
SELECT id, url, title, state, error, fragments, mode, enqueued_at, started_at, finished_at
FROM ingest_jobs WHERE id = ?;

-- name: ListJobs :many
SELECT id, url, title, state, error, fragments, mode, enqueued_at, started_at, finished_at
FROM ingest_jobs ORDER BY id DESC LIMIT ?;

-- name: EnqueueJob :one
INSERT INTO ingest_jobs(url, title, state, enqueued_at) VALUES(?, ?, 'pending', ?) RETURNING id;

-- name: ListJobStages :many
SELECT seq, name, engine, state, detail, at FROM job_stages WHERE job_id = ? ORDER BY seq;

-- name: ListOcrPagesByDoc :many
SELECT page, engine, image_path FROM ocr_pages WHERE doc_id = ? ORDER BY page;
