# Setup

*Русская версия: [SETUP.md](../ru/SETUP.md)*

Getting `nxs-anomaly` running locally, and the settings you meet on the way.

## Requirements

- Docker, or a reachable PostgreSQL 14+
- Go 1.25 and Node.js 22.12+ — only to build from source

## Docker Compose

The fastest path. It brings up PostgreSQL, the API, the worker and the web
interface:

```bash
cp .env.example .env      # set POSTGRES_PASSWORD and the bootstrap admin password
docker compose up -d
```

| Service | What it is |
|---|---|
| `postgres` | PostgreSQL 17, with a volume so data survives a restart |
| `app` | the API server (`NXS_ANOMALY_START_SCHEDULER=false` — the worker runs it) |
| `worker` | the background cycle: escalations, delivery, retries, retention |
| `frontend` | nginx serving the interface and proxying `/api` same-origin |

Check it answered:

```bash
curl http://127.0.0.1:8080/health
# {"status":"ok","db_ok":true,"edition":"community","version":"v0.1.73",...}
```

The interface is on <http://127.0.0.1:3100>. Sign in with the bootstrap admin
from your `.env`.

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

go run ./cmd/nxs-anomaly seed-demo --force   # migrations + demo data
go run ./cmd/nxs-anomaly serve               # the API
go run ./cmd/nxs-anomaly run-worker          # the worker, separately
```

Migrations run automatically when the store initialises; there is no separate
migrate step to forget.

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
  loopback and link-local addresses. On by default under the production profile.
- `NXS_ANOMALY_EGRESS_ALLOWLIST` — hostnames and CIDRs delivery may reach at all.

## Interface language and timezone

The interface ships English and Russian and follows the browser; a person can
override both language and timezone in their own settings, and the choice is
stored per user rather than per installation.

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
- **Nothing happens at all** — the worker may not be running. `/health` carries
  `worker_cycles_completed`, and a zero that stays zero means `run-worker` is
  not up.
