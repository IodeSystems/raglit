# raglit

## Work items

Work items are aw4 issues in project raglit. This repo has no plan/ directory;
this overrides the global Planning rule. Do not recreate plan/.

- A finding, a next step, a follow-up or a decision for Carl: file it as an
  issue. Run from ~/local/src/iodesystems/aw4 (`--home` is relative):

  ```
  cat > body.txt <<'BODY'
  what you measured, and how
  BODY
  aw4 issue ask "<one line>" --body "$(cat body.txt)" --project raglit --by <you>
  ```

  Write the body with a quoted heredoc. A backtick in an inline `--body` is
  command-substituted and silently lost.
- Design reasoning (why the code is the way it is): `docs/design/`. Those notes
  are history; their status marks are stale.
- The raglit project's aw4 share is 0. aw4 schedules raglit work only when
  valued work in another project depends on it. Do not change the share.

## Leaks

This repo is public. Tracked files must not name a real matter, a real person
from a corpus, or an absolute home path; `TestNoRealMatterDetail` enforces it.
Issue bodies in aw4 are private and may carry operator detail.
