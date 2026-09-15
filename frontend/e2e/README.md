# Browser e2e (Playwright)

Boots the **real** Go API (against PostgreSQL) and the Vite dev server, then drives
the app through Chromium. See `playwright.config.ts` and `start-api.sh`. Run with
`npm run e2e` (browsers: `npm run e2e:install`). No external providers — the paged
user notifies through the `log` channel, which delivers locally.

## Coverage map

The pre-beta browser-coverage scenarios and where each is exercised:

| Scenario | Test |
|---|---|
| Critical responder flow (login → triage → ack → resolve → delivery) | `responder-flow.spec.ts` |
| Full initial deployment through the UI (team, user, chain, integration) | `setup-flow.spec.ts` |
| Schedule rotation, override and coverage preview | `schedule-flow.spec.ts` |
| Provider test verdict, and a permanently-failed delivery surfaced | `delivery-failure.spec.ts` |
| RBAC (admin-only Audit); team isolation when enabled in Enterprise | `rbac-isolation.spec.ts` |
| Setup wizard steps derived from the live readiness report, and backup reporting clearing its blocker | `setup-readiness.spec.ts` |

`setup-readiness.spec.ts` reports a backup, which changes readiness for the rest of
the run. The suite is `workers: 1, fullyParallel: false` and files run in name order,
so it runs last — keep it that way if you add specs that assert on readiness.

Two scenarios are **not** browser tests — they are deployment/DR lifecycle
operations that the single-process Playwright harness (one API + one Vite it owns)
cannot perform, so they live in the harness built for them:

| Scenario | Test | Why not here |
|---|---|---|
| Upgrade of an existing install | `deploy/helm/nxs-anomaly/tests/e2e/kind-smoke.sh` (install → upgrade → `helm test`) | needs a real cluster and a version-to-version upgrade, not a browser |
| Recovery after worker/DB restart | worker: `tests/loadtest` (`worker_kill`, `retry_storm` — no loss; crash recovery can redeliver); DB: `tests/restore_drill.sh` (destroy → restore → alert flow) | needs to kill/restart the API/DB, which Playwright's `webServer` owns and cannot restart mid-spec |

## Local note

In some sandboxes (e.g. WSL without the browser system libraries and no root to
install them) Chromium will not launch locally; run the browser assertions in the Playwright container shown below.
Check your edition's CI workflow to see whether browser checks are automated. The whole stack (API + PostgreSQL + Vite)
still starts locally, so non-browser wiring can be checked with
`npx playwright test --list`.

## Locators: Mantine's required marker

`getByLabel('Name', { exact: true })` matches **nothing** on a required field.
Mantine renders the label as `Name<span aria-hidden> *</span>`, so the label's
text content is "Name *"; Playwright's `getByLabel` matches that text, not the
accessible name (which *is* "Name"). Dropping `exact` is not the answer either —
then 'Name' also matches 'Username'. Use the `field(scope, label)` helper from
`helpers.ts`, which anchors the name with a regex and tolerates the marker.

## Running the suite without a local browser

If Chromium cannot run in your WSL environment, use the Playwright image.
Run these commands from the repository root:

```sh
docker network create nxs-e2e
docker run -d --name nxs-e2e-pg --network nxs-e2e \
  -e POSTGRES_DB=nxs_anomaly_e2e -e POSTGRES_USER=nxs_anomaly -e POSTGRES_PASSWORD=nxs_anomaly \
  postgres:17-alpine
CGO_ENABLED=0 go build -o /tmp/nxs-anomaly-e2e ./cmd/nxs-anomaly   # from the repo root

docker run --rm --network nxs-e2e -v "$PWD:/src:ro" \
  -v /tmp/nxs-anomaly-e2e:/usr/local/bin/nxs-anomaly-e2e:ro -e CI=true \
  -e NXS_ANOMALY_TEST_DATABASE_URL="postgres://nxs_anomaly:nxs_anomaly@nxs-e2e-pg:5432/nxs_anomaly_e2e?sslmode=disable" \
  -e NXS_ANOMALY_E2E_BINARY=/usr/local/bin/nxs-anomaly-e2e \
  mcr.microsoft.com/playwright:v1.61.1-jammy bash -c '
    mkdir -p /tmp/app && tar -C /src --exclude=./frontend/node_modules --exclude=./.git -cf - . | tar -C /tmp/app -xf -
    cd /tmp/app/frontend && npm ci && npx playwright test'
```

The repo is copied out of the read-only mount because `npm ci` must write
`node_modules`; the host checkout is never touched. A full run is ~40s.
