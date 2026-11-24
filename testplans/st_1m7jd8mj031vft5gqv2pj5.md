# Test plan: st_1m7jd8mj031vft5gqv2pj5

TypeScript build check: `tsc -b --noEmit` on the web/ directory. Must produce no errors.

```sh inputs=web/src/api.ts,web/src/panes/Activity.tsx
cd web && npx tsc -b --noEmit 2>&1
```
