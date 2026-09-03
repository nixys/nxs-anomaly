import { defineConfig, devices } from '@playwright/test';
import base from './playwright.config';

// Design export harness. Not a test suite: it drives the same real stack the
// e2e gate boots (Go API against PostgreSQL + the Vite dev server) and writes
// artefacts for a designer — full-page screenshots of every screen and the
// theme's own CSS variables — into design-export/.
//
// It reuses the e2e webServer block verbatim rather than defining its own, so
// there is one description of how this app is brought up. Everything else is
// overridden: a longer timeout (a capture walks ~17 routes in one test) and no
// retries, because a half-written screenshot set is worse than a loud failure.
export default defineConfig({
  ...base,
  testDir: './design',
  timeout: 300_000,
  retries: 0,
  reporter: [['list']],
  use: { ...base.use, trace: 'off' },
  projects: [{ name: 'design', use: { ...devices['Desktop Chrome'] } }],
});
