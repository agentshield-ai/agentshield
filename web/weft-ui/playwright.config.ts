// Browser tests against a running Weft UI (docs/weft/DESIGN.md §7.1 task tests).
//   WEFT_E2E_URL   the UI to test (default http://127.0.0.1:5210, a Vite dev server proxying a weft server)
//   WEFT_E2E_OUT   where screenshots and click counts go (default e2e-results/)
// Start a server first, e.g.  weft --addr 127.0.0.1:6610 --shield-rules-dir rules/rules
// and  WEFT_SERVER_URL=http://127.0.0.1:6610 npx vite --port 5210, then  npx playwright test
import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  outputDir: process.env.WEFT_E2E_OUT ? `${process.env.WEFT_E2E_OUT}/artifacts` : 'e2e-results/artifacts',
  fullyParallel: false,
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: [['list']],
  use: {
    baseURL: process.env.WEFT_E2E_URL ?? 'http://127.0.0.1:5210',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
