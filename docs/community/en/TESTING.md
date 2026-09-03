# Testing

*Русская версия: [TESTING.md](../ru/TESTING.md)*

The project is checked at four levels: fast unit and static checks, integration
against PostgreSQL, browser scenarios, and finally deployment and disaster
recovery in kind. The list of test files changes faster than documentation does;
the sources of truth are `.gitlab-ci.yml`, `frontend/package.json` and the
`*_test.go`, `*.test.tsx`, `frontend/e2e` and `deploy/helm/nxs-anomaly/tests`
directories.

## Fast checks

With no external services:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
golangci-lint run --timeout=15m ./...
git diff --check
```

A plain `go test ./...` must not accidentally connect to a runtime database.
Tests that need PostgreSQL turn on only when `NXS_ANOMALY_TEST_DATABASE_URL` is
set.

## The PostgreSQL integration suite

The recommended local run:

```bash
tests/run_postgres_integration.sh
```

The script uses an existing `NXS_ANOMALY_TEST_DATABASE_URL` or starts a throwaway
`postgres:17-alpine`, waits for readiness and runs the full `go test`. The knobs:

| Variable | Default | Purpose |
|---|---:|---|
| `NXS_ANOMALY_TEST_DATABASE_URL` | — | an existing PostgreSQL DSN |
| `NXS_ANOMALY_TEST_POSTGRES_PORT` | `55432` | the local container's port |
| `NXS_ANOMALY_TEST_POSTGRES_DB` | `nxs_anomaly_test` | database |
| `NXS_ANOMALY_TEST_POSTGRES_USER` | `nxs_anomaly` | user |
| `NXS_ANOMALY_TEST_POSTGRES_PASSWORD` | `nxs_anomaly` | password |
| `NXS_ANOMALY_KEEP_TEST_POSTGRES` | `0` | `1` keeps the container around |
| `NXS_ANOMALY_TEST_GO_TEST_ARGS` | — | extra `go test` arguments, used by the coverage job |
| `NXS_ANOMALY_TEST_TIMEOUT_SCALE` | `1` | multiplier for the suite's wall-clock deadlines |

**The suite needs a database to itself.** It clears collections and runs worker
cycles of its own, expecting deliveries to arrive at its local stubs. A live
worker pointed at the same database will claim its notifications and deliver them
elsewhere — which looks from the outside like seven unrelated delivery tests
failing. The suite reads the worker heartbeat (the same one readiness uses) and
refuses to start if the database is busy.

The suite's deadlines assume PostgreSQL next to the process, which is how CI is
arranged and how production is not. One network hop makes a query cost ~6.6 ms
instead of ~0.6 ms, and tests start failing on the clock rather than on
behaviour. For a remote database raise `NXS_ANOMALY_TEST_TIMEOUT_SCALE` — to `4`,
say.

The suite applies migrations `0001`–`0027` and exercises real transactions,
constraints and indexes, ingest, RBAC, the audit trail, sessions, schedules,
maintenance windows, readiness, personal-data export and erasure, retention,
delivery and retry, several worker replicas, and the propagation of trace
context.

## Frontend

Node.js 22.12+ is required:

```bash
cd frontend
npm ci
npm run typecheck
npm run test
npm run test:coverage
npm run build
npm run openapi:check
npm run audit:prod
npm run audit:dev
```

Vitest runs in jsdom. The suite covers the alert groups, audit, integrations,
maintenance, notifications, readiness, schedules, settings, users and onboarding
pages, notification policies, and the i18n/Intl formatting for `ru-RU` and
`en-US`. Coverage thresholds live in `frontend/vite.config.ts`.

`openapi:check` regenerates the TypeScript types from `docs/openapi.json` into a
temporary file and diffs them against `frontend/src/api/schema.d.ts`. After
changing the contract deliberately, run first:

```bash
cd frontend
npm run openapi:types
```

If rolldown reports an incompatible native binding, `node_modules` was installed
for a different libc or platform. The fix is a clean `npm ci`, not copying the
directory from an Alpine environment into a glibc one or the other way round.

## Browser end-to-end

```bash
cd frontend
npm run e2e:install
npm run e2e
```

Playwright starts a real Go API, PostgreSQL and Vite. The scenarios:

- the responder flow: sign in → ingest → acknowledge → resolve → delivery;
- first-time setup through the interface;
- schedule rotation, overrides and coverage;
- a successful provider test and a permanently failed delivery;
- RBAC and cross-team isolation;
- setup, readiness and the backup report.

The map, and how to run it in a container under WSL, are in
`frontend/e2e/README.md`. The suite is sequential: the readiness scenario mutates
the shared database and has to stay last.

## Helm, deployment and disaster recovery

```bash
helm lint deploy/helm/nxs-anomaly
helm unittest -f 'tests/unit/*_test.yaml' deploy/helm/nxs-anomaly

deploy/helm/nxs-anomaly/tests/e2e/kind-smoke.sh
deploy/helm/nxs-anomaly/tests/e2e/kind-networkpolicy.sh

tests/restore_drill.sh
tests/pitr_drill.sh
tests/run_load_test.sh
```

The Helm unit tests check workloads, secret modes, the production preflight,
rate-limit scaling, NetworkPolicy, the datastores, Istio and the Gateway API, and
the acceptance hook. The kind scenarios run install and upgrade, network policy
enforcement, and real upgrade paths. The DR drills verify logical restore,
point-in-time recovery, readiness, and that the previous release still runs
against the migrated schema.

## Contract and security gates

- `internal/server/openapi_contract_test.go` checks routes against
  `docs/openapi.json` in both directions, including suffix and action routes and
  authorization coverage.
- A dedicated security job runs the production profile, secret references,
  redaction, same-origin enforcement, signatures and delivery-honesty
  regressions.
- `govulncheck`, `gosec`, `golangci-lint`, the frontend audit allowlist and
  dependency freshness are all required gates.
- `scripts/check-version.sh` reconciles `VERSION`, the chart, the OpenAPI
  document and the release metadata.
- A documentation job checks the Markdown invariants; a commitlint job enforces
  Conventional Commits.

## The pipeline

It is split by cost rather than into two large stages:

1. `static` — docs, formatting, version, commitlint;
2. `quality` — vet, lint, govulncheck, gosec;
3. `unit` — Go, race, security, frontend, OpenAPI, reproducible build;
4. `integration` — PostgreSQL coverage, Playwright, restore/PITR/load;
5. `cluster` — the kind scenarios, serialised;
6. `build` → `package` → `publish` → `sign` → `verify`.

A tag pipeline publishes two images, an OCI Helm chart, CycloneDX SBOMs and
signatures, after which the verification job checks the publicly documented pull
and render, and the images by digest.

## The minimum before merging

For a documentation change:

```bash
git diff --check
# plus the local Markdown link check described in the README
```

For a Go or backend change:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
golangci-lint run --timeout=15m ./...
tests/run_postgres_integration.sh
```

For a frontend or API contract change add `npm run test:coverage`,
`npm run build` and `npm run openapi:check`; for the chart, `helm lint` and
helm-unittest. Changes to delivery, migrations, topology or restore need the
matching integration, kind or drill scenario — not unit tests alone.

The commit message convention is in
[CONTRIBUTING.md](../../../CONTRIBUTING.md).
