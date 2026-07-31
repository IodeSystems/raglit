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
  end_off   INTEGER NOT NULL DEFAULT 0,
  -- Page boundaries WITHIN this fragment, as JSON [{"off":N,"page":P},...] where
  -- off is a byte offset into `text` at which page P's content begins.
  --
  -- A fragment spans pages: the assembler absorbs a continuation, or a sub-floor
  -- sibling, from the NEXT page into the open fragment and keeps only the start
  -- page in `page`. That made `page` right for a fragment's beginning and wrong
  -- for the rest of it, so a search hit inside one could not be resolved to the
  -- page it is actually on — which is the whole point of storing a page.
  -- Empty means the fragment lies entirely on `page`.
  page_spans TEXT NOT NULL DEFAULT ''
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
  finished_at INTEGER NOT NULL DEFAULT 0,
  -- The process that claimed this job. A 'running' row outlives the process that
  -- owned it: kill the daemon mid-ingest and the row says running forever, so the
  -- queue reports work in flight that nothing is doing. Recording the owner lets
  -- a fresh daemon tell its own jobs from a dead one's and abort the orphans.
  owner_pid   INTEGER NOT NULL DEFAULT 0
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
-- Page-level OCR cache, keyed by the SHA-256 of the PAGE IMAGE.
--
-- An ingest that fails on page 31 of 33 used to discard the thirty pages it had
-- already transcribed, and the retry started again from page 1. Measured on a
-- real corpus: every document over 20 pages failed that way and none ever
-- completed, because ~33 requests is enough exposure that one upstream blip is
-- near-certain, and a blip killed the whole document.
--
-- Keyed on the image bytes rather than on (document, page) deliberately. It is
-- exactly the input the model saw, so it is correct across a renamed or moved
-- file, and it MISSES when the page actually changes — which is when re-reading
-- is the right answer. A page that renders differently is a different page.
-- Per-index settings that are DISCOVERED rather than configured.
--
-- The embed model's input limit is the case this exists for. It is a property of
-- the endpoint, not a preference, and getting it wrong is invisible until a
-- document fails: fragments defaulted to 9000 characters with nothing checking
-- them against what the model would actually accept, and the first symptom was a
-- 500 about batch sizes that named nothing about fragments.
--
-- Keyed by model, so swapping the embed model re-probes instead of reusing a
-- number that was true of a different model.
CREATE TABLE IF NOT EXISTS index_meta (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS ocr_page_cache (
  img_sha    TEXT PRIMARY KEY,
  engine     TEXT NOT NULL DEFAULT '',
  text       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);

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
-- One embedding per figure, so figures are retrievable at figure grain. space
-- records which tower produced it: 'text' (the description, in the SAME space as
-- fragment_vectors — so a text query can cosine against it) or 'image' (a CLIP-
-- style image embedding, a DIFFERENT space — not comparable to a text query, kept
-- for a future image-query path). A text-query figure search uses space='text'.
CREATE TABLE IF NOT EXISTS media_vectors (
  media_id INTEGER PRIMARY KEY REFERENCES media(id) ON DELETE CASCADE,
  dim      INTEGER NOT NULL,
  vec      BLOB NOT NULL,
  space    TEXT NOT NULL DEFAULT 'text'
);
-- Shingle sketches — near-duplicate and containment detection at PAGE grain.
--
-- Stored rather than recomputed for the same reason fragments are: the question
-- "is this upload something we already hold?" is asked on every upload, and
-- answering it by re-folding and re-shingling every indexed document reads the
-- whole corpus each time.
--
-- No migration step is needed and none was added: store.go applies this file with
-- CREATE TABLE IF NOT EXISTS on every Open, so an existing index grows these
-- tables on next use with no rows in them. migrate() exists only for new COLUMNS
-- on existing tables, which ALTER cannot do idempotently. An index that predates
-- this is not wrong, only unsketched — `similar` reports which documents have no
-- sketch rather than treating an absent sketch as "nothing similar", because
-- those two answers lead a person to opposite conclusions.
--
-- PAGE grain, not fragment grain, and not document grain. Document grain cannot
-- express "pages 12-14 of X are pages 1-3 of Y", which is the actionable finding.
-- Fragment grain cannot either: a fragment is a ~9000-character window snapped to
-- the source's own offsets, so the same passage at a different offset in another
-- document falls in a different window. A page is the unit a person cites.
CREATE TABLE IF NOT EXISTS shingle_pages (
  doc_id  INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page    INTEGER NOT NULL,
  chars   INTEGER NOT NULL DEFAULT 0,  -- folded length; tells a blank page from a short one
  -- total is the count of DISTINCT shingles on the page: the denominator of
  -- containment. shingle_index holds only a 1-in-N sample, which makes |A∩B|
  -- estimable and would make |A| unrecoverable, so the size is stored separately.
  total   INTEGER NOT NULL DEFAULT 0,
  sampled INTEGER NOT NULL DEFAULT 0,  -- how many of them are in shingle_index
  -- recipe pins the fold/shingle parameters this row was built under, mirroring
  -- documents.frag_recipe and for the same reason: a width change should mark
  -- exactly the documents that need re-sketching, not invalidate the index.
  recipe  TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (doc_id, page)
);

-- The inverted index: which pages carry a given sampled shingle.
--
-- hash leads the primary key and there is no rowid, so probing by hash is a
-- covering range scan with no secondary index to keep. hash is stored as SQLite's
-- signed INTEGER — a uint64 is written by reinterpreting its bits, and read back
-- the same way. Storing the decimal string instead was tried first and made the
-- table four times larger for no benefit.
CREATE TABLE IF NOT EXISTS shingle_index (
  hash   INTEGER NOT NULL,
  doc_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page   INTEGER NOT NULL,
  PRIMARY KEY (hash, doc_id, page)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS shingle_index_doc ON shingle_index(doc_id);

-- Branch storage: a tombstone marks a PARENT document path as deleted-in-branch,
-- so the parent's version does not show through the branch-over-parent overlay.
-- Present in every index (harmless for non-branch indexes).
CREATE TABLE IF NOT EXISTS tombstones (
  path TEXT PRIMARY KEY
);
