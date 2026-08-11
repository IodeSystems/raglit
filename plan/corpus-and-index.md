# raglit: a corpus of embeddings, and indexes as membership over it

Status: MOSTLY ALREADY BUILT — corrected 2026-08-03 after reading pool.go. Prerequisite for `plan/answered-questions.md`
and for dun's durable memory (`~/inflight/shelf.md`).

## Goal (from user)

> "re-use embeddings so that we have a corpus and an index — (corpus shares
> cache) index provides membership of corpus cache (to prevent re-work) —
> corpus needs some sort of gc for orphaned objects"

## It exists. It is `pool.sqlite`.

Almost all of this was proposed without reading `pool.go`, which had already
built it. Its own comment is the design:

> Shared document pool — cross-index dedup of INDEXING work. The expensive part
> of ingest (extract/OCR/segment/embed) is a pure function of the source bytes
> and the "recipe" (the models + config that shape the output). The pool caches
> that output keyed by (recipe_hash, file_hash), SHARED across every index in a
> daemon.

    pool(recipe_hash, file_hash, payload, created_at, last_used_at)
    PRIMARY KEY (recipe_hash, file_hash)

**And the key is better than the one proposed.** This document worried about
fragment-grain keying: two indexes of one file fragmenting differently would
share nothing. Document grain sidesteps it — a changed model or config is a new
RECIPE and reprocesses; the same recipe on the same bytes replays cached
fragments, vectors and page images with no LLM call at all. `last_used_at` is
already the LRU basis.

**GC exists too**: `Pool.GC(GCPolicy{MaxBytes, MaxEntries, MaxAgeUnused})`,
oldest-accessed first, and it already handles the subtlety that a file's
`pool-pages/` images are shared across its recipe entries and free only when its
LAST entry is evicted. Wired into the daemon on a timer and via HTTP.

So the corpus work is DONE. What follows is what reading it actually turned up.

## What is NOT built: indexes have no lifecycle

The pool is GC'd. The INDEXES are not, and nothing evicts them from memory
either:

- `Registry.getLocked` opens a store once and caches it forever
  (`r.stores[name] = s`). The only removal is `DeleteBranch`, an explicit drop.
  There is no LRU and no idle close, so every index touched in a daemon's
  lifetime stays open at `MaxOpenConns(1)` — measured: 24 sqlite fds for 5
  indexes, ~4.8 each. The fd ceiling here is 1,048,575, so fds are not the
  binding constraint; memory per open store is.
- **On disk an index is a directory**, not a file:
  `indexes/<project>__<index>/` holding `index.sqlite` (+wal +shm), `originals/`
  and `pages/`. `dun__dun-main` is 3.1 MB after ONE ingest of one repo.
  Nothing ever deletes one.

**This is newly urgent because of dun.** dun now names its index
`<directory>-<branch>`, so every worktree on every branch mints a directory that
is never reclaimed — and dun's own shelf records ~13 worktrees a day in this
repo, 1.1 GB of them swept on 2026-08-03. Per-worktree indexes reproduce that
accumulation somewhere new unless the index dies with the worktree.

**next:** the owner of a worktree is the one that knows when it dies, so dun
should drop the index in the same place it removes the worktree (`/close` and
the startup prune both already have the hook). raglit's half is a way to ask —
a delete-index call that is not `DeleteBranch`.

**risk:** deleting an index must NOT touch the pool. The pool is the expensive
part (it cost LLM calls) and is shared; an index is cheap to rebuild FROM it.
Getting that backwards turns a cleanup into a re-embedding bill.

## Scope: project IS the corpus, directory+branch IS the index (USER, 2026-08-03)

    --project  dun                 → corpus-dun.sqlite      the embeddings
    --index    dun-feature-x       → index-dun-feature-x.sqlite   the membership

The usage this is FOR: two worktrees of one repo on two branches. Each gets its
own index, so a search returns this branch's files and not a sibling's — and
both draw on one corpus, so the files they share cost one embedding between
them. Isolation and reuse at once, which is exactly what a single index cannot
give you.

dun needs precisely this. Its plan already records the day pointing raglit at a
workspace root indexed 16 worktree COPIES and every workspace-wide search came
back entirely stale duplicates. Per-worktree indexes make that unreachable; the
corpus is what stops the isolation costing 16× the embeddings.

Corpus per PROJECT, not per home: two unrelated projects sharing a vendored file
will embed it twice. That is the deliberate trade — a shared-everything corpus
is one namespace to corrupt, and cross-project reuse is worth less than
per-project blast radius.

## Where it physically lives: the corpus IS the file (USER, 2026-08-03)

One sqlite file per PROJECT, and the indexes live inside it:

    corpus-dun.sqlite
      corpus(key, model, dim, vec, …)          embedded once, shared
      indexes(name, …)                          dun-main, dun-feature-x
      fragments(…, index_id)                    membership, scoped by index
      documents(…, index_id)

This is simpler than the alternative it replaces, and the simplification is the
argument for it. A corpus in its own file beside `index-<name>.sqlite` would have
to be ATTACHed to every index connection, and ATTACH is per-CONNECTION: it works
today only because `Store.Open` happens to call `db.SetMaxOpenConns(1)`, and
raising that limit later would hand out connections with no corpus attached —
failing as `no such table: corpus.corpus`, intermittently, under concurrency
only. One file has no such coupling to a decision made elsewhere for another
reason.

**What it costs, stated plainly:**

- **Dropping an index is a DELETE, not an unlink.** Today `raglit` removes an
  index by removing a file. It becomes `DELETE … WHERE index_id=?` plus a
  VACUUM to actually reclaim the space, which is slower and needs the write
  lock.
- **One file is one lock.** Two indexes in the same project no longer write
  concurrently. Under the shared daemon that is already true (single writer), so
  the cost lands only on `--embedded` callers.
- **Blast radius is the project.** Corruption took out one index before; now it
  takes the project's indexes and its corpus with them. The corpus is the
  expensive part — it cost LLM calls — so backups matter more than they did.

**Migration.** `Registry` currently discovers indexes by scanning for
`index-*.sqlite` (`registry.go:150`), so both layouts can be read at once: a
project file when present, the legacy per-index files otherwise. Existing
indexes keep working, and are folded in when re-ingested rather than by a flag
day.

## GC

Orphans appear whenever a document is re-ingested, deleted, or withdrawn, so the
corpus only grows without a sweep:

    DELETE FROM corpus WHERE key NOT IN (SELECT corpus_key FROM fragment_vectors)

Mark-and-sweep rather than refcounting, deliberately: a refcount is a second
source of truth that drifts the first time a write path forgets to decrement,
and the sweep is a single query over a column that is already indexed. Run it on
demand (`raglit gc`) and report what it reclaimed — a GC that runs silently is
one nobody can tell is working.

**Open:** whether the sweep is safe to run concurrently with an ingest on the
shared daemon. A key inserted-but-not-yet-linked would look orphaned. Either GC
takes the write lock, or corpus rows carry a `created_at` and the sweep ignores
anything younger than a few minutes. The second is cheaper and racier; the first
is correct and blocks a writer briefly.

## Scope: project IS the corpus, directory+branch IS the index (USER, 2026-08-03)

    --project  dun                 → corpus-dun.sqlite      the embeddings
    --index    dun-feature-x       → index-dun-feature-x.sqlite   the membership

The usage this is FOR: two worktrees of one repo on two branches. Each gets its
own index, so a search returns this branch's files and not a sibling's — and
both draw on one corpus, so the files they share cost one embedding between
them. Isolation and reuse at once, which is exactly what a single index cannot
give you.

dun needs precisely this. Its plan already records the day pointing raglit at a
workspace root indexed 16 worktree COPIES and every workspace-wide search came
back entirely stale duplicates. Per-worktree indexes make that unreachable; the
corpus is what stops the isolation costing 16× the embeddings.

Corpus per PROJECT, not per home: two unrelated projects sharing a vendored file
will embed it twice. That is the deliberate trade — a shared-everything corpus
is one namespace to corrupt, and cross-project reuse is worth less than
per-project blast radius.

## Where it physically lives

An index is ONE SQLITE FILE (`index-<name>.sqlite`, see `home.go`), so a corpus
shared BETWEEN indexes cannot live inside one of them. It is its own database,
`corpus-<project>.sqlite`, joined at query time:

    ATTACH DATABASE '<home>/corpus-<project>.sqlite' AS corpus;

    SELECT …, c.vec
      FROM fragment_vectors fv
      JOIN fragments f ON f.id = fv.fragment_id
      JOIN documents d ON d.id = f.doc_id
      JOIN corpus.corpus c ON c.key = fv.corpus_key

which keeps `VecSearch`'s shape — scan, score in Go — unchanged.

**ATTACH is per-CONNECTION, and that is only safe here by accident of an
existing decision.** `Store.Open` already calls `db.SetMaxOpenConns(1)`, so
there is one connection and the attach persists on it. Raise that limit and the
pool will hand out connections with no corpus attached, and the failure is
`no such table: corpus.corpus` — intermittently, under concurrency only, which
is the worst way to find out. If the single-connection decision is ever revised,
the attach must move into a connect hook in the same change.

## Risks

- **Migration.** Existing indexes have vectors keyed by fragment. The migration
  can be lazy: keep reading `fragment_vectors.vec` when `corpus_key` is null,
  and populate the corpus as documents are re-ingested. No flag day.
- **The cache hides embedder changes.** Changing the configured embed model must
  MISS the whole corpus rather than reuse it. That falls out of keying by model,
  but only if the model name recorded is the one actually called, not the one
  configured — those differ whenever a server aliases or upgrades a model.
- **A corpus row is worth more than a fragment row.** It cost an LLM call. GC
  deleting one that a slow ingest was about to link is the expensive mistake
  here, which is why the concurrency question above is not a detail.

## The index and the transcription want different text

A layout-aware VLM returns markup. The TRANSCRIPTION should keep it — the
bounding boxes are how a quotation is checked against the pixels. The INDEX
should not: measured on the delano corpus 2026-08-10, 413 of 2692 fragments
carried `data-bbox` markup, 40% of their bytes were tags (~1.3 MB indexed,
embedded and searched), and one 1947 deed was 49% tags.

`FlattenForIndex` = `StripLayoutMarkup` + `FlattenMarkdownForIndex`, applied in
`ingestUnits` AFTER the transcription writeback and BEFORE segmentation. The two
halves were written weeks apart for the same reason and NEITHER was wired to
anything; conflating "what the artifact holds" with "what the index holds" is
what left them that way.

Two rules inside the stripper, both learned by getting them wrong:
  - BLOCK tags become a line break, INLINE tags become NOTHING. Replacing every
    tag with a space inserts whitespace the page never had (`Afro-Shirazi ,`) and
    breaks exact-phrase search on perfectly-read text. That cost ~4 points of
    apparent model error on olmOCR-bench before it was found — in the scorer.
  - `<[^>]*>` is WRONG. It matched `< 2 acres and the setback is >` and deleted
    the sentence. Legal and survey prose is full of bare comparisons; the opener
    must require a letter.
  - `<img alt="...">` text is CONTENT and is kept: it is the only text a
    photograph or a barcode ever produces.

### Measured: flattening alone does not fix segmentation

A 2-page 1947 deed, four arms, same cached OCR:

| segmenter | input | segment |
|---|---|---|
| chandra | markup | degraded — no valid JSON |
| Qwen | markup | done, dropped 1184 of 1234 chars |
| chandra | flattened | degraded — no valid JSON |
| **Qwen** | **flattened** | **done, clean, no fallback** |

BOTH were needed. The markup hypothesis alone was wrong: chandra fails to emit
the tool call on clean 3295-char input too. chandra is an OCR model and the
segmenter asks for structured JSON over TEXT — a different job it was never
chosen for. `NewSegmenter(w.OCR.Client)` ties the two together; giving the
segmenter its own model is the remaining fix and now has evidence behind it.

### `segment_model` — reading pixels and emitting JSON are different jobs

`NewSegmenter(w.OCR.Client)` tied the segmenter to whatever `vision_model` was,
so switching the OCR model on 2026-08-05 silently switched the SEGMENTER too.
Config `segment_model` (flag `--segment-model`) now separates them; empty falls
back to the vision model, so an index that never sets it behaves as before.

Verified end-to-end on the 1947 deed: `ocr done (chandra-ocr-2)` →
`flatten done` → `segment done`, page provenance still chandra.

The wiring bug worth remembering: THREE places built a segmenter, and the two on
the paths that matter — `ingestPDF` (every PDF) and the image branch of
`extractAndIngestAs` — each constructed their own from the OCR client, ignoring
`Worker.Segmenter`. Only the email/spreadsheet path used the field. A setting
wired into the struct but bypassed by the main path would have looked inert and
been blamed on the model.

OPEN TRADE-OFF, for the operator not the code: on this fleet Qwen sits on the
5090 with `maxConcurrent 1`, and that slot is what interactive chat needs — the
original reason OCR moved to chandra. Segmentation is ONE text call per page
against OCR's image call per page, so the contention returns at a fraction of
the volume, but it does return.
