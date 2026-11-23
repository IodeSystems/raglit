# Test plan: st_1m7hqrqt03pn8mwtrprczn

TypeScript typecheck passes on the changed files (api.ts, Dashboard.tsx, DocumentList.tsx, router.tsx).

```sh inputs=web/src/api.ts,web/src/panes/Dashboard.tsx,web/src/panes/DocumentList.tsx,web/src/router.tsx
cd web && npx tsc -b --noEmit
```

oxlint type-aware lint passes on the changed files.

```sh inputs=web/src/api.ts,web/src/panes/Dashboard.tsx,web/src/panes/DocumentList.tsx,web/src/router.tsx
npx --prefix web oxlint --type-aware web/src/api.ts web/src/panes/Dashboard.tsx web/src/panes/DocumentList.tsx web/src/router.tsx
```
