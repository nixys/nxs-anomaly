import { defineConfig, devices } from '@playwright/test';

// BETA-002 browser e2e release gate. Boots the real Go API (against PostgreSQL)
// and the Vite dev server (proxying the API same-origin), then drives the
// critical responder flow through a real browser. No external providers: the
// paged user notifies through the log channel, which delivers locally.
const API_PORT = process.env.E2E_API_PORT ?? '8080';
const UI_PORT = process.env.E2E_UI_PORT ?? '3100';

export default defineConfig({
  testDir: './e2e',
  timeout: 90_000,
  expect: { timeout: 20_000 },
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  // On CI also write the HTML report: the job uploads frontend/playwright-report
  // as an artifact, and with only the line reporter that path never existed
  // ("no matching files" on every failed job). The HTML report is what makes the
  // traces and screenshots browsable after the fact.
  reporter: process.env.CI ? [['line'], ['html', { open: 'never' }]] : [['list']],
  use: {
    baseURL: `http://localhost:${UI_PORT}`,
    trace: 'on-first-retry',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: [
    {
      command: 'sh e2e/start-api.sh',
      url: `http://localhost:${API_PORT}/health`,
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
    {
      command: `npm run dev -- --port ${UI_PORT} --strictPort`,
      url: `http://localhost:${UI_PORT}`,
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
      env: { NXS_ANOMALY_API_URL: `http://localhost:${API_PORT}` },
    },
  ],
});
