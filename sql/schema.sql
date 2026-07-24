-- Canonical raglit index schema. Source of truth for sqlc codegen AND the
-- runtime (store.go applies this same file via //go:embed). FTS5 search and the
-- vector cosine scan are NOT expressed as sqlc queries (sqlc's SQLite parser
-- can't model fts5 virtual tables / MATCH / bm25); those stay as raw SQL.
CREATE TABLE IF NOT EXISTS documents (
  id           INTEGER PRIMARY KEY,
  path         TEXT NOT NULL UNIQUE,
  title        TEXT NOT NULL DEFAULT '',
  added_at     INTEGER NOT NULL,
  content_hash TEXT NOT NULL DEFAULT '',  -- sha256 of the source bytes; skip re-ingest when unchanged
  -- How this DOCUMENT was fragmented (a document property, not a job's): 'text-overlap'
  -- (deterministic overlapping windows) or 'llm-seg' (a VLM already transcribed a
  -- page, so the model segments too). frag_recipe hashes ONLY the fragmentation
  -- inputs (mode + window/stride/floor + figure-prompt version), so a stride change
  -- marks exactly the affected documents stale — narrower than the pool recipe.
  frag_mode    TEXT NOT NULL DEFAULT '',
  frag_recipe  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS fragments (
  id     INTEGER PRIMARY KEY,
  doc_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page   INTEGER NOT NULL DEFAULT 0,
  ord    INTEGER NOT NULL DEFAULT 0,
  text   TEXT NOT NULL,
  -- Half-open [start_off, end_off) byte span into the document's source text.
  -- Set on the text-overlap path (overlapping windows share text, so reassembly
  -- and same-doc dedup need exact spans); 0/0 on llm-seg, where a fragment is
  -- model-emitted text and not a span of anything.
  start_off INTEGER NOT NULL DEFAULT 0,
  end_off   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS fragments_doc ON fragments(doc_id);
CREATE VIRTUAL TABLE IF NOT EXISTS fragments_fts USING fts5(
  text, content='fragments', content_rowid='id', tokenize='porter unicode61'
);
CREATE TRIGGER IF NOT EXISTS fragments_ai AFTER INSERT ON fragments BEGIN
  INSERT INTO fragments_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS fragments_ad AFTER DELETE ON fragments BEGIN
  INSERT INTO fragments_fts(fragments_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER IF NOT EXISTS fragments_au AFTER UPDATE ON fragments BEGIN
  INSERT INTO fragments_fts(fragments_fts, rowid, text) VALUES ('delete', old.id, old.text);
  INSERT INTO fragments_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TABLE IF NOT EXISTS fragment_vectors (
  fragment_id INTEGER PRIMARY KEY REFERENCES fragments(id) ON DELETE CASCADE,
  dim         INTEGER NOT NULL,
  vec         BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS ingest_jobs (
  id          INTEGER PRIMARY KEY,
  url         TEXT NOT NULL,
  title       TEXT NOT NULL DEFAULT '',
  state       TEXT NOT NULL DEFAULT 'pending',
  error       TEXT NOT NULL DEFAULT '',
  fragments   INTEGER NOT NULL DEFAULT 0,
  mode        TEXT NOT NULL DEFAULT '',
  enqueued_at INTEGER NOT NULL,
  started_at  INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ingest_jobs_state ON ingest_jobs(state, id);
CREATE TABLE IF NOT EXISTS job_stages (
  id      INTEGER PRIMARY KEY,
  job_id  INTEGER NOT NULL REFERENCES ingest_jobs(id) ON DELETE CASCADE,
  seq     INTEGER NOT NULL,
  name    TEXT NOT NULL,
  engine  TEXT NOT NULL DEFAULT '',
  state   TEXT NOT NULL DEFAULT 'done',
  detail  TEXT NOT NULL DEFAULT '',
  at      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS job_stages_job ON job_stages(job_id, seq);
CREATE TABLE IF NOT EXISTS ocr_pages (
  doc_id     INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page       INTEGER NOT NULL,
  engine     TEXT NOT NULL DEFAULT '',
  image_path TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (doc_id, page)
);
-- Media objects: figures/diagrams explained into a fragment. A figure's DESCRIPTION
-- rides the surrounding prose inline (so it indexes as text with no new infra); a
-- media row anchors the figure to the fragment holding that description and points
-- at its image, so a search hit arrives with its figures attached. image_path is
-- the figure crop where one exists, else the WHOLE PAGE image (still beats none).
-- bbox is a "x,y,w,h" string (empty when only a page-level image is known).
CREATE TABLE IF NOT EXISTS media (
  id          INTEGER PRIMARY KEY,
  doc_id      INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page        INTEGER NOT NULL DEFAULT 0,
  ord         INTEGER NOT NULL DEFAULT 0,
  kind        TEXT NOT NULL DEFAULT 'figure',
  image_path  TEXT NOT NULL DEFAULT '',
  bbox        TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  fragment_id INTEGER REFERENCES fragments(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS media_doc ON media(doc_id);
CREATE INDEX IF NOT EXISTS media_frag ON media(fragment_id);
-- Branch storage: a tombstone marks a PARENT document path as deleted-in-branch,
-- so the parent's version does not show through the branch-over-parent overlay.
-- Present in every index (harmless for non-branch indexes).
CREATE TABLE IF NOT EXISTS tombstones (
  path TEXT PRIMARY KEY
);
