# Full corpus at 1000/750/300 with document fusion (2026-10-26)

24,821 documents, 148,625 fragments (was 31,117 at 9000). 378 queries, top 20.

| mode | success@1 | success@5 | success@10 | recall@20 | MRR (was, at 9000) |
|---|---|---|---|---|---|
| grep | 0.17 | 0.33 | 0.40 | 0.33 | 0.243 |
| bm25 | 0.38 | 0.61 | 0.68 | 0.53 | 0.482 (0.484) |
| vec | 0.28 | 0.49 | 0.56 | 0.43 | 0.371 (0.205) |
| hybrid | 0.38 | 0.62 | 0.69 | 0.55 | 0.486 (0.338) |

The regression is gone: hybrid went from far below bm25 to level with it (+0.15 MRR). On the full corpus hybrid is not clearly ahead of bm25, unlike the Gitaly + glab subset (0.630 vs 0.596): vec is weaker on the monolith's Ruby and YAML. Cross-repo (n=8) and docs-only (n=20) subsets are too small to rank modes.

Next levers if hybrid should beat bm25 here: identity summaries (not measured yet), a code-aware embedding model, and the generated-questions issue.
