-- Judgements: what a PERSON decided about this corpus.
--
-- A separate database from the index, and the separation is the point. The index
-- at ~/.raglit/indexes/<name>/ is derived: it is rebuilt by a reindex, it sits
-- outside the one folder Syncthing replicates, and every project that has a
-- local .raglit/ gitignores it. Nothing in here can be recomputed from the
-- corpus — no reading of the bytes says two deeds are versions of one
-- instrument, or that pages 3-6 of a scan are the record of survey — so it lives
-- beside the documents, where it survives a rebuild and reaches the other
-- machines.
--
-- Append-only in spirit, not in schema. Every table carries who decided and
-- when, and a correction is a new row that supersedes rather than an UPDATE that
-- erases: the question "why did this change?" has to stay answerable over a
-- corpus where one corrected fact is chased through a dozen documents.

-- Document relations: what an overlapping pair IS.
--
-- The pair is ONE fact, so (a, b) is stored normalized with a < b and the
-- uniqueness constraint enforces it. Ruling the same pair from the other
-- direction updates the ruling rather than creating a contradicting second row.
CREATE TABLE IF NOT EXISTS doc_relations (
  id         INTEGER PRIMARY KEY,
  a          TEXT NOT NULL,               -- document path, normalized a < b
  b          TEXT NOT NULL,
  kind       TEXT NOT NULL,               -- copy | version | unrelated
  -- supersedes names a SIDE, so it is not normalized: which side governs is the
  -- entire content of an ordering. NULL means two versions with no ruling on
  -- precedence, which is a real state (two undated drafts), not a missing value.
  supersedes TEXT,
  note       TEXT NOT NULL DEFAULT '',
  decided_by TEXT NOT NULL DEFAULT '',    -- 'raglit' for machine-observed facts
  decided_at TEXT NOT NULL DEFAULT '',
  -- Evidence as it stood when the ruling was made, quoted rather than re-derived.
  relation   TEXT NOT NULL DEFAULT '',
  coverage   REAL NOT NULL DEFAULT 0,
  UNIQUE(a, b)
);
CREATE INDEX IF NOT EXISTS doc_relations_a ON doc_relations(a);
CREATE INDEX IF NOT EXISTS doc_relations_b ON doc_relations(b);

-- Slices: a page range of a bundle that is a document in its own right.
--
-- The bundle is what was filed and is never cut up; a slice declares structure
-- inside it. Pages are the PARENT's own numbering — a child of pages 3-6 has
-- pages 3,4,5,6 and never 1-4 — because a quotation cited from the child has to
-- be checkable against the exhibit as filed.
CREATE TABLE IF NOT EXISTS doc_slices (
  id         TEXT PRIMARY KEY,            -- stable child identity; facts cite it
  parent     TEXT NOT NULL,
  from_page  INTEGER NOT NULL,
  to_page    INTEGER NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  decided_by TEXT NOT NULL DEFAULT '',
  decided_at TEXT NOT NULL DEFAULT '',
  CHECK (from_page >= 1 AND to_page >= from_page)
);
CREATE INDEX IF NOT EXISTS doc_slices_parent ON doc_slices(parent, from_page);

-- Page corrections: what a person read off the page that the machine got wrong.
--
-- A plan sheet's identifiers are the case that forces this. A machine read of
-- the 2022 Halvor record of survey gets two of five recording numbers right and
-- invents a twelve-digit auditor file number; the correct values were read off
-- 200% crops of the native 960 ppi image by a person, and nothing in the bytes
-- can recompute that work.
--
-- It lived in the .raglit-transcription.md file beside the document, which
-- raglit rewrites on every read, and was destroyed twice by ordinary re-reads.
-- So it lives here, and the transcription is RENDERED with corrections applied
-- rather than being a place to keep them.
CREATE TABLE IF NOT EXISTS page_corrections (
  doc        TEXT NOT NULL,               -- document path the correction is for
  page       INTEGER NOT NULL,            -- the document's own page number
  text       TEXT NOT NULL,               -- what the page actually says
  note       TEXT NOT NULL DEFAULT '',    -- how it was established (crop, dpi, magnification)
  corrected_by TEXT NOT NULL DEFAULT '',
  corrected_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (doc, page)
);

-- History: every ruling ever made, including the ones since replaced.
--
-- The live tables answer "what do we believe"; this answers "what did we believe
-- and when did it change". Losing that was the one real cost of moving off an
-- append-only text file, so it is paid back here rather than dropped: a row is
-- written on every write, and nothing deletes from it.
CREATE TABLE IF NOT EXISTS judgement_log (
  id         INTEGER PRIMARY KEY,
  kind       TEXT NOT NULL,               -- 'relation' | 'slice'
  subject    TEXT NOT NULL,               -- pair key, or slice id
  payload    TEXT NOT NULL,               -- the row as JSON, as written
  decided_by TEXT NOT NULL DEFAULT '',
  decided_at TEXT NOT NULL DEFAULT '',
  logged_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS judgement_log_subject ON judgement_log(kind, subject, id);
