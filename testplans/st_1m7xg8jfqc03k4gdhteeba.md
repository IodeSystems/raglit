# Test plan: st_1m7xg8jfqc03k4gdhteeba

Typecheck the web SPA after adding the facet filter: tsc must report no errors in DocumentList.tsx and router.tsx.

```sh produces=tsc.log inputs=web/src/panes/DocumentList.tsx,web/src/router.tsx,web/src/panes/DocumentList.css
set -e
cd web
pnpm exec tsc -b --noEmit > $OUT/tsc.log 2>&1 || { echo 'TYPECHECK FAILED'; exit 1; }
if grep -Eiq 'error TS' $OUT/tsc.log; then echo 'TYPECHECK FAILED'; exit 1; fi
echo "exit=0 errors=0" >> $OUT/tsc.log
echo 'TYPECHECK OK'
```
