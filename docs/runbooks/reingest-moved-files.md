# Runbook: re-ingest renamed/moved files and forget dead document rows

**When to use.** A project's index has document rows whose source files have
moved — a directory was archived to a new location, and/or files were renamed
without re-ingesting — leaving rows that point at paths that no longer exist
on disk. The files themselves are still on disk at the new paths.

**What this does NOT do.** This is not a product change. Every command cited
below already exists and is verified against the code at the commit where this
runbook was written. The runbook documents *order and guard conditions*; it
adds no new behaviour.

---

## Parameters

Fill these in before starting:

| Name | Description |
|------|-------------|
| `INDEX` | The raglit index name (`raglit index --list` to find it). |
| `OLD_DIR` | The old top-level directory of the archived files (the prefix of the dead rows). |
| `NEW_DIR` | The new directory where those files now live. |
| `FILING_1` … `FILING_4` | Old path → new path for each renamed court filing. |

---

## Step 0 — Confirm daemon and index

```bash
raglit index --list
raglit index INDEX status
```

Confirm the index you intend to work on is present and its document count is
what you expect *before* making any change. Record the count; you will check
it again at the end.

---

## Step 1 — Enumerate dead rows (dry-run; removes nothing)

```bash
raglit prune --index INDEX
```

Without `--yes` this is a dry run: it lists every indexed document whose local
source file is missing from disk, grouped by the top-most gone directory.
Nothing is removed.

**Cross-check with the health report (JSON mode).**

```bash
raglit health --index INDEX --json --kind missing-file
```

The text mode of `raglit health` does **not** render the `missing-file` kind —
`healthOrder` in `cmd/raglit/problemscmd.go` omits it. The row is still
computed (`problems.go`, `ProblemMissingFile`) and is reachable only via the
`--json` flag.

**The health list is a subset of the prune list, not the same list.** The
`ProblemMissingFile` SQL excludes paths in the `withdrawals` table
(`problems.go:296`: `AND NOT EXISTS (SELECT 1 FROM withdrawals w WHERE w.path = d.path)`),
whereas `MissingDocuments` (`prune.go:82`) does not. So a deliberately-withdrawn
document whose file is also missing appears in the prune dry-run list but
**not** in the `--json --kind missing-file` list.

Expected relationship:

- Every path in the health list should also appear in the prune list.
- The prune list may have **more** paths than the health list.
- Any path present in the prune list but **absent** from the health list is a
  candidate deliberate withdrawal — that is the case step 2 must decide. Do
  not treat the discrepancy as an error; it is the signal step 2 exists to
  resolve.

**Confirm the prune list matches what you expect.** The paths in the prune
list should be:

- `OLD_DIR/…` — the archived directory (dozens of rows).
- `FILING_1`, `FILING_2`, `FILING_3`, `FILING_4` — the renamed court filings.

If you see paths you do not recognise, stop and investigate before continuing.

---

## Step 2 — Decide forget vs. withdraw per row (the u2 guard)

**Do not run `prune --yes` until this step is complete.** `prune --yes` does
not consult the withdrawals table (`prune.go:82`, `MissingDocuments` filters
only on `fromFileSQL` and a missing source; it does not exclude
`withdrawals.path`). A bulk prune would erase deliberately-withdrawn rows
with no audit trail.

For each dead row, ask: **was this file absent from the index by decision?**

- **Yes** (deliberately excluded, with recorded grounds) → use `withdraw`:

  ```bash
  raglit withdraw --index INDEX --reason "<grounds>" --by <you> <old-path>
  ```

  This records the grounds in the audit trail and makes the ingest path refuse
  the document permanently. Use this when someone ruled the file out of the
  corpus on purpose.

- **No** (the absence is the archival move or an un-re-ingested rename — raglit's
  own doing) → use `forget` in step 4. `raglit forget` records nothing and
  prevents nothing; it is correct for a row that should not have existed.

If *all* dead rows are the result of the move (the assumed case), you may
proceed to the bulk `prune --yes` in step 4. If even one row is a deliberate
withdrawal, handle that row separately with `withdraw` and use `forget` for
the rest.

---

## Step 3 — Ingest the new paths **first**

The `missing-file` problem's own detail text, shown in the health report, is:

> "Usually the file MOVED and is still in the tree: ingest it at its new path
> first, then forget this row. Forgetting alone drops the document from the
> corpus and leaves the copy that still exists unindexed."

Ingest order is the point: forgetting first drops the document and leaves the
copy on disk unindexed.

```bash
# The moved directory:
raglit ingest --index INDEX --wait NEW_DIR

# The four renamed court filings:
raglit ingest --index INDEX --wait FILING_1_new FILING_2_new FILING_3_new FILING_4_new
```

`--wait` blocks until the index queue is empty for those targets.

Add `--fresh` only if a cached ingest result is wrong for a reason the
unchanged-bytes check cannot see (e.g. a fragmenter model upgrade). Do not add
it by default.

**Verify the count rose by the right amount:**

```bash
raglit index INDEX status
```

The document count should have gone up by the number of newly-ingested files.
A quick `raglit search` for a known phrase from one of the moved files
confirms the content is now indexable.

---

## Step 4 — Remove the dead rows

**Per-row** (safe; use when the list is small or mixed):

```bash
raglit forget --index INDEX OLD_DIR/file-1 OLD_DIR/file-2 …
raglit forget --index INDEX FILING_1 FILING_2 FILING_3 FILING_4
```

**Bulk** (only after step 2 confirms no dead row is a deliberate withdrawal):

```bash
raglit prune --index INDEX --yes
```

This removes every document whose local source file is gone, including its
fragments, vectors, pages, fields, caption rows, page images, and any pending
ingest jobs on those files. It also removes orphaned page images and dangling
copies under `originals/`.

---

## Step 5 — Verify

```bash
raglit index INDEX status
```

The document count should now be the original count (recorded in step 0)
minus the dead rows, plus the newly-ingested files — which, for a pure move,
is the same as the original count. The dead rows are gone; the new rows are
present.

```bash
raglit health --index INDEX --json --kind missing-file
```

This should return an empty list (or one with only entries you already
accounted for in step 2).

A search for a known phrase from one of the moved files confirms the content
is reachable at the new path.

---

## What went wrong and why the order matters

| Symptom | Cause |
|---------|-------|
| Document absent from search after `forget` | New path was never ingested. Re-ingest at the new path, then the row is gone; the document was not lost, only the pointer. |
| `prune --yes` removed a deliberately-withdrawn document | `MissingDocuments` does not exclude the withdrawals table. Step 2 must be complete before any bulk removal. |
| `raglit health` (text) shows no `missing-file` rows | `healthOrder` omits that kind. Use `--json --kind missing-file`. |
| Ingest of `NEW_DIR` found nothing | The files are in a subdirectory of `NEW_DIR`, or the path is wrong. Confirm with `ls NEW_DIR/` and re-run with the correct path. |
| Prune dry-run shows more paths than `health --json --kind missing-file` | Expected: `ProblemMissingFile` SQL excludes withdrawn paths (`problems.go:296`); `MissingDocuments` does not. The extra paths are candidate deliberate withdrawals — step 2 decides. |

---

## Code references (verified at base main, 2026-10)

| Claim | Location |
|-------|----------|
| `MissingDocuments` filters on `fromFileSQL` + missing source only; no withdrawals exclusion | `prune.go:82` |
| `Prune(dry, deleteOriginals)` — dry run reports, removes nothing | `prune.go` `Prune` |
| `ProblemMissingFile` kind, detail text: "ingest it at its new path first, then forget this row" | `problems.go:286` |
| `healthOrder` omits `ProblemMissingFile` (text mode does not render it) | `cmd/raglit/problemscmd.go:131` |
| `withdraw` records grounds; ingest path refuses the document permanently | `cmd/raglit/withdrawcmd.go` |
| `forget` records nothing, prevents nothing | `cmd/raglit/forgetcmd.go` |
| `ingest --wait TARGET…` blocks until the index queue is empty | `cmd/raglit/queuecmd.go:73` |
| `ProblemMissingFile` SQL excludes withdrawn paths (`NOT EXISTS (SELECT 1 FROM withdrawals w WHERE w.path = d.path)`) | `problems.go:296` |
