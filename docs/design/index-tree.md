# Index contents as a tree — rollups per folder

How this plan works: living plan, see ~/CLAUDE.md "Planning". Status marks:
◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked.

USER 2026-10-22: "why not tree view for index contents w/ rollup stats of
type, size, earliest, latest".

## Decisions (USER 2026-10-22)

- **Dates:** the source file's modified time, recorded at ingest and
  backfilled for files still on disk.
- **Size:** bytes, documents and pages per folder. Bytes are recorded at
  ingest and backfilled the same way.
- **Type:** both breakdowns — file extension and classified kind.
- **Placement:** a List | Tree toggle on the Documents page. A folder row can
  open purge with its glob filled in.

## Design

- `documents.src_size` and `documents.src_mtime` (migrated columns, 0 when
  unknown). A finished ingest job stats its local file. At daemon start, each
  index backfills rows with `src_mtime = 0` whose file is still on disk.
- `/api/documents` rows carry `src_size` and `src_mtime`. The tree is built in
  the browser from rows the page already loads, so there is no new endpoint.
- Rollup per folder: document count, pages, bytes, earliest and latest
  modified time, extension counts and kind counts. Folders start collapsed.

## Slices (✅ 2026-10-22)

1. ✅ Columns, recorded at ingest, backfill, list fields — 847747e.
2. ✅ Tree view with rollups and a per-folder purge link. The link fills in the
   purge pattern on Controls (`?purge=`). Checked in the browser against a
   scratch daemon: folder rollups, date spans, chips, collapsed single-child
   folders, and the purge pattern (5 of 5 documents matched).

**next:** none. Deploy with the rest.

**risks:**
- URLs and uploads have no local file, so they show no size or date.
- A file changed on disk after ingest shows its ingest-time stat until it is
  re-ingested.
- Building the tree in the browser costs one pass over every row. That is fine
  for tens of thousands of documents; it has not been measured above that.

**blocking decisions:** none open.

**optional extensions:**
- A document date from extracted fields, as opposed to the file's mtime.
