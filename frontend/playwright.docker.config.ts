import { defineConfig } from '@playwright/test';
import config from './playwright.config';

// Start deploy/local-audit/compose.yaml first. This suite owns no servers.
export default defineConfig({
  ...config,
  webServer: undefined,
  use: {
    ...config.use,
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:13100',
    launchOptions: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
      ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
      : undefined,
  },
});
