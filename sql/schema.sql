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
  frag_recipe  TEXT NOT NULL DEFAULT '',
  -- What this document IS, as opposed to what its file is called: a caption, a
  -- few sentences of what it covers, and a kind from a closed vocabulary. See
  -- identity.go. The path and title are NOT touched by any of this — a filename
  -- is what everything joins on, and "this file is called X and is actually Y"
  -- is itself a finding, so both are kept and both are shown.
  --
  -- gen_source records WHO said it: 'machine' (a model read the transcript) or
  -- 'person' (someone corrected it). A person's caption is not regenerated.
  gen_name     TEXT NOT NULL DEFAULT '',
  gen_summary  TEXT NOT NULL DEFAULT '',
  gen_kind     TEXT NOT NULL DEFAULT '',
  gen_source   TEXT NOT NULL DEFAULT '',
  gen_model    TEXT NOT NULL DEFAULT '',
  gen_at       INTEGER NOT NULL DEFAULT 0,
  -- The TEXT the caption was written from, hashed.
  --
  -- A caption is downstream of the transcript, so it goes stale when the
  -- transcript changes — and a re-read changes it constantly: a page that was a
  -- signature stamp becomes a purchase and sale agreement, a corrected reading
  -- replaces a misread certificate number. Without this the only detectable
  -- state was "has no caption at all", so a document whose text had been
  -- replaced wholesale kept a summary describing the old one, and nothing could
  -- tell the two apart.
  --
  -- Empty means unknown — a caption written before this column existed. Treated
  -- as stale, because it is exactly as trustworthy as an unverified one.
  gen_text_hash TEXT NOT NULL DEFAULT ''
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
  page_spans TEXT NOT NULL DEFAULT '',
  -- Where this text came from. Empty (the overwhelming default) means the
  -- document's own words. 'identity' means a MODEL wrote it — the generated
  -- caption and summary, indexed so a query for "purchase and sale agreement"
  -- can rank a document whose body never says it.
  --
  -- The column exists so those two can never be confused. A hit on a paraphrase
  -- is not a hit on the instrument, and a person quoting one has quoted nothing,
  -- so search marks it and every path that REASSEMBLES a document (get_document,
  -- TruePages, the transcription export, the pool) filters it out. Adding
  -- generated text to `fragments` without this column would put a machine's
  -- words inside the document's text with nothing able to tell them apart.
  origin TEXT NOT NULL DEFAULT ''
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
  owner_pid   INTEGER NOT NULL DEFAULT 0,
  -- Which scheduling lane runs this job: 'heavy' (vision/OCR, one GPU slot) or
  -- 'light' (everything else). Guessed from the URL at enqueue and corrected by
  -- the worker once it has routed the bytes. See lane.go.
  --
  -- Stored rather than recomputed so the claim is one indexed query per lane,
  -- and so the correction survives: a file that turned out to be a scan is
  -- claimed by the right lane on a retry.
  lane        TEXT NOT NULL DEFAULT ''
);
-- NOTE: the index on (state, lane, id) is created in migrate(), NOT here.
-- This file runs BEFORE migrate on every open, and on an existing database
-- CREATE TABLE IF NOT EXISTS is a no-op — so the table still lacks `lane` at
-- this point and an index naming it fails the whole schema apply. It did: every
-- daemon with an existing index crash-looped on "no such column: lane".
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

-- Page readings: every version of what a page says, newest not overwriting old.
--
-- A machine read is a reading. A person's correction is another reading of the
-- same page, and recording it must not erase what it replaced: the old text is
-- what the index held when a document was cited, it is what a stale quotation
-- elsewhere still matches, and "the OCR read 2008081020 for 200808180120" is
-- itself evidence of how far this sheet can be trusted.
--
-- So readings accumulate as ROWS. Exactly one per (doc,page) is active; the
-- rest are superseded and kept. Projected from raglit-audit.jsonl, which is the
-- record — this table is the queryable view of it, and can be rebuilt.
CREATE TABLE IF NOT EXISTS page_readings (
  id         INTEGER PRIMARY KEY,
  doc        TEXT NOT NULL,
  page       INTEGER NOT NULL,
  seq        INTEGER NOT NULL DEFAULT 0,   -- 1,2,3… order the readings arrived
  text       TEXT NOT NULL,
  source     TEXT NOT NULL DEFAULT '',     -- 'machine' | 'corrected'
  -- WHO read it. source says whether a machine or a person produced the reading;
  -- these say WHICH. A corpus read by three different engines over a year — a
  -- text layer, tesseract, a vision model, and tomorrow a different vision model
  -- — cannot answer "how far do I trust this page" without them, and "machine"
  -- covers all of it equally.
  --
  -- engine is the reader ('vision' | 'tesseract' | 'paddleocr' | 'text-layer');
  -- model is the specific thing behind it (a model id for a VLM, the engine's
  -- own name otherwise). For a person's correction both are empty and read_by
  -- carries the answer.
  engine     TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',     -- how it was established
  read_by    TEXT NOT NULL DEFAULT '',
  read_at    TEXT NOT NULL DEFAULT '',
  active     INTEGER NOT NULL DEFAULT 0,   -- exactly one per (doc,page)
  UNIQUE(doc, page, seq)
);
CREATE INDEX IF NOT EXISTS page_readings_doc ON page_readings(doc, page, seq);
CREATE INDEX IF NOT EXISTS page_readings_active ON page_readings(doc, page) WHERE active = 1;

-- A verdict is a person ruling on one claim a machine made.
--
-- The same discipline as page_readings, one level up: rows accumulate, nothing
-- is rewritten, and the jsonl beside the asset stays the record this projects.
-- A ruling cannot be recomputed, so it has to outlive a reindex and reach the
-- other machines; sqlite is the queryable view and can be rebuilt from the log.
--
-- Keyed by attest's CONTENT-ADDRESSED unit id, not by a page or an offset. That
-- is what makes a re-read safe: a unit's id is a digest of its locator, text and
-- evidence, so changing any of them mints a new unit and the old verdicts stay
-- pinned to units that no longer exist. Those orphans are KEPT and are not
-- rebound. Rebinding would silently carry a confirmation across a change in the
-- words it confirmed, which is the one thing this whole table exists to prevent.
--
-- So "how much of this document is reviewed" is never the row count here. It is
-- a join against the CURRENT reading, and a re-read correctly shows the work as
-- undone rather than inherited.
--
-- No `active` column, unlike page_readings. Entries are not versions of one
-- another: a later `retract` does not replace an earlier `confirmed` row, it is
-- another fact about the same unit, and attest.Resolve folds the ordered log
-- into a state. Storing a winner here would duplicate that logic in SQL and let
-- the two disagree.
CREATE TABLE IF NOT EXISTS attestations (
  id         INTEGER PRIMARY KEY,
  doc        TEXT NOT NULL,
  seq        INTEGER NOT NULL,             -- position in the asset's log, 1,2,3…
  unit       TEXT NOT NULL DEFAULT '',     -- attest unit id; empty for a blanket
  kind       TEXT NOT NULL,                -- confirmed|corrected|affirmed|unclear|unsupported|retract|resegment
  blanket    INTEGER NOT NULL DEFAULT 0,   -- covers every unit unruled earlier in the log
  text       TEXT NOT NULL DEFAULT '',     -- correction; empty means not disputed
  label      TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',     -- the reason
  statement  TEXT NOT NULL DEFAULT '',     -- the terms a blanket affirmation was made under, verbatim
  payload    TEXT NOT NULL DEFAULT '',     -- resegment units/supersedes, as JSON
  ruled_by   TEXT NOT NULL DEFAULT '',     -- the PERSON, self-declared
  auth       TEXT NOT NULL DEFAULT '',     -- the authenticated account, from the host
  ruled_at   TEXT NOT NULL DEFAULT '',     -- RFC3339
  UNIQUE(doc, seq)
);
CREATE INDEX IF NOT EXISTS attestations_doc ON attestations(doc, seq);
CREATE INDEX IF NOT EXISTS attestations_unit ON attestations(doc, unit);

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
  model      TEXT NOT NULL DEFAULT '',   -- the model behind the engine, when it has one
  -- The resolution this page was RENDERED at, which decides what any reader can
  -- possibly see. Measured on a record of survey whose lettering is ~3.6pt: at
  -- 200 the certificate number reads 201364, at 400 it reads 20123169. Recorded
  -- because "why is this page wrong" is unanswerable afterwards without it.
  dpi        INTEGER NOT NULL DEFAULT 0,
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

-- Captioning work, queued and durable (identity.go, identityqueue.go).
--
-- Asking a model what a document IS is one bounded call per document, and a
-- corpus is hundreds of them. Run from a command they live in that command: kill
-- the terminal and the remaining work is gone, and nothing else can see what is
-- outstanding. Run them all at once and they crowd an endpoint that serves two
-- requests at a time — the ingest pipeline is competing for the same two.
--
-- So the work is ROWS. Enqueueing is instant and durable, a worker drains them
-- at the endpoint's real concurrency, and a machine that dies mid-sweep resumes
-- where it stopped rather than starting over.
--
-- One row per path (UNIQUE), re-enqueued by resetting it: two pending captions
-- for one document is one wasted model call, and this is exactly the case where
-- somebody re-runs the sweep because they cannot remember whether it finished.
CREATE TABLE IF NOT EXISTS identity_jobs (
  id          INTEGER PRIMARY KEY,
  path        TEXT NOT NULL UNIQUE,
  state       TEXT NOT NULL DEFAULT 'pending',  -- pending|running|done|error
  force       INTEGER NOT NULL DEFAULT 0,       -- replace a machine caption that exists
  error       TEXT NOT NULL DEFAULT '',
  enqueued_at INTEGER NOT NULL,
  started_at  INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0,
  owner_pid   INTEGER NOT NULL DEFAULT 0        -- the claiming process; see reclaim
);
CREATE INDEX IF NOT EXISTS identity_jobs_state ON identity_jobs(state, id);

-- Branch storage: a tombstone marks a PARENT document path as deleted-in-branch,
-- so the parent's version does not show through the branch-over-parent overlay.
-- Present in every index (harmless for non-branch indexes).
CREATE TABLE IF NOT EXISTS tombstones (
  path TEXT PRIMARY KEY
);

-- Documents ruled OUT of the corpus, with the grounds.
--
-- A projection of doc.withdraw events in raglit-audit.jsonl, which is the source
-- of record; this table exists because the two consumers cannot read the trail.
-- The worker has to refuse a withdrawn path at INGEST — otherwise the next file
-- change re-adds it and the decision lasts until the next sweep — and search has
-- to answer with the reason rather than with nothing.
--
-- The reason is NOT NULL and checked non-empty by the writer. A withdrawal
-- without grounds is a delete with extra steps: the document is gone, nothing
-- says why, and the next reader re-ingests it.
CREATE TABLE IF NOT EXISTS withdrawals (
  path       TEXT PRIMARY KEY,
  reason     TEXT NOT NULL,
  by_who     TEXT NOT NULL DEFAULT '',
  at         TEXT NOT NULL DEFAULT ''
);

-- What a PERSON knows about a document that the machine could not read off it.
--
-- Distinct from the three things that already exist here, and the differences
-- are the reason it is its own table. A document's identity (documents.gen_*)
-- says what it IS. An attestation rules on a claim the machine made. A page
-- reading is a transcription. A note is none of those — it is context somebody
-- carries in their head, and until there was somewhere to put it, it stayed
-- there and left the corpus lying.
--
-- page is NULLABLE: NULL is a note about the whole document, a number anchors it
-- to one page. A bundle is not one thing — "the second exhibit starts here" is
-- about page 40, and filing it against the document loses the only part that
-- made it useful.
--
-- Keyed by doc_id, which SURVIVES A RE-INGEST: ingest upserts documents by path
-- (ON CONFLICT(path) DO UPDATE), so the row id is stable across re-reads and
-- re-fragmentation. The cascade fires only on an explicit DELETE FROM documents
-- — a forget — and that is correct: a note about a document nobody is keeping is
-- a note about nothing.
--
-- Not a projection of an audit trail, unlike attestations and withdrawals. A
-- note is not a ruling and nothing downstream has to replay it, so this table is
-- the record rather than a queryable view of one.
CREATE TABLE IF NOT EXISTS document_notes (
  id         INTEGER PRIMARY KEY,
  doc_id     INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page       INTEGER,                     -- NULL = the whole document
  body       TEXT NOT NULL,
  author     TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT ''     -- RFC3339
);
CREATE INDEX IF NOT EXISTS document_notes_doc ON document_notes(doc_id, page, id);
