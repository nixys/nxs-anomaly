# Backup, restore, upgrade and rollback

*Русская версия: [BACKUP_RESTORE.md](../ru/BACKUP_RESTORE.md)*

All of nxs-anomaly's persistent state lives in **one PostgreSQL database** —
alerts, alert groups, users, schedules, escalation chains, integrations,
notification state and the append-only audit trail. The application keeps state
nowhere else. So the whole disaster-recovery story is the PostgreSQL backup
story, described below.

## Recovery targets

| Target | Goal | How it is met |
|---|---|---|
| **RPO** (maximum data loss) | ≤ 5 min | Continuous WAL archiving / PITR, or a managed instance with PITR granularity ≤ 5 min. A nightly `pg_dump` alone is **not enough**. |
| **RTO** (time to service restored) | ≤ 30 min | Measured from the incident to the moment **`/readiness` reports `database` and `worker` = ok** — a restored database nobody can be paged from is not a service. The automated drill measures this on every run and fails if the budget is exceeded. |

RPO is a property of **how often you copy**, not of how fast you restore: a daily
dump gives an RPO of up to 24 hours no matter how quick the restore is. To stay
within five minutes you need PITR (WAL archiving) or a managed PostgreSQL with a
point-in-time window of ≤ 5 minutes. Keep the logical `pg_dump` as the portable,
version-independent secondary option.

## Backup

### Option A — managed PostgreSQL (recommended for production)

Turn on the provider's automatic backups and PITR with retention covering your
recovery window, and set the PITR granularity to ≤ 5 minutes. Nothing
application-specific is required: there is no state outside the database.

### Option B — self-managed PITR

Archive WAL continuously (`archive_mode=on`, `archive_command=…` to object
storage) plus a periodic base backup (`pg_basebackup`). That is what gives an RPO
of ≤ 5 minutes on a self-managed instance.

### Option C — a logical dump (portable; always keep one)

```bash
pg_dump --format=custom --file=nxs-anomaly-$(date -u +%Y%m%dT%H%M%SZ).dump \
  "postgres://USER:PASS@HOST:5432/nxs_anomaly?sslmode=require"
```

Store dumps outside the cluster (object storage), encrypted, with a lifecycle and
retention policy.

A backup is a separate copy of personal data. Calling
`POST /api/v1/users/{id}/erase` changes the live database but does not rewrite
WAL, snapshots or dumps already made. How long they are kept, and how a deleted
profile is prevented from "returning" after a restore, have to be part of the
operator's procedure; the application honestly lists backups under
`out_of_scope` in the erasure response. See
[DATA_INVENTORY.md](DATA_INVENTORY.md).

> Secrets and configuration are **not** in the database — they come from a
> Kubernetes Secret and ConfigMap (see the Helm chart). Back them up through your
> usual cluster or GitOps process; a database backup does not contain them.

### Telling nxs-anomaly that a backup exists

All three options above happen **outside** the application: a provider snapshot,
a WAL archive and a dump in object storage are invisible to it. So the readiness
check (the Setup page, `GET /api/v1/readiness`) cannot observe your backups — the
job that makes them has to say so:

```bash
curl -fsS -X POST "$NXS_ANOMALY_URL/api/v1/backups/report" \
  -H "X-API-Key: $NXS_ANOMALY_ADMIN_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"pg_dump"}'
```

Call it **only after the copy has finished successfully** — calling it before
would turn the check into a report of intent rather than of fact. Both fields are
optional: `at` (ISO-8601) defaults to now, and `kind` is a free-form label shown
in the report.

The endpoint requires an **admin** key. It records an assertion the readiness
gate then trusts, so a lesser credential should not be able to make it; the
backup job already has access to a database dump, which is the greater privilege
anyway.

Readiness treats the backup as a **blocker** if none was ever reported or the
last one is older than 26 hours — the RPO above plus room for a night job that
ran late. A pilot deliberately running without backups can record that decision
on the readiness page rather than quietly ignoring the warning.

## Restore

1. Prepare an empty database — a fresh managed instance, or a restored PITR
   point.
2. Restore:
   - PITR: recover to the chosen timestamp with the provider's tooling or through
     `recovery_target_time`;
   - a logical dump:
     `pg_restore --dbname="postgres://USER:PASS@HOST:5432/nxs_anomaly" --clean --if-exists nxs-anomaly-….dump`
3. Point the application's `NXS_ANOMALY_DB_DSN` at the restored database. It
   applies any missing migrations itself at startup, under an advisory lock — no
   separate Job is needed, and starting replicas in parallel is safe.
4. **Check that an alert flows through**, not just that the rows are there. The
   automated drill below does exactly that: the canary integration is in place,
   and a freshly ingested alert opens a new group.

## The automated drills

There are two, because "the database is back" and "the service is back" are
different claims, and because an RPO of five minutes rests on WAL rather than on
dumps.

### 1. Logical restore, service readiness and rollback (`tests/restore_drill.sh`)

Wired into CI as a restore-drill job:

```
seed a full alert flow → pg_dump (this is the recovery point / RPO)
  → DROP the database (the incident) → restore from the dump
  → check that the seeded data survived AND that a fresh ingest still opens
    an alert group
  → start the real API and worker against the restored database and wait until
    GET /api/v1/readiness reports database=ok AND worker=ok
  → run the PREVIOUS release against the restored schema and accept an alert
    through it
  → check RTO ≤ budget (1800 s by default)
```

**RTO is measured to service readiness, not to the end of the import.** A
restored database nobody can be paged from is not a restored service, and the
difference is not academic: a local run shows ~5 s of import and another ~13 s
until readiness turns green. Reporting only the first number would understate
recovery threefold. The readiness gate is the product's own answer (`worker=ok`
requires the worker to have written a heartbeat into the restored database),
rather than a substitute for it.

The last step is **the rollback path from the migration contract below, tested
rather than declared**: the previous release tag is built, pointed at the
restored and already-migrated schema, and must both answer `/health` and accept
an alert. The step fails loudly if the previous tag is absent — it will be in a
shallow clone, which is why CI sets `GIT_DEPTH: 0` — and skipping it requires an
explicit `NXS_ANOMALY_DRILL_SKIP_ROLLBACK=1`, which is reported in the output.

Locally (needs Docker; starts its own throwaway postgres):

```bash
bash tests/restore_drill.sh
```

Against an existing database, as CI does, set `NXS_ANOMALY_TEST_DATABASE_URL` and
the `NXS_ANOMALY_DRILL_DUMP` / `_PSQL` / `_RESET` commands. The checking half is a
Go test, `tests/restore_drill_test.go` behind the `restoredrill` tag, so it works
through the real store and engine rather than an SQL approximation.

### 2. Point-in-time recovery (`tests/pitr_drill.sh`)

Wired into CI as a PITR drill under docker-in-docker — the drill configures
`archive_mode` and rebuilds the data directory, which a database provided as a CI
service cannot do:

```
enable WAL archiving → base backup → write, mark the target point, write again
  → destroy the cluster → restore the base backup and replay WAL to the target
  → check: the write before the point is back, the write AFTER the point is
    ABSENT, the application schema survived, the cluster came up writable
```

The meaningful check is the **absence** of the write made after the target point.
A restore that replays WAL to the end is indistinguishable from a successful PITR
on a green run — and would then quietly fail to undo an accidental `DELETE`,
which is the entire reason PITR exists. The drill also waits for
`pg_is_in_recovery() = false` rather than `pg_isready`: a cluster still replaying
its log accepts connections and fails every write with "read-only transaction".

```bash
bash tests/pitr_drill.sh
```

**Scope:** the drill proves the PITR *mechanism* works against the real schema. On
a managed PostgreSQL the same capability lives behind the provider's console or
API, and on the target cluster a restore is an infrastructure operation: perform
it with the provider's tooling, then run the readiness-check step from drill 1
against the restored instance. That combination on the real production topology
remains a manual DR acceptance step.

Both drills are written to be run **by another person or agent following this
document** — that is the acceptance bar.

## Migrations: the backward-compatibility contract

Migrating automatically at startup is convenient, but it is only safe together
with a rollback contract. The rule:

> **A migration must leave the schema readable and writable by the previously
> released version of the application, for at least one release.**

Concretely, within one release a migration may make only **additive, backward
compatible** changes:

- ✅ add a table, a nullable column, a new index, a new default;
- ✅ backfill data; add a constraint — but only after the code stopped violating
  it;
- ❌ drop or rename a column or table the previous release still reads;
- ❌ narrow a type, or add NOT NULL without a default, in the same release.

Destructive changes take **two releases, expand then contract**: release *N* adds
the new shape and writes to both; release *N+1* — once *N* is fully rolled out
and you no longer intend to roll back past it — removes the old one. That is what
makes "upgrade, watch, roll back if needed" safe: rolling back to *N* works
against a schema migrated by *N+1*, because *N+1* only removed what *N* had
already stopped using.

The rule is not merely documented but checked: the last step of
`tests/restore_drill.sh` runs the previous release against the migrated schema
and fails the build if it cannot serve requests and accept alerts.

Migrations live in `internal/store/migrations/NNNN_*.sql` and are applied in
order, once.

## Upgrade and rollback

**Before any upgrade:**

1. Take a fresh backup — the option C dump is quick and portable — and note its
   timestamp: that is your recovery point if you roll back.
2. Read the new migrations in `internal/store/migrations/` against the contract
   above.

**Upgrade** (Helm; the images and the chart share a version):

```bash
helm upgrade nxs-anomaly oci://ghcr.io/nixys/nxs-anomaly \
  --version <new> -f values-production.yaml --reuse-values
```

Migrations run at startup. Watch the worker's `/ready` and the delivery metrics.

**Rollback:**

- *Application only* — the usual case, when the new migrations are additive: roll
  the images back. The previous version runs against the migrated schema because
  the changes were backward compatible.

  ```bash
  helm rollback nxs-anomaly            # the previous release
  # or: helm upgrade … --version <previous>
  ```

- *The schema too* — only if a release broke the contract, or data has to be
  recovered: restore the backup taken before the upgrade and deploy the previous
  version onto it. Anything written between the backup and the rollback is lost,
  within your RPO.

Never roll the application back **past** a release whose migration has already
run the contract phase and dropped the old columns. That is exactly the boundary
the two-release rule protects.
