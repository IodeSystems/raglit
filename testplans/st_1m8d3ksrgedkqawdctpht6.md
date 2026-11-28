# Test plan: st_1m8d3ksrgedkqawdctpht6

Node and npm are present in this environment.

```sh
command -v node && node --version && command -v npm && npm --version
```

Throwaway install of the s1 harness deps (vitest, Testing Library, jsdom) without saving to package.json, so the committed tree stays clean. Setup only, for the run block below.

```sh
set -o pipefail
cd web && npm i --no-save --no-audit --no-fund vitest @testing-library/react jsdom 2>&1 | tail -3
```

Run the two new specs under the throwaway harness: a minimal vitest config (jsdom, the vite react plugin, so JSX in the test and its imports transform the same way the app builds) pointed at the new file. This is the measurement the whole node exists for — the band spec and the shortfall-anchor contract, against the current Dashboard.

```sh inputs=src/panes/Dashboard.test.tsx
set -o pipefail
cat > web/vitest.config.testplan.ts <<'EOF'
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: false,
  },
});
EOF
cd web && npx vitest run --config vitest.config.testplan.ts src/panes/Dashboard.test.tsx 2>&1 | tail -30
```
