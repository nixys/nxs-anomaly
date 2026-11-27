# Local acceptance environment

From the nxs-anomaly repository root:

```sh
docker compose -p anomaly-audit -f deploy/local-audit/compose.yaml up -d --build
cd frontend
E2E_API_PORT=18080 npm run e2e -- --config playwright.docker.config.ts
```

UI: http://127.0.0.1:13100. Test login: `e2e-admin` / `e2e-password-1234`.
The fixture credentials and API key are for this loopback-only environment.
Install Playwright Chromium first (`npm run e2e:install`), or set
`PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` to an existing compatible Chromium binary.

The existing readiness tests require a fresh database (no previously reported
backup). To repeat the entire suite, recreate this disposable stand first:

```sh
docker compose -p anomaly-audit -f deploy/local-audit/compose.yaml down
docker compose -p anomaly-audit -f deploy/local-audit/compose.yaml up -d
```

PostgreSQL has no persistent volume: `down` removes its test data. Use only this
project name and compose file to avoid affecting other environments.

After the functional suite, run `python3 deploy/local-audit/benchmark.py` from
repository root. It inserts 1k/10k/50k synthetic users, measures HTTP response
sizes and wall times, then deletes its rows even on failure. Results go to
`../ui-audit/docker/benchmark.json`. Run it without concurrent fixture writes.
These are local warm request measurements, not production load-test results.

Native Android runtime acceptance uses the mobile repository's
`scripts/e2e-emulator.sh`, which requires Docker access to `/dev/kvm`. It also
uses port 18080; stop this stand before running that separate script.
