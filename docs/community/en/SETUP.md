# Setup

*Русская версия: [SETUP.md](../ru/SETUP.md)*

Getting `nxs-anomaly` running locally, and the settings you meet on the way.
For prepared env files, systemd, Compose and Kubernetes, start with
[Installation](INSTALLATION.md).

## Requirements

- Docker, or a reachable PostgreSQL 14+
- Go 1.25 and Node.js 22.12+ — only to build from source

## Docker Compose

The fastest path. It pulls the published, signed images for a release — no
local Go or npm build, no registry other than GHCR — and brings up PostgreSQL,
the API, the worker and the web interface:

```bash
cp .env.example .env
# edit .env: NXS_ANOMALY_VERSION (a tag from the Releases page, matching your checkout),
# POSTGRES_PASSWORD, and the bootstrap admin username/password. Compose refuses
# to start a container whose required variable is still empty, rather than
# fall back to a guessable default.
docker compose up -d --wait --wait-timeout 180
```

`.env` is yours to keep local — it is already covered by `.gitignore` — and it
is never optional: `docker compose up -d` without it fails immediately, naming
the missing variable, instead of starting the stack with a blank password.

| Service | What it is |
|---|---|
| `postgres` | PostgreSQL 17, data on the named volume `pgdata` |
| `app` | the API server (`NXS_ANOMALY_START_SCHEDULER=false` — the worker runs it) |
| `worker` | the background cycle: escalations, delivery, retries, retention |
| `frontend` | nginx serving the interface and proxying `/api` same-origin |

Data survives an ordinary `docker compose down` followed by `up` — the
database lives on the named `pgdata` volume, not inside the container. To
throw the installation away on purpose, `docker compose down -v` removes that
volume along with the containers. Maintain backups for data you need to keep.

Check it answered:

```bash
curl http://127.0.0.1:8080/health
# {"status":"ok","db_ok":true,"edition":"community","version":"vX.Y.Z",...}

curl http://127.0.0.1:8081/ready
# the worker's own probe — {"status":"ok","db_ok":true,"worker_cycles_completed":N,...}.
# app's /health always reports worker_cycles_completed: 0 here: the scheduler
# runs on `worker` (NXS_ANOMALY_START_SCHEDULER=false on `app`), so that counter
# is a different process's zero, not a sign the worker is down.
```

The interface is on <http://127.0.0.1:3100>. Sign in with the bootstrap admin
from your `.env`. What you land on is an empty, unconfigured installation —
see [Seeing it work](#seeing-it-work) below for demo data to look at, and the
in-app Setup page for connecting your own alerts.

To build the images from source instead of pulling a release — for
development, or to try an unreleased change — use `docker-compose.dev.yml` and
`.env.dev.example` in place of the two files above:

```bash
cp .env.dev.example .env
docker compose -f docker-compose.dev.yml up -d --build
```

That variant has no `NXS_ANOMALY_VERSION` to set; everything else is the same.

## From source

Start a database:

```bash
docker run --rm --name nxs-anomaly-postgres \
  -e POSTGRES_DB=nxs_anomaly -e POSTGRES_USER=nxs_anomaly \
  -e POSTGRES_PASSWORD=nxs_anomaly \
  -p 127.0.0.1:5432:5432 postgres:17-alpine
```

Then, in another terminal:

```bash
export NXS_ANOMALY_DB_DSN='postgres://nxs_anomaly:nxs_anomaly@127.0.0.1:5432/nxs_anomaly?sslmode=disable'
export NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME=admin
export NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD='pick a password'
export NXS_ANOMALY_SESSION_COOKIE_SECURE=false

go run ./cmd/nxs-anomaly serve --no-scheduler # the API — migrations run automatically
go run ./cmd/nxs-anomaly run-worker          # the worker, in another terminal
```

Migrations run automatically when the store initialises; there is no separate
migrate step to forget. This gives you an authenticated, empty installation:
sign in at the frontend below with the bootstrap admin. `seed-demo` (without
`--force`, which is safe on an empty database — see [Seeing it
work](#seeing-it-work)) adds demo users, a schedule and an integration to look
at; it is a way to see the shape of the product, not a step the first run
needs.

The frontend is a separate build (Node.js 22.12+):

```bash
cd frontend
npm ci
npm run dev -- --port 3100   # http://127.0.0.1:3100, proxies /api to :8080
```

## Seeing it work

To populate an empty installation with demo users, a schedule, an escalation
chain and an integration — rather than connect your own alerts right away:

```bash
go run ./cmd/nxs-anomaly seed-demo           # fails loudly if the database is not empty
go run ./cmd/nxs-anomaly print-state         # what it created, as JSON
```

`--force` additionally clears every collection first, demo and real data
alike; it exists for resetting a scratch installation, not for a first run —
run it only when you mean to discard whatever is already there.

## Commands

| Command | What it does |
|---|---|
| `serve` | the HTTP API, and the scheduler unless it is turned off |
| `run-worker` | the background cycle on its own, for a separate deployment |
| `run-escalations` | one escalation pass, then exit |
| `seed-demo` | migrations plus a demo installation to look at |
| `healthcheck --url` | probe an endpoint; this is what the image's HEALTHCHECK runs |

## Database

Either give the whole DSN:

```bash
export NXS_ANOMALY_DB_DSN='postgres://user:pass@host:5432/nxs_anomaly?sslmode=require'
```

or the parts, and let the service assemble it:

```bash
export NXS_ANOMALY_DB_HOST=127.0.0.1
export NXS_ANOMALY_DB_PORT=5432
export NXS_ANOMALY_DB_NAME=nxs_anomaly
export NXS_ANOMALY_DB_USER=nxs_anomaly
export NXS_ANOMALY_DB_PASSWORD=…
export NXS_ANOMALY_DB_SSLMODE=disable      # production: require, or stricter
```

Connection pool:

| Variable | Default | |
|---|---|---|
| `NXS_ANOMALY_DB_POOL_MAX` | `10` | maximum connections |
| `NXS_ANOMALY_DB_POOL_MIN` | `1` | idle connections kept |
| `NXS_ANOMALY_DB_POOL_MAX_CONN_LIFETIME_SECONDS` | `3600` | shed connections after a failover or through a load balancer |
| `NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS` | `30` | per-connection `statement_timeout`; migrations are exempt |
| `NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS` | `0` | retry the first connection for N seconds. Set it above zero under Kubernetes: the pod may start before the database accepts connections |

A note from load testing, in case you size this: raising the pool helps only once
the database has CPU to spare. Against a throttled PostgreSQL a larger pool makes
throughput *worse*, because the extra connections only add contention.

## Authentication

Pick at least one before exposing the service.

| Variable | |
|---|---|
| `NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME` / `_PASSWORD` | break-glass admin, created or re-promoted on every start. The password is re-applied each time, so unset both once real accounts exist |
| `NXS_ANOMALY_API_KEYS` | `tok1:admin,tok2:viewer` — keys with roles: `admin`, `editor`, `responder`, `viewer`. A key given without a role is a viewer, not an admin |
| `NXS_ANOMALY_API_KEY` | one admin key, for automation |
| `NXS_ANOMALY_SESSION_TTL_SECONDS` | `43200` — how long a browser session lives |
| `NXS_ANOMALY_SESSION_COOKIE_SECURE` | `true`. Set it to `false` only for local HTTP: a Secure cookie over plain HTTP is accepted by the browser and never sent back, so sign-in appears to work and nothing stays signed in |

Single sign-on through an OIDC provider is an enterprise feature. This build
shows the button disabled on the sign-in screen rather than hiding it, and
`GET /api/v1/capabilities` reports which features the edition has.

## Delivery

| Variable | |
|---|---|
| `NXS_ANOMALY_NOTIFICATION_MAX_RETRIES` | `3` |
| `NXS_ANOMALY_NOTIFICATION_RETRY_DELAYS` | `1,5` — minutes between attempts |
| `NXS_ANOMALY_WEBHOOK_TIMEOUT_SECONDS` | `5` |
| `NXS_ANOMALY_NOTIFICATION_DELIVERY_CONCURRENCY` | `8` provider calls in parallel per cycle |
| `NXS_ANOMALY_NOTIFICATION_CLAIM_TIMEOUT_SECONDS` | `300`. A delivery held longer than this is returned to the queue. **It must exceed the worst worker cycle**, or the reaper takes over a delivery still in flight and the notification goes out twice |
| `NXS_ANOMALY_DEAD_LETTER_WEBHOOK_URL` | where to report a notification that has permanently failed |
| `NXS_ANOMALY_WORKER_HEARTBEAT_URL` | an external dead-man's switch (healthchecks.io, Cronitor, an Uptime Kuma push monitor). The worker sends it a `GET` after a successful cycle, at most once a minute; when the pings stop, the worker, its database or the network is down. See [ALERTING_RULES.md](ALERTING_RULES.md) |
| `NXS_ANOMALY_NOTIFY_ON_RESOLVE` | `false`. With it on, whoever was woken is told the alert closed — the recipients come from the group, not from who happens to be on call now |

Provider credentials — SMTP, Telegram, Slack, Mattermost, Asterisk — and the
outbound proxy settings are their own subject; they are covered in the
configuration reference.

## Hardening

`NXS_ANOMALY_PROFILE=production` turns on the safe defaults together: the SSRF
guard, request rate limits, the delivery circuit breaker, and the refusal to
store new inline secrets. Every one of them can still be set individually.

Two that are worth knowing by name:

- `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS=true` — outbound delivery refuses private,
  loopback and link-local addresses. On by default under the production profile
  and in the Helm chart's values; the binary's own default is off.
- `NXS_ANOMALY_TRUSTED_PROXIES` — CIDRs whose `X-Forwarded-For` is believed. The
  client is the nearest address that is not a trusted proxy. The sign-in rate limit
  is per client address: behind a proxy that is not trusted every user shares one
  limit, and a few wrong passwords lock everyone out. The Helm chart trusts the
  private ranges by default — which also means a client that is itself inside one
  of those ranges (a VPN, the office network, another pod) chooses the address it
  is limited by. That is why failed sign-ins also spend a budget per account, ten
  attempts refilling at twelve a minute, which no header can vary. Narrow this
  list to the proxies you actually run if clients reach the API from a private
  network.
- `NXS_ANOMALY_EGRESS_ALLOWLIST` — hostnames and CIDRs delivery may reach at all.
- `NXS_ANOMALY_REOPEN_ACKED_ON_NEW_ALERT=true` — a new alert on an acknowledged
  group takes it back to `open` and runs the chain from step zero. Off by
  default: a repeat firing does not undo an acknowledgement. See
  [ALERT_PROCESSING.md](ALERT_PROCESSING.md) §3.1.

## Interface language and timezone

The interface ships English and Russian and follows the browser, falling back to
English when the browser asks for neither; a person can override both language
and timezone in their own settings, and the choice is stored per user rather
than per installation.

## Smoke test

Create a chain and an integration, then post an alert to it:

```bash
KEY=…            # the integration key from the UI or POST /api/v1/integrations
curl -s -X POST "http://127.0.0.1:8080/integrations/v1/alertmanager/$KEY" \
  -H 'Content-Type: application/json' \
  -d '{"alerts":[{"status":"firing",
        "labels":{"alertname":"DiskFull","severity":"critical","instance":"node-1"},
        "annotations":{"summary":"disk at 95%"}}]}'
# 202
```

The group appears in `GET /api/v1/alert-groups` within a second, and the worker
starts the escalation chain on its next cycle. Sending the same alert again
attaches it to the same group instead of opening a second one; sending it with
`"status":"resolved"` closes the group and its alerts.

## When something does not work

- **`/health` says `db_ok: false`** — the DSN or the database. Ingest answers
  `503` with `Retry-After` while the database is unavailable, so senders back off
  and retry rather than dropping the alert.
- **Alerts arrive but nobody is notified** — check that the integration's route
  ends at a chain with at least one step, and that the people it selects have a
  notification target. `GET /api/v1/readiness` reports exactly this, and says
  which check failed.
- **Notifications are attempted and fail** — look for `delivery_failed` in the
  worker log; the destination, the channel and the provider's answer are in the
  structured fields.
- **Nothing happens at all** — the worker may not be running. Check the
  worker's *own* probe, not the API's: start it with `--worker-addr :8081` (the
  Docker Compose stack already does) and curl `/ready` there —
  `worker_cycles_completed` staying at zero, or `worker_stalled: true`, means
  `run-worker` is not up or its cycle is wedged. The API's `/health` carries a
  `worker_cycles_completed` field too, but it is that *process's* count: with
  the scheduler split out (`NXS_ANOMALY_START_SCHEDULER=false`, the split
  deployment this stack uses) it stays zero forever and proves nothing about
  the worker.
