// Browser tests against a running Weft UI (docs/weft/DESIGN.md §7.1 task tests).
//   WEFT_E2E_URL   the UI to test (default http://127.0.0.1:5210). Either a weft
//                  server's own address, which serves the embedded UI (what CI
//                  does), or a Vite dev server proxying one.
//   WEFT_E2E_OUT   where screenshots, a11y.json and task-clicks.json go (default e2e-results/)
// The suite seeds fresh projects itself (e2e/seed.ts), so any server works,
// with the compatibility adapter off (the default). Replay tests need
// --shield-rules-dir and are skipped without it.
//   weft --addr 127.0.0.1:6610 --db /tmp/e2e.db --shield-rules-dir rules/rules
//   WEFT_E2E_URL=http://127.0.0.1:6610 npx playwright test
// To test UI changes without rebuilding the server, run Vite in front of it:
//   WEFT_SERVER_URL=http://127.0.0.1:6610 npx vite --port 5210 and leave WEFT_E2E_URL unset.
import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  outputDir: process.env.WEFT_E2E_OUT ? `${process.env.WEFT_E2E_OUT}/artifacts` : 'e2e-results/artifacts',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [['list'], ['github']] : [['list']],
  use: {
    baseURL: process.env.WEFT_E2E_URL ?? 'http://127.0.0.1:5210',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
