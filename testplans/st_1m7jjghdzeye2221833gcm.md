# Test plan: st_1m7jjghdzeye2221833gcm

TypeScript type-check of the web frontend, including the new Hint pane.

Installs node_modules from package-lock.json, then runs `tsc -b --noEmit` to verify all TypeScript compiles cleanly with strict mode.

```sh inputs=web/package-lock.json,web/tsconfig.json,web/src/api.ts,web/src/panes/Hint.tsx,web/src/router.tsx,web/src/shell/NavDrawer.tsx
cd web
npm ci --ignore-scripts 2>&1 | tail -5
npx tsc -b --noEmit 2>&1
```
