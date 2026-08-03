# raglit: a corpus of embeddings, and indexes as membership over it

Status: PROPOSED 2026-08-03 (USER). Prerequisite for `plan/answered-questions.md`
and for dun's durable memory (`~/inflight/shelf.md`).

## Goal (from user)

> "re-use embeddings so that we have a corpus and an index — (corpus shares
> cache) index provides membership of corpus cache (to prevent re-work) —
> corpus needs some sort of gc for orphaned objects"

## Why — the same text is embedded again and again

`fragment_vectors` is keyed by `fragment_id` with `ON DELETE CASCADE`, so a
vector belongs to one fragment of one document of one index and dies with it.
Three consequences, all paid in embedding calls:

- **Re-ingest re-embeds.** `Ingest` is idempotent by DROPPING a document's
  fragments and replacing them, which cascades the vectors away. A document
  whose text did not change is embedded again anyway.
- **Two indexes never share.** The same vendored file, licence, or README in
  five projects is five identical vectors under five fragment ids.
- **dun pays this every session.** It ingests its whole workspace at startup;
  today into a temp home that is deleted on exit, so every session embeds the
  same repository from scratch.

## The shape

**Corpus** — content-addressed embeddings, one row per distinct (text, embedder).
**Index** — what it already is, plus a membership link into the corpus instead of
a private vector.

    corpus(key PRIMARY KEY, model, dim, vec, bytes, created_at)
    fragment_vectors(fragment_id PRIMARY KEY, corpus_key REFERENCES corpus(key))

Ingest becomes: hash the text, look for (hash, model); on a hit link to it and
embed nothing; on a miss embed once and insert. A re-ingest of unchanged text is
then a membership rewrite with no LLM call at all.

## The trap that must not be got wrong

**The key is (content hash, MODEL), never the hash alone.** A vector is only
meaningful in the space that produced it, and a cache that returns a
`nomic-embed` vector for a `text-embedding-3` index does not fail — it silently
returns plausible neighbours that are wrong. Dimension is not enough of a
discriminator either: two different models at the same dim collide. Model
identity belongs IN the key, not beside it.

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

## Scope

The corpus is per-HOME, not per-index — that is the whole point. One shared
daemon holds one home, N project-namespaced indexes, and one corpus underneath
them all, so identical content across projects is embedded once.

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
