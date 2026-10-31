# 2026-10-30 — always a daemon, never open, session keys

Proposal, not built. Extends `2026-10-19-daemon-auth.md`, which built OIDC,
per-index ACL (`acl.go`: owner + private|public, read = (public ∨ owner) ∧
read(parent), write = owner), branches with ACL, project layers and the client
token (`client/auth.go`). That work applies only when `auth.issuer` is set;
without one every endpoint is open.

## Decisions (USER, 2026-10-30)

1. **Never open.** "Raglit should be admin-token or oidc, never open." No
   opt-out flag.
2. **Always a daemon.** raglit is always a daemon connection; "Solo is a
   nuance to be avoided". No in-process index for any client.
3. **Sessions never write main.** "the main merge phase owns the main session
   as public": main is public, and only the merge phase writes it. A session
   writes its own branch.
4. **Run as the user, for now.** A separate OS user for the daemon is
   deferred: it protects the index files from a same-user agent, but it also
   blocks the routine work agents do on the daemon (restart, logs, benches).
   The design must let a separate-user install come later with no client
   change.
5. **aw4 owns main**, a watch of the merge tree; a session is identified by
   its aw4 tracking id; aw4 authenticates with an API token.
6. **Session documents are kept until the TTL** (relayed by dun-a8: "7 day to
   save thrash"). Close does not delete.

## 1. Never open

- `daemonAuth` (`cmd/raglit/authn.go`) returns no middleware when no issuer
  is set. Change: no issuer → **admin-token mode**. The admin token at
  `<root>/admin.token` (0600) is always created; the ACL is always on, with
  admin = `localAdmin`. With an issuer, OIDC tokens are accepted as well.
- `/api/health` stays open; it already names the token file.
- Client: `client/auth.go` gets one more source, after `$RAGLIT_TOKEN`,
  `$RAGLIT_TOKEN_COMMAND` and `auth.token_command`: the admin token file that
  `/api/health` names, when this process can read it (same user, local
  daemon). No config is needed on a single-user machine.
- The branch API stops being "localhost, no auth": it goes through
  `ForkBranchAs` and the ACL like every other route.

## 2. Always a daemon

- Remove `--embedded`, `--db`, and `index`'s in-process mode.
- Tests, benches and `raglit demo` start their own daemon with a temporary
  root and port and connect to it as a client.
- The CLI and `raglit mcp` share one path to the daemon (discovery,
  namespacing, token). Planned as an importable package for a Go session
  index; not needed once the session index became plain CLI use (§5), so it
  stays in `cmd/raglit`.
- Reads never create an index (the read-shim issue): with one path, the fix is
  in one place.

## 3. Main and branches

- **main** (`<ns>__<idx>`) is public and is a daemon watch of the **merge
  tree**: the directory where merged work lands (USER 2026-10-30: aw4 runs
  the merge; it "doesn't even need to run raglit, aw4 just needs to know
  where the merge sync dir is and point the main index there"). The watcher
  writes main as the index owner (the 2026-10-19 rule: workers act as the
  index owner). No client writes main.
- So raglit has **no merge operation**. Promotion happens in the files: aw4
  merges into the merge tree, the watcher re-indexes what changed, and main
  follows.
- **A session's branch** `<ns>__<idx>@<session>` is forked on its first
  write; private to the session's key. It holds what the session changed (its
  worktree's files, its notes). The session's reads see the branch over main;
  others see main only.
- Once the work is merged, main carries it and the branch is redundant. A
  branch with no write for 7 days is deleted by the same reaper as session
  documents.
- **aw4 owns main** (USER 2026-10-30). aw4's principal is the owner of
  `<ns>__<idx>`. It authenticates with OIDC or an **API token**: "we give the
  API token to aw4 when they create the index". The index already exists, so
  the bootstrap is: the admin mints a token for principal `aw4`, makes `aw4`
  the owner of the existing main index, and writes the token (0600) into
  aw4's data directory, where aw4 loads it:
  `raglit key mint --principal aw4 --out FILE` and
  `raglit index <ns>:<idx> owner aw4`.
  Its setup step points main at the merge tree (watch + scope): a config
  change, not a new raglit feature.
- A session branch is owned by the key that first wrote it (USER refinement:
  a child key owns its branch, its parent reads and revokes it); the keys'
  write grants keep one session out of another's branch. A branch over a
  parent nothing has written (a sessions index) creates that parent empty,
  admin-owned and public. Before its first write a branch reads as its
  parent. A session key reads only what its issuer may (aw4's keys: what aw4
  may read).
- Selector spelling: `proj:idx@branch` (`:` is taken by `proj:idx`).

## 4. API tokens and session keys (built: keys.go, cmd/raglit/keysapi.go)

Two kinds, one store: sha256 hashes in `<root>/keys.json` (0600). A token is
`rk_<id>_<secret>`, shown once.

- **API token**: a long-lived credential for a principal that is not a
  person, such as `aw4`. Minted by the admin; it acts as that principal
  through the ACL, like an OIDC user. Never expires; revocable.
  `raglit key mint --principal aw4 --out FILE`, then
  `raglit index <ns>:<idx> owner aw4`.
- **Session key**: minted by any principal that is not itself a session key
  (aw4 with its API token, or a person) for a project and a session id:
  `raglit key mint --session <id> [--project P]`. It acts as its own
  principal, `key#<id>`, so the branches it creates are its own.
  - read: what its issuer may read, narrowed to `P__*`, P's `shared`
    namespaces and global indexes (`:global`);
  - write: only `P__*@<session>`, its session's branches;
  - never admin; expires 7 days after its last use.
- **Child key**: a session key mints one for ONE of its branches, for a
  sub-agent: `raglit key mint --branch [proj:]idx@<session>` with the parent
  key as `$RAGLIT_TOKEN`. The child owns that branch; its ancestors read it
  and revoke it; revoking a key revokes every key under it.
- `raglit key list` shows the keys a caller issued and those under them;
  `raglit key revoke ID`.
- **Session id** (USER 2026-10-30): the aw4 tracking id when aw4 runs the
  session (the root of a successor chain), so a resumed session gets a new
  key for the same id and sees its own branches. A session id is a name, not
  a credential: the key is the credential.
- The CLI sends `$RAGLIT_TOKEN` as the bearer, so a host hands a session its
  key in that variable.

**Limit, stated plainly.** With the daemon running as the user, an agent with a
shell can read `admin.token` or edit the index files directly. Keys bound agent
mistakes (a wrong selector, a purge, a write to main), not a determined
same-user agent. A separate daemon user (decision 4) is the fix when that
matters.

## 5. Session index (built as branches; no Go package)

USER 2026-10-30 (relayed by dun-a8, after two earlier shapes — a Go package,
then MCP tools — were dropped): the session index is ordinary raglit use.

- A session's fold and recap documents go into its branch of the sessions
  index, `<ns>:sessions@<session-id>`: forked on the first write, never
  merged (main is public; transcripts must not reach it), removed 7 days
  after its last write.
- Write: `raglit ingest --index sessions@<id> --name fold-3 --text-file -
  --identity fold-3.json` with the session's key. The identity
  `{name, summary, kind}` is recorded as gen_source `supplied` (Model = the
  caller), summary cut to 2500 characters, no minimum, no identity or tags
  job; a re-ingest that supplies another replaces it; a person's caption
  outranks it.
- Read: the ordinary search. The host adds `<ns>:sessions@<id>` to its search
  selector; before the first write it reads nothing and never 404s.
- Close deletes nothing; the reaper does (`session_branch_ttl_hours`, default
  168, hourly).

## Order (as built)

1. Never open: admin-token mode, the client reads a local daemon's token file.
2. Every command through the daemon; reads never create; a missing name 404s
   with suggestions; `proj:prefix*` wildcards.
3. Session branches `idx@session`, created on write, reaped after the TTL.
4. API tokens, session keys, child keys; `raglit key`, `raglit index NAME
   owner`.
5. Supplied identity on ingest; `raglit ingest --text-file`.
6. Not built (USER: skip): `raglit service install --user raglit`.

## Open

- Upgrade: every client without a token breaks when the daemon upgrades,
  including the Mac's `raglit mcp` until it is on the new build. Needs a
  release note.
- `raglit mcp` does not mint a session key itself; a host passes one in
  `$RAGLIT_TOKEN`. Minting one per MCP process would turn every MCP write
  into a branch write, which no one has asked for yet.
