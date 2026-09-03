# Capacity and chaos envelope

*Русская версия: [CAPACITY.md](../ru/CAPACITY.md)*

How to measure what nxs-anomaly can take, and to confirm that under beta load it
holds its targets and loses no notifications when things break. The harness and
the acceptance rules live in
[`tests/loadtest/loadtest.go`](../../../tests/loadtest/loadtest.go); the runner is
[`tests/run_load_test.sh`](../../../tests/run_load_test.sh).

## What is measured

The harness drives the engine against a real PostgreSQL with fixed profiles,
embeds the ingest timestamp in the alert's title, and counts on the webhook stub
side:

- **ingest→first-attempt latency** — from `IngestAlert` to the first delivery for
  the group (p50/p95/p99/max);
- **loss** — whether every group was delivered
  (`delivered_groups == alerts`);
- **duplicates** — whether a group reached its recipients more than once
  (`double_deliveries`), including across replicas;
- **backlog drain** — whether the `delivery_scheduled/retry/delivering` queue
  returned to zero after ingest stopped.

## Profiles and scenarios

`run_load_test.sh` runs a matrix (by default `alerts=1500`, `rate=150/s`,
overridable through `LOADTEST_ALERTS`/`LOADTEST_RATE`):

| Profile | workers | integrations | scenario | What it checks |
|---|---|---|---|---|
| baseline-1 | 1 | 1 | baseline | a single worker replica |
| baseline-2 | 2 | 1 | baseline | two replicas, no duplicates |
| baseline-4 | 4 | 1 | baseline | scaling by replicas |
| baseline-multi | 2 | 8 | baseline | ingest spread across sources, so per-integration serialisation is no longer shared |
| slow-provider | 2 | 1 | `provider_timeout` | ~20% of deliveries are slow (2s) |
| worker-kill | 2 | 1 | `worker_kill` | a replica is killed mid-run — nothing is lost |
| retry-storm | 2 | 1 | `retry_storm` | the provider returns 500 for the first third — everything still arrives |

Each profile carries its own drain budget: it is a property of the scenario, not
of the suite. All profiles are declared in one list in
`tests/run_load_test.sh`; every profile notifies three recipients.

## Acceptance rules (the harness exits 1)

- **p95 ingest→first-attempt below 10 s** for every profile except
  `provider_timeout`, where the delay is injected deliberately and what is being
  checked is the absence of loss rather than latency;
- **no more duplicates than reclaims**
  (`double_deliveries <= reclaimed_stale_claims`), multi-worker included. On
  healthy profiles there are no reclaims and the threshold is zero. On
  `worker_kill` it cannot be zero: a worker killed between the POST to the
  provider and the save leaves a claim, the reaper returns it, and the resend is
  at-least-once working as designed rather than a defect. A duplicate with no
  reclaim behind it means two workers held one notification — a real loss of
  exclusivity, and that is what must fail;
- **zero loss** — with the backlog drained, every group was delivered;
- **the backlog drains** within `-drain-timeout`: all four in-flight statuses are
  empty, including both transitional claims (`delivering`, `retrying`).

`worker_kill` and `retry_storm` are the direct check that a crash does not lose a
notification: the claim is reclaimed (`ReclaimStaleClaims`) and re-sent by
another replica. For `worker_kill` the harness shortens the claim lease to 15 s.
The default (`defaultClaimTimeout`, floor five minutes) is derived from the worst
delivery stage and is longer than any sensible drain budget, so with it the
scenario only passed when the kill happened to land between deliveries — that is,
when it left nothing behind and the reclaim path the scenario exists for never
ran at all.

## Running it

```bash
# Starts a throwaway postgres:17-alpine and runs every profile:
tests/run_load_test.sh

# Against an existing database:
NXS_ANOMALY_TEST_DATABASE_URL='postgres://user:pass@host:5432/db?sslmode=disable' \
  tests/run_load_test.sh

# One profile by hand:
go run -tags loadtest ./tests/loadtest \
  -alerts 2000 -rate 200 -workers 2 -recipients 3 -scenario baseline
```

Each profile prints a JSON report (`p95_seconds`, `double_deliveries`,
`reclaimed_stale_claims`, `backlog_drained`, `pass`, …).

## The measured envelope

> These numbers depend on CPU, disk and PostgreSQL latency — there is no
> canonical figure, so re-run the harness in your own environment. Below is a
> reference run of `alerts=1500, recipients=3` (4500 deliveries per profile) on a
> development machine: WSL2, 4 vCPU, `postgres:17-alpine` in Docker.

| Profile | workers | integrations | drain | ingest | alerts/s | p95 | duplicates | loss |
|---|---|---|---|---|---|---|---|---|
| baseline | 1 | 1 | 60s | 72.1s | 20.8 | 0.20s | 0 | 0 |
| baseline | 2 | 1 | 60s | 77.0s | 19.5 | 0.16s | 0 | 0 |
| baseline | 4 | 1 | 60s | 73.5s | 20.4 | 0.12s | 0 | 0 |
| baseline | 2 | 8 | 60s | 78.8s | 19.0 | 0.16s | 0 | 0 |
| provider_timeout | 2 | 1 | 180s | 52.5s | 28.6 | 68.08s | 0 | 0 |
| worker_kill | 2 | 1 | 90s | 73.6s | 20.4 | 0.20s | 3 (= reclaims) | 0 |
| retry_storm | 2 | 1 | 90s | 74.4s | 20.2 | 0.22s | 0 | 0 |

What it says:

- **Ingest throughput is ~20 alerts/s**, and it no longer depends on how many
  groups are already open. Ingest used to load every unresolved group of the
  integration inside the advisory lock, so the cost of one alert grew linearly
  with the backlog: 16.6 ms at 50 open groups, 148.1 ms at 800 — about 0.174 ms
  per group, making a burst of n alerts cost O(n²). Closing those 800 groups
  brought latency back to 17.0 ms, which is what identified the cause. With a
  narrowed LoadSpec the same 1500-alert run takes 81.5 s instead of 204.6 s.
- **The number of workers does not affect ingest** (20.8 / 19.5 / 20.4 alerts/s
  at 1 / 2 / 4 replicas): the bottleneck is on the ingest path, not in delivery.
  Workers shorten the delivery tail — p95 falls from 0.222 s to 0.136 s — but do
  not move ingest throughput.
- **Nor does the number of integrations** (19.0 alerts/s across eight against
  20.8 on one). Ingest serialises per integration through an advisory lock, so
  spreading the load across sources should in theory have scaled. It does not.
  So once the group loading was removed, the ceiling lies in something shared —
  the connection pool, or writes to PostgreSQL — rather than in the
  per-integration lock. That is what to measure next; narrowing the lock is not
  worth doing.
- **Baseline p95 ingest→first-attempt is 0.12–0.20 s**, two orders of magnitude
  below the 10 s target.
- **provider_timeout** and **retry_storm** produce high latency for the affected
  share of deliveries, which is the point of them (a 2-second provider and retry
  backoff), so the p95 gate applies to baseline only. Both integrity properties
  still hold.
- **The drain budget belongs to the profile, not to one constant.**
  provider_timeout holds ~20% of 4500 deliveries behind a 2-second provider; it
  fit inside 60 s only because slow ingest spread the work out. Once ingest got
  faster, the same run delivered 1335/1500 within 60 s and 1500/1500 within
  180 s — nothing was lost, the drain simply had less time. An acceptance
  criterion that depends on the producer being slow is a coincidence, not a
  criterion.
- **worker_kill** — a replica killed mid-run — loses nothing: abandoned claims
  are reclaimed and re-sent by the second replica. The duplicates are exactly the
  ones the reclaims explain (3 out of 4500 in the run above): a notification the
  killed worker had already handed to the provider but had not yet saved arrives
  twice. That is the price of at-least-once, not a defect; a zero here would mean
  the kill touched nothing.
- **Absolute numbers depend heavily on the machine.** The run behind the table
  above and the run before the LoadSpec fix were made on the same machine under
  different background load: ~73 s against 204.6 s. That comparison on its own is
  not controlled. What is controlled is the back-to-back "latency versus number
  of open groups" curve, and the reverse experiment of closing the groups; those
  are what demonstrate the mechanism. Re-run the harness on your own hardware and
  compare it with itself.

The `DB restart` scenario from the original brief is not injected directly (that
needs an external container orchestrator); the claim reclaim and outbox retry it
would exercise are covered by `worker_kill`/`retry_storm` and by the integration
suite.

## Suggested starting resources

For the target beta audience — one to three SRE teams, tens of integrations,
peaks in the hundreds of alerts per minute:

| Component | Replicas | CPU (req/limit) | Memory (req/limit) | Notes |
|---|---|---|---|---|
| API (`serve --no-scheduler`) | 2 | 100m / 500m | 128Mi / 256Mi | scales horizontally |
| Worker (`run-worker`) | 1–2 | 200m / 1000m | 128Mi / 256Mi | the claim model allows several replicas without duplicates |
| PostgreSQL | 1 (+HA to taste) | 500m / 2000m | 512Mi / 2Gi | the hot path is claiming and upserting notifications |

The database pool is set by `NXS_ANOMALY_DB_POOL_MIN`/`_MAX`; if
`nxs_anomaly_db_pool_empty_acquire_total` grows under load, raise `_MAX` — but
see the note in [SETUP.md](SETUP.md): a larger pool helps only when the database
has CPU to spare. The worker-stall threshold used by readiness is
`NXS_ANOMALY_WORKER_STALL_TIMEOUT_SECONDS`, defaulting to `max(30s, 4×poll)`.
