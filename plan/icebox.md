# raglit icebox — deferred, opt-in next-steps

Not scheduled. Pick up explicitly. Each entry carries enough design to resume.

## 1. MCP-as-daemon-client: one shared server, many MCP clients

**Ask (2026-07-23):** Multiple raglit MCP instances will run at once. We want ONE
server daemon that owns LLM usage, the ingest queue, and the indexes; each MCP
instance (started in `serve` mode) becomes a **client to that daemon** so LLM
calls, queueing, and index access are coordinated across instances — not N
processes independently hammering corrallm and opening the same sqlite files.

**Split:** the `.raglit` config file stays **local** (per project/worktree —
endpoint, models, which index to use), but the **index lives on the daemon**,
most likely `~/.raglit/indexes/<some-index>`.

**What already exists (build on):**
- `raglit daemon` already owns the registry + background workers and exposes
  `POST /ingest`, `GET /search`, `GET /status`, `GET /indexes` (+ the review UI).
- `--daemon URL` / `$RAGLIT_DAEMON` already routes `ingest`/`search`/`status`
  from the CLI INTO a daemon (see `daemonIngest`/`daemonGet` in daemon.go).
- So the client wire protocol is partly here; the gap is `serve`.

**Gap / work:**
- `serve` (the MCP server) currently opens the registry DIRECTLY
  (`OpenRegistry(home)`), NOT via a daemon. Make `serve` a THIN MCP front end that
  proxies each tool (search/ingest/index_status/list_indexes/list_documents/
  get_document/ocr) to the daemon over HTTP when a daemon URL is configured
  (`$RAGLIT_DAEMON` / config `daemon_url` / `--daemon`); keep the direct-registry
  path as the no-daemon fallback.
- Add daemon endpoints missing for MCP parity: `list_documents`, `get_document`
  (currently only MCP/`serve`-side + `/api/documents`,`/api/doc` which is
  OCR-only). Unify: the daemon should serve the full read surface.
- Config: add `daemon_url` (+ the index NAME the local project targets) to
  Config; keep models/endpoint local. Index bytes move to the daemon home
  (`~/.raglit/indexes/<name>`), so `.raglit/` locally holds config only, no
  sqlite. Decide precedence: local index (today) vs daemon index (new) — probably
  "daemon if `daemon_url` set, else local".
- Coordination: the daemon is the single writer + the single LLM caller, so
  queueing and corrallm fair-share happen in one place (aligns with corrallm's
  own lane/reservation model). MCP clients never touch sqlite or the LLM directly.
- Auth/bind: daemon currently localhost, no auth. Multi-client on one host is
  fine on loopback; cross-host needs auth + a non-loopback bind (already needed
  for the browser UI — today worked around with `--addr 0.0.0.0`).

## 2. Versioned / branched indexes (worktree-style)

**Ask (2026-07-23):** Be aware of VERSIONED indexes. E.g. a git worktree creates
a **branched index** that has differences between the parent index and the
branch — so we can diff/merge index state along a branch like code.

**Shape to explore:**
- An index gains a lineage: a branch index references a PARENT index and stores
  only its DIFFS (docs added/removed/changed vs parent), so a worktree's index is
  cheap to spin up and reflects only what that branch changed.
- Read path resolves branch-over-parent (branch fragment wins; fall through to
  parent for untouched docs) — copy-on-write at the document grain (documents are
  keyed by path; a changed file re-ingests into the branch layer).
- Operations: `diff` (which docs/fragments differ branch vs parent), `merge`
  (fold branch changes back), `fork` (create a branch from a parent at a point).
- Ties into #1: the daemon owns the index store, so it's the natural place to
  host parent + branch layers and resolve reads. A worktree's local `.raglit`
  config names its branch index; the daemon materializes it over the parent.
- Open questions: grain of versioning (document vs fragment vs whole-index
  snapshot); how vectors/ocr_pages layer; whether to reuse sqlite ATTACH or a
  logical overlay in one file; GC of orphaned branch layers.

**Note:** the existing `isolation: worktree` idea (agents in fresh git
worktrees) is the code side; this is its index-state analogue.
