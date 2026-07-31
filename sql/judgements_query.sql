-- Queries over the judgement database. See sql/judgements.sql for why it is a
-- separate database from the index.

-- ===== relations =====
-- name: UpsertRelation :exec
INSERT INTO doc_relations(a, b, kind, supersedes, note, decided_by, decided_at, relation, coverage)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(a, b) DO UPDATE SET
  kind=excluded.kind, supersedes=excluded.supersedes, note=excluded.note,
  decided_by=excluded.decided_by, decided_at=excluded.decided_at,
  relation=excluded.relation, coverage=excluded.coverage;

-- name: GetRelation :one
SELECT id, a, b, kind, supersedes, note, decided_by, decided_at, relation, coverage
FROM doc_relations WHERE a = ? AND b = ?;

-- name: ListRelations :many
SELECT id, a, b, kind, supersedes, note, decided_by, decided_at, relation, coverage
FROM doc_relations ORDER BY a, b;

-- name: ListRelationsFor :many
SELECT id, a, b, kind, supersedes, note, decided_by, decided_at, relation, coverage
FROM doc_relations WHERE a = ? OR b = ? ORDER BY a, b;

-- name: CountRelationsByKind :many
SELECT kind, COUNT(*) AS n FROM doc_relations GROUP BY kind ORDER BY kind;

-- ===== slices =====
-- name: UpsertSlice :exec
INSERT INTO doc_slices(id, parent, from_page, to_page, title, note, decided_by, decided_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  parent=excluded.parent, from_page=excluded.from_page, to_page=excluded.to_page,
  title=excluded.title, note=excluded.note,
  decided_by=excluded.decided_by, decided_at=excluded.decided_at;

-- name: GetSlice :one
SELECT id, parent, from_page, to_page, title, note, decided_by, decided_at
FROM doc_slices WHERE id = ?;

-- name: ListSlices :many
SELECT id, parent, from_page, to_page, title, note, decided_by, decided_at
FROM doc_slices ORDER BY parent, from_page, to_page;

-- name: ListSlicesOf :many
SELECT id, parent, from_page, to_page, title, note, decided_by, decided_at
FROM doc_slices WHERE parent = ? ORDER BY from_page, to_page;

-- name: ListSliceParents :many
SELECT DISTINCT parent FROM doc_slices ORDER BY parent;

-- name: DeleteSlice :exec
DELETE FROM doc_slices WHERE id = ?;

-- ===== history =====
-- name: AppendJudgementLog :exec
INSERT INTO judgement_log(kind, subject, payload, decided_by, decided_at, logged_at)
VALUES(?, ?, ?, ?, ?, ?);

-- name: ListJudgementHistory :many
SELECT id, kind, subject, payload, decided_by, decided_at, logged_at
FROM judgement_log WHERE kind = ? AND subject = ? ORDER BY id;
