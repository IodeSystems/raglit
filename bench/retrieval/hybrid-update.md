# Update 2026-10-26: cause found, fix on local main (not pushed)

Cause 1 in the body (embed truncation) was WRONG: the window is 9,000 chars and nomic's native context is 8,192 tokens, so fragments were embedded whole. The real cause is fragment SIZE: a 9,000-char fragment (most code files = one fragment) embeds as one blurred vector.

Gitaly + glab subset, 253 queries, MRR:

| window | fragments | vec | hybrid (old fusion) | hybrid (doc fusion) | bm25 |
|---|---|---|---|---|---|
| 9000 | 2,605 | 0.302 | 0.395 | - | 0.608 |
| 4000 | 3,833 | 0.414 | 0.541 | - | 0.597 |
| 2000 | 6,832 | 0.523 | 0.587 | 0.598 | 0.619 |
| 1000 | 14,240 | 0.547 | 0.610 | 0.630 | 0.596 |

At 1000 with doc fusion, hybrid beats bm25 on every metric (success@5 0.80 vs 0.76, recall@20 0.73 vs 0.70).

Commits: 32885e6 (hybrid fuses per document), afb7b80 (default window 1000/750/300), 733e0b1 (eval options). Existing indexes keep old fragments until a file changes or a --fresh re-read.

Pending: the full corpus (monolith + handbook) re-indexed at 1000 to confirm.
