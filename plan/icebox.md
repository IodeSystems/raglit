# raglit icebox — deferred, opt-in next-steps

Not scheduled. Pick up explicitly. Each entry carries enough design to resume.

## ◐ REQUEST from dun (USER, 2026-10-19): daemon auth — OIDC

Written by the dun session; no raglit session was live to tell directly. Design
is raglit's to own — this states the need, not the solution.

**Need.** dun will keep memories in raglit indexes on a remote, shared daemon
(`dun/plan/plan.md` item 9). Memories are PRIVATE by default and shared only
with an explicit `public:true`. Today the daemon has no auth, owner or
visibility field (checked 2026-10-19: none in `plan/*.md`), so any client that
can reach it reads every index — private memory cannot be remote until this
exists.

**USER preference: OIDC.**

What dun needs from it:
- An authenticated principal on every request (OIDC bearer token).
- Per-index (or per-branch) owner + visibility: private (owner only) vs public.
  dun plans "private × project" as a raglit BRANCH of the project index (P6
  overlay: reads see parent + branch, writes go to the branch) — so a private
  branch over a public parent must work: the owner reads both, others read
  only the parent.
- Search that only returns what the principal may read — enforced in the
  daemon, not filtered by the client.
- The principal flows into attest's `Auth` (attest.md:67 already says the host
  owns auth and `Auth` is the resolved principal) — so this fills a slot that
  exists rather than adding a second identity.

Open for raglit to decide: token validation (JWKS) vs a proxy in front; how a
headless client (dun daemon, CI) gets a token; whether visibility is per index,
per branch, or per document.

### raglit proposal (2026-10-19) — not started; blocking decisions below

**Validation: in the daemon, JWKS** (`coreos/go-oidc` v3: issuer discovery,
signature, `exp`, `aud`). Not a proxy: the daemon binds a LIST of addresses
(`listenlist.go`), and a trusted `X-Forwarded-User` header is forgeable from
every bind the proxy does not sit in front of. Config: `auth.oidc.issuer`,
`auth.oidc.audience`. Principal = `<iss>#<sub>` (sub alone collides across
issuers; email is mutable). Unset → today's behavior, no auth.

**Every request, one middleware** on the chi router before humachi/gat/SPA/
page-image/attest: no valid bearer → 401, except `/api/health`. The principal
goes into the request ctx.

**Headless tokens.** Client reads a bearer from `RAGLIT_TOKEN`, else runs
`auth.token_command` (git-credential-helper shape: prints a token; re-run on
401). dun daemon / CI: IdP client-credentials grant behind that command.
Interactive `raglit login` (device-code flow) = optional extension.

**Visibility grain: per index** (a branch IS an index in the Registry).
Per-document rejected: every read path (FTS, vec, figures, docs, pages,
readings, attest, page images, GraphQL) would need its own filter; one missed
path leaks. Per-index = one gate at index lookup.
- Sidecar `<index home>/acl.json`: `{owner, visibility: private|public}`.
- read(i) = (public ∨ owner = p) ∧ read(parent(i)) recursively.
- write(i) = owner = p. fork(parent) needs read(parent); new branch owner = p,
  default `private`.
- dun's case: private branch B over public project P. Owner searches B → B+P
  (overlay). Others: search B → 404, search P → P only. ✔
- Unreadable index → 404, not 403 (same rule attest's authorize uses).
- Multi-index search / listIndexes / listProjects / listBranches: filter the
  index set by read BEFORE fan-out.

**Enforcement shape.** Handlers take a principal-scoped registry view
(`reg.As(p).Get(name)` → not-found when unreadable) instead of `reg.Get`, so
an unscoped call is visible in review. ~50 ops in `cmd/raglit/httpd.go` + the
plain routes + `mountAttest`.

**attest.** New `attest.Identity` impl: `Principal` = ctx principal (replaces
`attest.Guest{}` at `attestmount.go:58` when auth is on); `Can(asset, perm)` =
read/write of the asset's index.

**Risks.**
- Plain routes (SPA, page image) and the gat GraphQL/gRPC surface bypass huma —
  the middleware must sit on the router, and a test must hit each surface
  unauthenticated.
- Branch overlay is incomplete: VecSearch + DocReview see the branch layer
  only (`branch.go:19`, daemon-stack.md P6). dun's private-branch search would
  miss parent VECTOR hits. Separate fix; dun depends on it.
- Watcher / queue workers act with no principal — they must act as the index
  owner, not as "anyone".
- Daemon-local MCP (`raglit serve` client) must forward the bearer.

**Decisions (USER, 2026-10-19).**
1. Index with no `acl.json` → owned by configured `auth.admin` principal,
   private.
2. Loopback binds require a token too — one rule for every bind.
3. write(public index) = owner only; others fork a branch.
4. IdP: Zitadel (user already runs it). Headless = Zitadel machine user,
   client-credentials (or JWT-profile key) behind `token_command`. Tests use an
   in-process fake issuer; Zitadel is the documented real setup.

**Built (2026-10-19)** — slices a–d ✅, commits 8c050a7, d2bfadc:
- `acl.go`: Registry.Read/Write/Readable/ForkBranchAs/DeleteAs/SetVisibility;
  `EnableACL(admin)` refuses single-home (`--home`) storage.
- `cmd/raglit/authn.go`: middleware wraps the WHOLE handler (huma, GraphQL,
  SPA, page image, attest). Config `auth.{issuer,audience,admin}`.
- `cmd/raglit/authz.go`: readIdx/writeIdx (404/403), requireAdmin.
- Admin-only with auth on (host filesystem / daemon-wide): ingest (targets
  are host paths/URLs), `/api/ocr` with a path, watch ops, pool ops,
  `/api/tools`, relations/slices/materialize/correct/withdraw/restore (they
  take a host project dir). Watched-home paths hidden from non-admin.
  `/api/email` checks the path is a doc of the index.
- attest: `aclIdentity` — read by index read rule, rulings owner-only.
- Client: `client/auth.go` Transport + TokenSource ($RAGLIT_TOKEN >
  $RAGLIT_TOKEN_COMMAND > config `auth.token_command`; renew once on 401).
  CLI installs it on http.DefaultTransport, token sent only to hosts
  ensureDaemon/resolveDaemon resolved.
- Tests: `acl_test.go`, `cmd/raglit/authn_test.go` (fake issuer: every
  surface 401 w/o token, forged/wrong-aud refused, search/list/fork/delete
  visibility, GraphQL carries principal), `client/auth_test.go`.

**Gaps found while building (not done).**
- ❓ Non-admin REMOTE INGEST has no path: ingest takes host paths, so it is
  admin-only. dun on a remote daemon needs a content-upload ingest (bytes
  or text in the request body). Blocks dun's remote use.
- ❓ Browser UI with auth on: every request 401s — no login flow. Needs an
  authorization-code login (session cookie) or SPA PKCE.
- attest is mounted per index at daemon START only (pre-existing): a layer
  created later has no review workbench until restart.
- Zitadel: access tokens must be JWT type (app / machine-user setting);
  opaque tokens fail validation. Not tested against real Zitadel.
- VecSearch + DocReview branch overlay (branch.go:19) — still open.

### dun follow-up (2026-10-19) — NOT confirmed with USER yet

dun's reading of USER decisions; confirm before building:
1. Remote architecture: raglit + model (+ maybe dun) remote. dun passes the
   HUMAN's OIDC token (device flow / browser login). Machine user only for
   unattended runs. → fits TokenSource; open: who runs the device flow
   (dun, or a `raglit login`), and where the refresh token lives.
2. Register-or-layer: dun registers an index under a PROJECT ID. None → it
   becomes the base; exists → registrant gets a top layer (P6 branch).
   Needs lookup by project id + atomic register-or-fork. Proposed id = repo
   root commit, label = normalized origin (unconfirmed).
3. `public:true` layer → every layer on the same base sees all PUBLIC sibling
   layers in search: fan-out to siblings, ACL per sibling. Changes branch
   read semantics (today: self + parent chain).
4. Maintained public base: a maintainer service (own machine user) owns the
   base, sole writer, folds public layer notes in (dedup/merge/keep
   attestation). Fits owner-only write.
5. Savings recommendation: when several layers share a project id with no
   maintained base, report duplicate docs, LLM tokens/embeddings spent,
   bytes, per-query fan-out cost.

**Optional extensions.** Group/shared ACLs (reader lists); `raglit login`
device flow; per-document visibility; audit log of reads.

## USER CHECKLIST (2026-07-23) — the agreed next build

The concrete roadmap, in order. Expands items #1 and #2 below.

- [x] **Local folder config for the client.** DONE (P0+P4). `Config.DaemonURL` +
      `resolveDaemon` (flag > $RAGLIT_DAEMON > config); CLI (ingest/search/status)
      and `serve` MCP both route to the daemon when set. `serve` proxies all tools
      to the daemon's gat HTTP surface. (Still TODO: make init offer to write a
      client-only config, and skip creating local index/originals/pages when
      daemon_url is set — see P5.)
- [x] **Multi-config daemon with scoped storage.** DONE (P5). `raglit httpd` uses
      `OpenScopedRegistry(root)` — each index its own Home at
      `<root>/indexes/<name>/` (default root `$RAGLIT_ROOT` / `~/.raglit`), the
      single daemon owning storage + workers + LLM. `--home` still selects the
      single-home layout for back-compat. (Remaining tail: client-only `init` that
      writes `daemon_url` and skips creating local index bytes.)
- [x] **Branch storage** DONE (P6). A branch is a scoped index with a parent;
      reads overlay branch-over-parent at document grain (COW); stores only diffs
      (changed docs + tombstones). `ForkBranch`/`DeleteBranch`/`ListBranches` in
      the Registry; `GET/POST/DELETE /api/branches` on the daemon; lineage +
      created_at + last_accessed_at in branch.json.
  - [x] **delete branch** — `DeleteBranch` / `DELETE /api/branches` (GC storage, parent intact).
  - [x] **list branches** — `ListBranches` / `GET /api/branches` (age + last-access + local doc count).
  - Follow-ups (not blocking): VecSearch/DocReview overlay; merge/diff; CLI/MCP
    branch commands.

All three checklist items are complete. DONE since: retired the legacy stdlib
daemon/review (`daemon`/`review` now run the gat server; `httpd` alias removed);
per-index source-content dedup (documents.content_hash, mode "unchanged"); and the
**shared cross-index pool** (`pool.go`) — the daemon caches each processed document
by `(recipe_hash, file_hash)` at `<root>/pool.sqlite` + `<root>/pool-pages/`, so
the same file in ANY index (or a retry) reuses cached fragments+vectors+images
(mode "pooled") instead of re-running the LLM; a different recipe (alt models)
reprocesses. Pool GC/eviction DONE + made LAX: `Pool.GC(GCPolicy{MaxBytes,MaxEntries,MaxAgeUnused})`
evicts OLDEST-ACCESSED (LRU) first, orphan pool-pages cleanup. Default is a byte
budget only (`--pool-max-bytes`, 4 GiB) — no TTL, no entry cap — so merges/retries
keep reusing pooled work instead of re-indexing. Optional `--pool-max`/`--pool-ttl`;
hourly background GC; `GET /api/pool` (entries/files/bytes) + `POST /api/pool/gc`.

Remaining tails: client-only `init` (write daemon_url, skip local index bytes);
branch overlay follow-ups (VecSearch/DocReview, merge/diff, CLI/MCP); splitting the
vector cache from the fragment cache (re-embed without re-segment) if
alt-embed-model churn becomes common.


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

## 3. Fragmenters: specialized splitters, image embeddings, an eval harness

Deferred out of `plan/fragmenters.md` (design agreed 2026-07-24) so the two
shipping paths — deterministic overlapping blocks for text, LLM segmentation
only for documents a VLM already transcribed — can land first.

- **Specialized fragmenters.** Markdown headings with breadcrumb prefixes on
  each fragment (the retrieval win the LLM path doesn't even attempt). Code via
  poly-lsp `symbols.FileSymbols` — symbol path + class + doc-comment span +
  `BodyStartLine` are ready-made atoms with titles attached. BLOCKED on
  poly-lsp's daemon (see its `plan/plan.md`): raglit builds CGO_ENABLED=0 and
  tree-sitter needs cgo, so a socket is the only import-free way in.
- **Image embeddings / a second vector space.** MOSTLY SHIPPED: figures get a
  `media_vectors` row + `SearchFigures`, and raglit ships the **nomic-vision** image
  embedder (imageembed.go), which shares nomic-text's space — so image figures ARE
  text-query-searchable (space `image-aligned`) with no separate query tower. What
  remains iceboxed is the genuinely-different-space case: an UNaligned model
  (SigLIP-class) whose text tower isn't the query embedder. Those vectors store as
  space `image` (dormant); making them searchable needs an image-query path or RRF
  fusion of the model's own text-tower ranking into the main search.
- **Eval harness.** A fixed query set with known-relevant documents, scored
  recall@k / MRR across fragmenter modes. Until it exists, every fragmenter
  choice here is taste, including the ones already decided.
- **Versioned fragmentations for A/B.** Keep two fragmentations of a document
  side by side to compare retrieval. Considered and explicitly not wanted yet
  (USER, 2026-07-24): reprocessing replaces a document atomically, which is the
  simpler contract. Revisit if fragmenter tuning ever needs a controlled
  comparison on real corpora rather than an eval set.
- **OCR-text cache layer.** Cache page TEXT separately from fragments so a
  stride change re-splits without re-OCRing. Not needed for correctness —
  reprocessing runs the whole pipeline by decision (fragmenters.md §4a) — and
  only pays off for vision-transcribed documents. Same shape as the vector/
  fragment cache split noted above.
