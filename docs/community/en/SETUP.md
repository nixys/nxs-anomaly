# Setup

Getting `nxs-anomaly` running locally, and the settings you meet on the way.
For prepared env files, systemd, Compose and Kubernetes, start with
[Installation](INSTALLATION.md).

## Requirements

- Docker, or a reachable PostgreSQL 14+
- Go 1.27 and Node.js 22.12+ — only to build from source

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
| `print-state` | every collection as JSON |
| `history` | alert group history (`--limit`, `--severity`, `--status`, `--integration`, `--from`, `--to`) |
| `alerts` | normalized alerts (`--limit`, `--status`, `--severity`, `--integration`) |
| `notifications` | notifications (`--limit`, `--status`, `--user`, `--group`) |

`serve` takes `--host`, `--port`, `--poll-interval`, `--no-scheduler`, `--api-key`,
`--tls-cert` and `--tls-key`; `run-worker` takes `--poll-interval`, `--once` and
`--worker-addr`. Each flag overrides the environment variable of the same meaning.

## Server

| Variable | Default | |
|---|---|---|
| `NXS_ANOMALY_LISTEN_HOST` | `0.0.0.0` | listen address |
| `NXS_ANOMALY_LISTEN_PORT` | `8080` | listen port |
| `NXS_ANOMALY_ADDR` | — | the whole `host:port`; overrides the two above |
| `NXS_ANOMALY_WORKER_ADDR` | `:8081` | where `run-worker` serves `/live`, `/ready` and `/metrics` |
| `NXS_ANOMALY_TLS_CERT` / `_KEY` | — | serve HTTPS directly; usually TLS ends at the ingress instead |
| `NXS_ANOMALY_START_SCHEDULER` | `true` | `false` — `serve` runs no worker cycle; run `run-worker` separately |
| `NXS_ANOMALY_POLL_INTERVAL` | `5` | seconds between worker cycles when nothing wakes it; an ingest wakes the worker at once through PostgreSQL `LISTEN/NOTIFY`; wake-started cycles are at least 200 ms apart, so a storm is handled in batches |
| `NXS_ANOMALY_WORKER_CYCLE_TIMEOUT_SECONDS` | — (off) | a deadline for one worker cycle; unset or `0` means none — a large delivery backlog can legitimately take long |
| `NXS_ANOMALY_SHUTDOWN_TIMEOUT` | `10` | seconds to finish requests in flight on SIGTERM |
| `NXS_ANOMALY_HTTP_READ_HEADER_TIMEOUT_SECONDS` | `5` | time to read request headers (slowloris protection) |
| `NXS_ANOMALY_HTTP_READ_TIMEOUT_SECONDS` | `15` | time to read a whole request |
| `NXS_ANOMALY_HTTP_WRITE_TIMEOUT_SECONDS` | `30` | time to write a response |
| `NXS_ANOMALY_HTTP_IDLE_TIMEOUT_SECONDS` | `60` | keep-alive idle connection |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. `debug` adds the diagnostic lines (`escalation_skipped`, `delivery_attempt` and others) |
| `LOG_FORMAT` | `text` | `text` or `json` |

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
| `NXS_ANOMALY_DB_POOL_MAX` | `10` | maximum connections. A process running the worker (`run-worker`, `serve` with its scheduler) needs at least 3: one is held for LISTEN, two more by a shard lock and the transaction under it; on a smaller pool it refuses to start |
| `NXS_ANOMALY_API_READ_CONCURRENCY` | half of `NXS_ANOMALY_DB_POOL_MAX` | API reads (GET) running at once per process; the rest of the pool stays free for ingest, the worker and writes. A read that waits over 10 s gets 503 + `Retry-After` |
| `NXS_ANOMALY_DB_POOL_MIN` | `1` | idle connections kept; `0` keeps none |
| `NXS_ANOMALY_DB_POOL_MAX_CONN_LIFETIME_SECONDS` | `3600` | shed connections after a failover or through a load balancer |
| `NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS` | `30` | per-connection `statement_timeout`; migrations are exempt. `0` sends no `statement_timeout` at all — required behind PgBouncer, which rejects startup parameters it is not told to ignore |
| `NXS_ANOMALY_DB_POOL_MAX_CONN_IDLE_SECONDS` | `1800` | close a connection idle this long |
| `NXS_ANOMALY_DB_POOL_HEALTHCHECK_SECONDS` | `60` | how often idle connections are checked |
| `NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS` | `0` | retry the first connection for N seconds. Set it above zero under Kubernetes: the pod may start before the database accepts connections |

**PostgreSQL behind PgBouncer.** Supported: a direct connection, or PgBouncer
in **session** pooling (`pool_mode = session`). **Transaction and statement
pooling are not supported**, and neither `NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS=0`
nor a bigger pool makes them work, because the service relies on session state:

- migrations run under a session-level `pg_advisory_lock`, with
  `SET statement_timeout = 0` / `RESET` around them on the same connection;
- the worker holds a session-level `pg_try_advisory_lock` per shard while the
  work under it runs on other pooled connections;
- the worker keeps one connection with a standing `LISTEN` for wake-ups (it
  falls back to polling, but the locks above have no fallback);
- queries use pgx's cached prepared statements.

In session pooling each of the service's connections holds a server
connection for its lifetime, so size PgBouncer for the sum of
`NXS_ANOMALY_DB_POOL_MAX` over every API and worker replica (`default_pool_size`
and `max_client_conn`), and set `NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS=0` or
add `statement_timeout` to `ignore_startup_parameters`. Prepared statements need
no extra setting in session mode. Supporting transaction pooling would mean
moving coordination to transaction-scoped locks or leases and keeping the
listener and migrations on a separate direct connection — tracked in GitHub #55.

`0` is accepted only where the table says what it means. For the pool size, the
connection lifetime, idle time and health-check period (`…_POOL_MAX`,
`…_MAX_CONN_LIFETIME_SECONDS`, `…_MAX_CONN_IDLE_SECONDS`, `…_HEALTHCHECK_SECONDS`)
and for the HTTP timeouts and `NXS_ANOMALY_SESSION_TTL_SECONDS` the minimum is
`1`: to the database driver and to the HTTP server `0` does not mean "off" but
"close every connection" or "no timeout". A value that does not parse or is below
the minimum keeps the default and is logged at start-up as `invalid_setting`
with the variable's name.

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
| `NXS_ANOMALY_TEAM_SCOPING` | `false`. `true` limits each user to the objects of their teams and the objects with no team; administrators, API keys and the worker are not limited. See [API.md](API.md#team-scoping) |
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
| `NXS_ANOMALY_CHATOPS_STATUS_UPDATES` | `false`. With it on, the ChatOps channels a group was posted to are told when it is acknowledged, unacknowledged or resolved. Independent of `NXS_ANOMALY_NOTIFY_ON_RESOLVE` — see [CONFIGURATION.md](CONFIGURATION.md#8-chatops) |

Provider credentials for email, Telegram, Slack and Mattermost are covered with
the channels themselves in [CONFIGURATION.md](CONFIGURATION.md) — §7 for SMTP,
§8 for the chat bots — and the outbound proxy in its §9 and in
[PROXY.md](PROXY.md).

### Phone calls (Asterisk)

The `call` channel originates a call through the Asterisk Manager Interface
(AMI, always port `5038`). Without an instance a `call` notification is
`skipped`.

| Variable | Default | |
|---|---|---|
| `NXS_ANOMALY_ASTERISK_HOST` | — | AMI host name, without a port |
| `NXS_ANOMALY_ASTERISK_USERNAME` / `_SECRET` | — | AMI credentials |
| `NXS_ANOMALY_ASTERISK_CHANNEL` | — | e.g. `SIP/trunk/`; the phone number is appended to it |
| `NXS_ANOMALY_ASTERISK_CONTEXT` / `_EXTEN` | — | where the answered call goes in your dialplan |
| `NXS_ANOMALY_ASTERISK_PRIORITY` | `1` | dialplan priority |
| `NXS_ANOMALY_ASTERISK_CALLER_ID` | `nxs-anomaly` | caller ID shown on the phone |
| `NXS_ANOMALY_ASTERISK_TRIGGER_VARIABLE` | `TRIGGER_MESSAGE` | channel variable that carries the alert text into the dialplan |

`HOST`, `USERNAME`, `SECRET`, `CHANNEL`, `CONTEXT` and `EXTEN` are all
required; with any of them missing the call fails with `settings incomplete`.
For several PBXes, number the same variables from zero —
`NXS_ANOMALY_ASTERISK_0_HOST`, `NXS_ANOMALY_ASTERISK_1_HOST` and so on, with no
gaps: the first missing number ends the list. A call tries them in order until
one succeeds. Once `…_0_HOST` is set, the unnumbered variables are not read.

### Mobile push relay

| Variable | |
|---|---|
| `NXS_ANOMALY_MOBILE_PUSH_URL` | your relay that turns a notification into a real push. Without it the `mobile` channel has no transport and its notifications are `skipped`, not `delivered`. A phone paired through the app does not need it: it holds the event stream open instead (see [API.md](API.md)) |
| `NXS_ANOMALY_MOBILE_PUSH_TOKEN` | optional `Authorization: Bearer` for the relay |

## Hardening

`NXS_ANOMALY_PROFILE=production` turns on the safe defaults together: the SSRF
guard, request rate limits and the refusal to store new inline secrets. Every
one of them can still be set individually. The delivery circuit breaker is on
in every profile.

Two that are worth knowing by name:

- `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS=true` — outbound delivery refuses private,
  loopback and link-local addresses. On by default under the production profile
  and in the Helm chart's values; the binary's own default is off. A receiver in
  your own network is let through by naming it in
  `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS_EXCEPT` (see
  [SECURITY_PROFILE.md](SECURITY_PROFILE.md#exceptions-for-internal-receivers)).
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
- `NXS_ANOMALY_CIRCUIT_BREAKER_THRESHOLD` — consecutive failures on one channel and
  target before delivery to it is short-circuited (`5` by default in every
  profile; `0` turns the breaker off), and
  `NXS_ANOMALY_CIRCUIT_BREAKER_COOLDOWN_SECONDS` (`30`) — how long it stays open
  before a trial call.
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
