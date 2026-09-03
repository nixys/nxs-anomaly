# nxs-anomaly frontend

Standalone web UI for nxs-anomaly. It replaces the Grafana OnCall plugin as the
product's frontend and talks **only** to the native management API (`/api/v1`) —
the Grafana compatibility layer (`/api/internal/v1`) is not used anywhere.

Stack: Vite + React + TypeScript, Mantine v7, TanStack Query, React Router.

## Pages

The page split mirrors Grafana OnCall, adapted to the nxs-anomaly domain model.

| Page | Route | Backend |
|---|---|---|
| Home | `/` | alert-groups, notifications, users, integrations |
| Alert groups | `/alert-groups`, `/alert-groups/:id` | list/get, acknowledge, unacknowledge, resolve, unresolve, silence, bulk actions, timeline |
| Alerts | `/alerts` | `/api/v1/alerts` |
| Users | `/users` | users CRUD, duty-on / duty-off |
| Teams | `/teams` | teams CRUD |
| Integrations | `/integrations`, `/integrations/:id` | integrations CRUD, rotate-key, routes, notification policy, templates, `routes/debug/{key}` |
| Escalation chains | `/escalation-chains` | chains CRUD with a step editor for all 10 step kinds |
| Schedules | `/schedules`, `/schedules/:id` | schedules CRUD, shifts, overrides, on-call |
| Notifications | `/notifications` | notifications, delivery-attempts, `escalations/run` |
| Insights | `/insights` | filtered counts + `/api/v1/history` |
| Settings | `/settings` | `/health`, ChatOps channels & messages, Grafana plugin registrations |

OnCall's "Outgoing webhooks" page has no counterpart entity here — outgoing
webhooks are a `TRIGGER_WEBHOOK` escalation step, so their runtime lives on the
Notifications page and their configuration in the chain editor.

## Development

```bash
npm install
npm run dev          # http://localhost:3100
```

Vite proxies `/api`, `/health`, and ingestion paths to `http://localhost:8080`.
Point it elsewhere with `NXS_ANOMALY_API_URL`:

```bash
NXS_ANOMALY_API_URL=http://staging:8080 npm run dev
```

Other scripts: `npm run typecheck`, `npm run build`, `npm run preview`.

### If `npm test` cannot start

On a glibc machine the tests may fail before running a single case, with a
rolldown binding error. The cause is not the machine: the lockfile carries both
`@rolldown/binding-linux-x64-gnu` and `-musl` and **neither declares a `libc`
field**, so npm cannot tell them apart by platform — an install done in the CI
image (`node:22-alpine`) leaves only the musl one behind.

A fresh `npm ci` installs both and works. Repairing an existing tree in place
usually does not: `npm install` into it hits `EACCES` if any of it was ever
created as root.

```bash
rm -rf node_modules && npm ci && npm test
```

## Authentication

The backend has no user accounts — it authenticates with a single static API key
(`NXS_ANOMALY_API_KEY`). On startup the UI probes the API:

- if unauthenticated requests succeed, the backend has no key configured and the
  UI goes straight in (Settings shows a warning about exposing it);
- otherwise the UI asks for the key, stores it in `localStorage`, and sends it as
  `X-API-Key` on every request.

## Production

The image is nginx serving the built SPA and reverse-proxying the API, which keeps
the browser same-origin so the Go backend needs no CORS configuration.

```bash
docker build -t nxs-anomaly-frontend ./frontend
docker run -p 3100:8080 -e NXS_ANOMALY_API_UPSTREAM=api.internal:8080 nxs-anomaly-frontend
```

CI builds the image with BuildKit (`buildctl-daemonless.sh`) exactly like the Go
image — same `.buildkit` job template, its own registry build cache under
`<image>-frontend:buildcache`. The `npm ci` layer is keyed on `package-lock.json`,
so dependency installation is restored from that cache whenever the lockfile is
unchanged. To reproduce a CI build locally:

```bash
buildctl build \
  --frontend dockerfile.v0 \
  --local context=./frontend --local dockerfile=./frontend \
  --opt filename=Dockerfile \
  --opt build-arg:VERSION=$(git describe --tags --always) \
  --output type=image,name=nxs-anomaly-frontend:local
```

`docker compose up` brings it up on <http://localhost:3100> alongside postgres,
the API and the worker.

To serve the SPA from a different origin than the API instead, build with
`VITE_API_BASE_URL=https://api.example.com` — the backend would then need CORS
headers, which it does not currently send.
