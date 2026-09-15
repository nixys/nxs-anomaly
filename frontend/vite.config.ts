/// <reference types="vitest" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// In dev the SPA talks to the Go API through Vite's proxy, so the browser stays
// same-origin and no CORS configuration is needed on the backend.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 3100,
    // changeOrigin:false preserves the browser's Host header on the upstream
    // request, mirroring the production nginx (`proxy_set_header Host $http_host`).
    // The API's same-origin write check compares Origin to Host, so rewriting
    // Host to the target would 403 every session-cookie write (ack/resolve/…).
    // The prefixes must mirror nginx.conf.template exactly. `/integrations/v1`
    // and not `/integrations`: the SPA has its own /integrations page, and a
    // prefix proxy swallowed it — a hard load or reload of that URL returned the
    // API's "404 page not found" instead of the app. Production was always
    // right (nginx proxies `location /integrations/v1/`), so this only ever bit
    // developers and the e2e run.
    proxy: Object.fromEntries(
      ['/api', '/health', '/live', '/integrations/v1'].map((p) => [
        p,
        { target: process.env.NXS_ANOMALY_API_URL ?? 'http://localhost:8080', changeOrigin: false },
      ]),
    ),
  },
  build: { outDir: 'dist', sourcemap: false },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/setupTests.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    // userEvent drives keystrokes one at a time, so the interaction-heavy schedule
    // tests take several seconds on a cold, loaded CI runner — well past vitest's
    // 5s default. Give them headroom so a slow runner is not a red build.
    testTimeout: 20000,
    hookTimeout: 20000,
    // Coverage is a ratchet, not a score. The Go side gained a floor
    // (COVERAGE_FLOOR in .gitlab-ci.yml) precisely because a number nobody
    // gates on drifts downwards a point per release; this is its counterpart.
    //
    // `include` is what makes the number honest. Without it v8 only counts
    // files a test happened to import, which reported ~69% while eleven of
    // seventeen pages had no test at all. Counting every source file says 20%,
    // which is the truth and the reason the thresholds start where they do.
    //
    // Raise these as pages gain tests. Lowering them is allowed but should be a
    // visible line in a merge request, not something that happens quietly.
    coverage: {
      provider: 'v8',
      reporter: ['text-summary', 'html'],
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.test.{ts,tsx}',
        'src/main.tsx', // bootstrap: renders the app into the DOM, nothing to assert
        'src/setupTests.ts',
        'src/api/schema.d.ts', // generated from docs/openapi.json
      ],
      thresholds: {
        statements: 44,
        branches: 39,
        functions: 37,
        lines: 44,
      },
    },
  },
});
