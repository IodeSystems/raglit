# 2026-10-19 — daemon auth (OIDC), per-index ACL, shared project layers

One entry of `plan/done/`. Requested by the dun session (USER-approved); the
original request, proposal and decisions are kept below as they stood.

## What was built (commits e13781a … 13e61a6)

- **Auth** `cmd/raglit/authn.go`: OIDC bearer, JWKS in the daemon (go-oidc),
  principal `<iss>#<sub>`, wraps the whole handler; only `/api/health` open.
  Config `auth.{issuer,audience,admin}`.
- **ACL** `acl.go`: per index (a branch is an index), `acl.json`
  {owner, visibility}. read = (public ∨ owner) ∧ read(parent); write = owner.
  No acl.json → admin-owned private. Unreadable → 404, not-owner write → 403.
  Scoped storage only.
- **Handlers** `cmd/raglit/authz.go`: readIdx/writeIdx everywhere; fan-out and
  listings filtered. Admin-only with auth on: ingest by path, `/api/ocr` with a
  path, watch, pool, `/api/tools`, project-dir ops (relations, slices,
  materialize, correct, withdraw, restore). `/api/email` must name a doc of
  the index. attest: `aclIdentity` (rulings owner-only).
- **Client** `client/auth.go`: $RAGLIT_TOKEN > $RAGLIT_TOKEN_COMMAND > config
  `auth.token_command`, renew once on 401; CLI sends it to resolved daemon
  hosts only. USER: dun runs the device flow; raglit stores no tokens.
- **Upload** `POST /api/upload` (`upload.go`): ingest by content into
  `<index home>/uploads/<name>`; owner write; the non-admin remote ingest.
- **Overlay** VecSearch + Hybrid + DocReview over the parent (hybrid fuses by
  path+fragment id).
- **Projects** `project.go`: `POST /api/projects/register` (register-or-layer,
  atomic) — first registration makes a PUBLIC EMPTY base, every registrant a
  private layer (USER 2026-10-19: a private base would make others' layers
  unreadable). `GET /api/projects/lookup?id=`. `POST /api/visibility` =
  public:true. `/search` on a layer adds public sibling layers (their own docs).
  Project id = opaque text (dun: root commit, label = normalized origin; USER
  confirmed).
- **Browser login** `cmd/raglit/authweb.go`: auth code + PKCE,
  `auth.{client_id,redirect_url}`, in-memory sessions, SameSite=Lax cookie,
  cookie writes must be same-origin.
- Tests: `acl_test.go`, `project_test.go`, `upload_test.go`,
  `branch_test.go` (vector overlay), `cmd/raglit/authn_test.go` (fake issuer:
  every surface, visibility, uploads, register→publish→search, GraphQL,
  browser login/CSRF/logout), `client/auth_test.go`.

## Record as it stood

### REQUEST from dun (USER, 2026-10-19): daemon auth — OIDC

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

### dun follow-up (2026-10-19) — USER confirmed 2026-10-19

USER answers: dun runs the device flow and hands raglit the token via
RAGLIT_TOKEN_COMMAND (raglit stores no tokens). Build order: (1) content-upload
ingest, (2) VecSearch/DocReview branch overlay, (3) register-or-layer by
project id + public sibling fan-out, (4) browser login (auth-code + session
cookie). Points 4–5 (maintainer service, savings report) not scheduled.

dun's points (confirmed as read):
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

