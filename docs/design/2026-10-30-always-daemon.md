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
   its aw4 tracking id.
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
- One daemon client package (discovery, namespacing, token) is used by the
  CLI, `raglit mcp` and `raglit/sessionindex`. It moves out of `cmd/raglit`
  (package main, not importable).
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
  aw4's data directory, where aw4 loads it. Commands, roughly:
  `raglit token mint --principal aw4 --out FILE` and
  `raglit index <ns>:<idx> --owner aw4`.
  Its setup step points main at the merge tree (watch + scope): a config
  change, not a new raglit feature.
- Session branches are forked by aw4's principal and owned by it; the session
  key's grants are what keep one session out of another's branch. aw4 may
  mint session keys only within what it owns (key grants ∩ issuer's rights).
- Selector spelling: `proj:idx@branch` (`:` is taken by `proj:idx`).

## 4. API tokens and session keys

Two kinds, one store (hash only, revocable, listed by the admin):

- **API token**: a long-lived credential for a principal that is not a
  person, such as `aw4`. Minted by the admin; rights come from what that
  principal owns or may read, through the ACL, the same as an OIDC user.
- **Session key**: short-lived, minted by a principal (aw4, or `raglit mcp`
  with the user's credential) for one session, with narrowed grants:

- `POST /api/sessions {project, cwd, session}` with the caller's admin or
  OIDC token returns a random key. The daemon stores its hash, the issuer
  principal, its grants, its session id and its last use.
- **Session id** (USER 2026-10-30): the aw4 tracking id when aw4 runs the
  session. It survives pauses and successor LLM sessions, so a resumed
  session gets a new key for the same id and sees its own branch and notes.
  Without aw4, `raglit mcp` generates one.
- A session id is a name, not a credential: aw4 ids, like LLM session ids,
  appear in issues, logs and messages. The key is the credential; the id
  only says what the key protects. Other sessions' keys can neither read nor
  write a session's branch or prefix.
- Grants for a session key started in project P:
  - read: `P__*`, P's `shared` namespaces, ambient global indexes, indexes
    scoped to `cwd`;
  - write: the session's branches, and `sessions/<id>/` in `P__sessions`;
  - no manage, no admin; it cannot mint keys.
- Effective rights = key grants ∩ issuer's rights.
- Expiry: 7 days after last use; the owner can revoke.
- aw4 mints the key when it starts a session and hands it to the agent as
  `RAGLIT_TOKEN`. Without aw4, `raglit mcp` mints one at start and passes it
  to child processes the same way.

**Limit, stated plainly.** With the daemon running as the user, an agent with a
shell can read `admin.token` or edit the index files directly. Keys bound agent
mistakes (a wrong selector, a purge, a write to main), not a determined
same-user agent. A separate daemon user (decision 4) is the fix when that
matters.

## 5. Session index (agentkit)

agentkit `agent.SessionIndex` (agentkit 0b98f58, untagged, v0.15.0):
Put / Search / Close over `SessionDoc{Key, Kind, Name, Summary, Body}`.

- `raglit/sessionindex.New(ns, sessionID, opts...)`; the host passes the
  session id of §4 (the aw4 tracking id under aw4), so a continued session
  shares one prefix.
- One index per namespace, `<ns>__sessions`: opt-in, never ambient, no
  directory scope.
- Put → upload `sessions/<id>/<Key>` with a supplied identity: new
  gen_source `supplied`, Model = caller, Summary cut to 2500 characters, no
  40-character minimum, no identity or tags job; re-ingest replaces it.
- Search: own prefix; option `SearchNamespace` for the whole index.
- Close: nothing by default (decision 5); option to purge the prefix.
- Reaper: index setting `expire_after` (7d for `<ns>__sessions`) with a
  prefix depth; deletes a prefix whose newest `added_at` is older.
- Uses the shared daemon client, so it sends a key like any other client.

## Order

1. Never open (admin-token mode, client token file source, branch API ACL).
2. One daemon client; remove solo mode; reads never create.
3. Main/branch rules: main is a watch of the merge tree; session writes go
   to branches.
4. API tokens (`token mint`, owner change) and the aw4 bootstrap; then
   session keys.
5. Session index (needs agentkit v0.15.0 tagged).
6. Optional: `raglit service install --user raglit`.

## Open

- Upgrade: every client without a token breaks when the daemon upgrades,
  including the Mac's `raglit mcp` until it is on the new build. Needs a
  release note.
