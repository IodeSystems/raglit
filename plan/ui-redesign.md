# raglit UI: a map first, then nav, styling, menus, search, metrics, activity

Status: ◐ planning, started 2026-08-19. Living doc — prune as slices land.
Supersedes the STYLING and NAVIGATION halves of `spa-ui.md`, which stays as the
record of how the SPA and its routing were built. `review-ui.md` and
`daemon-ui.md` stay as the record of the panes.

How this plan works: see `~/CLAUDE.md` § Planning. Status marks: ◻ todo ·
◐ in progress · ✅ done · ⏸ parked · ❓ blocked.

## Goal (from user)

> "we need to drastically work on the ui for raglit, its kind of a disaster.
> perhaps we need to make an organized ui map, and reimplement nav, styling,
> menus, search, metrics, activity"

## The diagnosis, measured rather than asserted

Walked the live daemon at :7420 against the delano index (656 documents) on
2026-08-19. The styling is the least of it.

### 1. The UI is a GENERATION behind the daemon

**It reads 20 of the daemon's 41 endpoints.** Everything shipped since the SPA
was built has no surface at all:

| shipped and working | UI |
|---|---|
| `/api/doc-types`, `/api/fields` — schemaed documents | none |
| `/api/identity-jobs`, `/api/identify/queue` — the captioning queue | none |
| the index digest / about (`list_indexes` `covers`) | none |
| content tags, role tags, `kind` | none |
| `/api/relations`, `/api/similar/build`, `/api/slices` | none |
| `/api/withdrawals`, `/api/pool`, `/api/watch`, `/api/tools` | none |

`/api/identify` is consumed for exactly one thing: the Re-title button. The
caption, the summary and the model that wrote them render in the document
header; the tags and the type do not render anywhere.

**This is the finding.** A restyle that does not close this gap makes a prettier
window onto a third of the system.

### 2. The document list throws away everything raglit knows

656 rows, each: filename, the FULL absolute path underneath it, and "N frags".
Every path shares the same ~60-character prefix, so the subtitle is noise in the
literal sense — it carries almost no information per row. No kind, no tags, no
doc type, no caption-vs-filename distinction, no coverage, no sort, no facets,
no grouping. The one column present (`frags`) is an implementation detail.

The corpus knows what each document IS. The list shows what the filesystem
called it — which is the exact failure `document-identity.md` was built to fix.

### 3. Metrics are seven identical cards for three different kinds of number

`656 DOCUMENTS · 2013 FRAGMENTS · 0 PENDING · 0 RUNNING · 2334 DONE · 22 FAILED
· — JOBS/MIN`, all the same size, weight and colour, seventh wrapping alone onto
its own row. Corpus SIZE, queue STATE and a RATE are three unrelated questions
rendered as one row of tiles. On the root page the same treatment puts **6409
FAILED** beside "5 PROJECTS" in identical grey — the most alarming number in the
system, styled as trivia.

And nothing anywhere reports COVERAGE — how much of the corpus is captioned,
tagged, typed, extracted, attested. The backend computes all of it
(`FieldsCoverage`, `identify --list`, the digest). It is the question a corpus
owner actually has, and no screen answers it.

### 4. There is no activity, only a job table

`Ingest jobs` is a dense grid whose right-hand columns run off the viewport and
whose target paths are ellipsized in the MIDDLE — so every row shows the shared
directory prefix and hides the filename. Backwards.

Worse, it is one of TWO queues. `identity_jobs` — captioning, tags, extraction —
is invisible. During the FDA run the terminal reported "50 pending" while the
UI's own job view showed nothing outstanding, because they are different tables.

### 5. The header spends a fifth of the viewport saying where you are

Logo, "review", four breadcrumb elements, a raw `<select>` naming the index in
full (`delano-v-mckinnon__default`) and a search box that WRAPS onto its own row
at 900px — while the breadcrumb immediately to its left already says
`delano-v-mckinnon / default`. The same fact, twice, in two idioms, costing a
row of vertical space on every page.

### 6. Seven flat tabs with no hierarchy and no state

`Dashboard · Health · Ingest jobs · Documents · Search · Branches · Review`.
Corpus, machinery and workflow at one level, alphabetically unordered and
unweighted. A `.tabcount` badge exists in the CSS for exactly this and is unused
on an index carrying 22 failures — so Health reads the same whether the corpus
is clean or on fire.

### 7. The styling is a ported `<style>` block, not a system

`styles.css` says so in its own first line: "Ported verbatim from
cmd/raglit/ui.html". 300 lines, one flat sheet, ~90 hand-named classes
(`.probgroup`, `.msgfrom`, `.idxchip`, `.branchmark`), no scale, no spacing
system, no component vocabulary. It already carries one fix for a variable a
dozen rules read and nothing defined (`--fg`). It is a stylesheet a person edits
by searching for a class name.

**The one pane that works is Health.** It states the problem in a sentence
("The document is not in the index and nothing said so"), names the stage, and
offers Retry / Forget plus a copyable CLI line. Every other pane should be held
to that standard, and the redesign should take it as the model rather than
restyle it.

## The map

### Today

    /                        overview: 7 cards, project list
    /p/:project              project: index list
    /i/:index                dashboard: 7 cards + model channels essay
      /health                problems (the good one)
      /jobs[/:id]            ingest queue only
      /d                     656 flat rows
      /d/:doc/{pages,transcript,seen,history,notes}
      /search?q=&mode=
      /branches
      /attest[/a/:asset]

### Proposed

Three scopes, and the nav says which one you are in.

    DAEMON    /                overview — every project, machine health, pool
    PROJECT   /p/:project      indexes, shared config, watches
    INDEX     /i/:index        the workspace, grouped:

      CORPUS      Documents · Search · Types & Fields* · Relations*
      WORK        Activity* · Problems
      INDEX       About* · Hint* · Branches · Review

`*` = no UI today.

- **Types & Fields** — the registered `doc_types`, each with coverage
  (resolved / extracted / STALE), the schema, and the extraction on a document.
  This is the whole schemaed-documents feature, currently CLI-only.
- **Relations** — `similar`, `marks`, `slices`: what is a copy of what, and
  which bundle a document was cut from.
- **Activity** — ONE timeline over both queues (see slice 6).
- **About / Hint** — what this index holds, counted; and the prose the owner
  tells every model. The hint is editable text that changes how the corpus is
  READ — it belongs on screen with that warning attached, not only behind
  `raglit hint --set`.

## Decisions taken 2026-08-19 (USER)

- **MUI**, and the nav is a **hierarchical drawer**. Missing surfaces first,
  restyle second.
- **`web/dist` is no longer committed.** It was, so that `go install …@latest`
  would serve a UI on a machine with no node; MUI multiplies its size and the
  trade stopped being worth it. Consequences, all handled in `web/embed.go`:
  - `dist/.gitkeep` IS committed. `//go:embed` of a missing or empty directory
    does not compile, so a clean checkout needs one file there. `emptyOutDir`
    deletes it on every build, so the build script writes it back — the comment
    in `vite.config.ts` says not to drop that step.
  - `Dist()` serves a page that SAYS the UI was not built, rather than a blank
    one or a 404. `Built()` lets a caller warn at startup.
  - **Deliberately NOT behind a build tag.** `rebuildRaglit`
    (`cmd/raglit/selfbuild.go:89`) re-invokes `go build` with no tags whenever
    the tree changes, so a tagged embed would silently drop the UI out of the
    binary every time it rebuilt itself. Tagless means the worst case is an
    honest page.
  - `make release` now depends on `web`; `build`/`install` deliberately do not,
    so a Go-only change never shells out to npm.

## Slices

### ✅ 0. The shell, and the two missing surfaces

Landed 2026-08-19. `go build`, `go test ./...` and `tsc -b` all green.

- **`theme.ts`** — one place colour is decided. The palette carries over from the
  sheet it replaces because the semantic roles were already right and already
  meant something here: `ok/warn/err` for state, `run` for work in flight,
  `vision` for a model-backed lane. Dense defaults (`size="small"` on tables,
  buttons, chips) because this tool is for looking at thousands of rows and
  MUI's comfortable defaults fit half as many on a screen.
- **`NavDrawer.tsx`** — Corpus / Work / Index, the three questions the seven flat
  tabs ran together. Active state is derived from the real pathname, not from
  `activeProps`, because `/i/:index` is a prefix of every other entry — the trap
  `spa-ui.md` already recorded on the tab bar.
- **`IndexShell.tsx`** — one scope switcher instead of a breadcrumb AND a picker
  saying the same thing side by side, and the search box no longer wraps.
- **`Types.tsx`** — the schemaed-documents feature, previously CLI-only: each
  registered type with its coverage bar, `stale` counted APART from extracted,
  its field names, and its reading instructions. An index with no types gets the
  two commands that author one.
- **`Activity.tsx`** — the identity queue: caption, tags and extraction jobs with
  their mode, duration and verbatim error.

**A stylesheet trap found and fixed on the way.** MUI's `AppBar` renders a
`<header>` and the content region is a `<main>`, and `styles.css` still carried
bare-element layout rules from the page it replaced (`main { max-width:1200px;
margin:0 auto }`) — so the old sheet centred and crushed the new shell. Those
rules are now `.pageheader` / `.pagemain`, which the unconverted scopes
(`Overview`, `ProjectShell`) opt into until they are converted.

**Bundle: 613 kB raw / 193 kB gzipped**, up from ~340 kB raw. Vite warns past
500 kB; code-splitting is worth nothing inside a binary-embedded bundle, so the
answer is either to accept it or to raise the warning limit deliberately.

### ✅ 0a. What Activity found in its first minute

The pane exists to make the second queue visible, and the first thing it showed
was that **290 of 817 identity jobs on the delano index have failed — 35%** —
with nothing anywhere reporting it.

| count | failure |
|---|---|
| 239 | `arguments are not a JSON object: invalid character 'X' after top-level value` |
| 25 | `"kind" must be exactly one of: …` |
| 4 | `"summary" says nothing that distinguishes this document` |
| 4 | `"name" is 342 characters — keep it under 200` |
| 3 | `invalid character '\n' in string literal` |

239 of them are ONE bug, and it is the same one that stopped the FDA run
(`plan/schema-ingest.md` §4a) from the opposite end: there, junk BEFORE the JSON
(`'/' looking for beginning of value`); here, content AFTER a complete object.
`extractJSON` is fragile at both ends and that single function accounts for both
corpora. The remaining rows are the fix loop working and running out of attempts.

**This is the argument for closing the §1 gap before restyling anything**: a
third of this index's captions have been failing silently, and the UI could not
have said so.

**✅ FIXED the same day.** `extractJSON` (segment.go) took the first `{` to the
LAST `}` — while its own comment claimed it took the first object. Any reply
carrying a second object or a trailing note produced a span holding both. It now
walks to the end of the first BALANCED object, string-aware so a brace inside a
quoted value is content rather than structure, preferring the first candidate
that parses. Tests cover the shapes that actually failed rather than invented
ones, and an unclosed fence no longer truncates a reply that merely mentions one.
Shared with the segmenter, which had the same exposure.

**Delano's 290 are NOT retried by this.** They are terminal rows in
`identity_jobs`; the fix changes what happens next time, not what is recorded.
Clearing them needs `raglit identify --force` against a daemon restarted on the
current binary — a decision about the user's running service, not something to
do silently.

### ◻ 1. Design tokens and a component vocabulary

Replace the ported sheet with a token layer (space, size, weight, radius,
colour, and the semantic roles already implied: `ok/warn/err/run/vision`) and a
small set of components — Panel, Table, Card, Badge, Toolbar, EmptyState,
Explain — that the panes compose instead of each inventing classes.

- **next**: settle the styling approach (BLOCKING, below), then port ONE pane
  end to end (Documents) to prove the vocabulary before converting the rest.
- **risks**: `web/dist` is COMMITTED and `go install` must work with no node
  (`spa-ui.md`). Anything added to the build must survive that constraint.

### ◻ 2. Nav, menus, and where you are

Grouped nav per the map; scope switcher that is not a raw `<select>`; the
breadcrumb OR the picker, not both; counts on the groups that carry problems.

- **next**: decide the nav shape (BLOCKING, below).
- **assumption to check**: that a sidebar is affordable. This is a local tool on
  a wide screen, not a phone app — but Pages and Review want the width.

### ◻ 3. Metrics that answer a question

Three bands, visually distinct:

- **Size** — documents, pages, fragments.
- **Coverage** — captioned / tagged / typed / extracted / attested, as
  proportions of the corpus, with the shortfall linking to the work that closes
  it. Nothing shows this today and it is the most useful screen in the redesign.
- **Work** — pending, running, failed, rate, model channels.

Failure is never a neutral tile: 22 failed and 6409 failed must not render like
a document count.

- **next**: `FieldsCoverage` and the identity coverage counts already exist
  server-side; check whether one endpoint can serve the whole band or whether
  this needs a new `/api/coverage`.

### ◻ 4. Search worth using

One search surface. Mode and scope beside the box that runs the query, not in
the results header. Facets from what the corpus knows: kind, role tag, content
tag, doc type, origin. Hits GROUPED by document — today one document appears as
four unrelated rows. Generated text marked in the list (`.origin` /
`.snip.generated` already exist and are right).

- **next**: confirm the search endpoint can return facet counts, or scope this
  to client-side faceting over the returned page first.

### ◻ 5. The document list as a corpus view

Caption first, filename second and marked when they disagree, kind and tags as
chips, doc type when resolved, coverage marks. Sort and facet. The absolute path
belongs in a tooltip or a detail row, not under all 656 rows.

### ◻ 6. Activity — one timeline over both queues

`ingest_jobs` and `identity_jobs` are two tables and one question: what is this
index doing, and what did it just do. One feed, filterable by kind
(ingest / caption / tags / extract), each entry naming its document, stage,
duration and outcome, with the retry/cancel actions inline.

- **why it matters**: during the FDA run the CLI reported 50 captioning jobs
  pending while the UI's job view showed an idle index. Both were "right".
- **next**: `/api/identity-jobs` exists; check whether the two can be merged
  server-side into one ordered feed or whether the client interleaves them.

## Blocking decisions (USER owns)

1. **Styling approach.** Tokens + hand-written CSS (no new deps, smallest
   change, keeps the committed-dist constraint trivially) · CSS Modules
   (scoping, still no runtime) · Tailwind (fastest to build in, biggest change
   to the tree and the build) · a component library (most opinionated, largest
   dependency surface for a tool that must `go install` cleanly).
2. **Nav shape.** Grouped top tabs · left sidebar · sidebar + command palette.
3. **Scope and order.** Whether this is a restyle of existing panes first, or
   the missing surfaces (Types & Fields, Activity, Coverage) first. They pull in
   opposite directions: the restyle makes what exists better, the new surfaces
   make the UI cover the product. **The gap in §1 is the more serious problem.**

## Risks

- **`web/dist` is committed.** Every build change must keep `go install` working
  with no node present, and a redesign churns `dist` on every commit.
- **The daemon at :7420 is older than the branch** (`4a09e16` vs `257ff03`), so
  new endpoints will 404 against a running daemon until it is restarted on the
  current binary. Any new pane needs to degrade rather than blank the page.
- **Rebuilding nav touches every route.** `spa-ui.md` records a routing trap
  already paid for once (double-encoded document paths). Do not re-litigate the
  URL scheme while changing the chrome — they are separate changes and only one
  of them is reversible cheaply.
