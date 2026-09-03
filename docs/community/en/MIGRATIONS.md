# Migrations

*Русская версия: [MIGRATIONS.md](../ru/MIGRATIONS.md)*

`nxs-anomaly` manages its PostgreSQL schema through SQL migrations embedded in
the binary.

## Where they live

The canonical path:

```text
internal/store/migrations/*.sql
```

The files are embedded at build time:

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS
```

No other path is used for migrations.

## How they run

Migrations run automatically when the store is created:

```go
store.NewPostgreSQLStore(ctx)
```

In order:

1. connect to PostgreSQL;
2. take advisory lock `72544000`;
3. create `nxs_anomaly_schema_migrations` if it does not exist;
4. sort `migrations/*.sql` by file name;
5. for each file, check whether its version is recorded;
6. apply the missing ones, each in its own transaction;
7. record the applied version;
8. release the advisory lock.

Critical data updates use their own advisory lock keys inside the engine, so that
schema initialisation and domain operations never contend for one lock.

## The version table

```sql
CREATE TABLE IF NOT EXISTS nxs_anomaly_schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

A version is the file name without its `.sql` extension:

```text
0001_initial_schema
0002_typed_columns
```

## The migrations

| Version | What it does |
|---|---|
| `0001_initial_schema` | The JSONB tables for every entity, and the first expression indexes. |
| `0002_typed_columns` | Typed columns for hot-path fields: users, teams, schedules, integrations, chatops, mobile, alerts, alert\_groups, notifications. |
| `0003_constraints_and_notification_retries` | Retry metadata (`retry_count`, `next_retry_at`, `last_error`), a unique index on idempotency\_key, deferred foreign keys. |
| `0004_alertmanager_channels_and_batches` | A `source_type` column, the `nxs_anomaly_notification_batches` table, batch columns on notifications. |
| `0005_delivery_attempts` | `nxs_anomaly_notification_delivery_attempts`, for auditing and debugging delivery. |
| `0006_duty_epic_templates` | On-duty flags on users, epic-tracking fields on alert\_groups. |
| `0007_notification_batches_group_idx` | Typed `alert_group_id`/`integration_id` on batches, and an index for archiving finished groups. |
| `0008_delivery_scheduled_idx` | A partial index for `status='delivery_scheduled'` — the worker's hot path. |
| `0009_alerts_archival_idx` | An `alert_group_id` index on alerts, for the cascading cleanup during archival. |
| `0010_validate_constraints` | `VALIDATE CONSTRAINT` for everything added `NOT VALID` earlier. |
| `0011_grafana_compat` | Tables for a Grafana plugin compatibility layer. **The layer has been removed**; the tables remain unused — see "Retired tables" below. |
| `0013_soft_delete_integrations` | A `deleted_at` column on integrations plus a partial index. Soft delete: a removed integration disappears from listing and ingest but stays in the database. |
| `0015_chatops_messages_created_at_idx` | An expression index on `((data->>'created_at'))` for chatops messages, which speeds up the TTL archival of old messages. |
| `0016_due_groups_idx_predicate` | Rebuilds `nxs_anomaly_alert_groups_due_idx` with the predicate `status NOT IN ('resolved','silenced')`, matching what the worker actually queries — the old `status='open'` predicate did not cover acknowledged groups. |
| `0017_notification_claim` | A partial index on `(status) WHERE status IN ('delivering','retrying')` for claim-then-deliver — safe delivery across several workers — and for the reaper that frees stuck claims. |
| `0018_audit_events` | `nxs_anomaly_audit_events`, an append-only trail of every mutating operation. A group's timeline (`data->'logs'`) will not do: it is capped by `maxGroupLogs`, rewritten on every write to the group, and covers only groups. Append-only is held by a trigger rather than by grants, because the service owns its schema and connects as the table's owner — so pruning requires deliberately disabling the trigger. |
| `0019_identity_sessions` | `nxs_anomaly_user_credentials` and `nxs_anomaly_web_sessions`. Credentials live outside `nxs_anomaly_users.data` on purpose: that JSONB is returned verbatim through the generic collection endpoints, so a password hash inside it would be one `GET /api/v1/users` away from disclosure. Sessions store the SHA-256 of a token rather than the token, so a database dump does not hand over live sessions. |
| `0020_integration_team` | A `team_id` column on integrations. The team boundary is drawn here: alerts and alert groups already carry `integration_id`, so "my team's groups" is a filter on that rather than a copy of `team_id` that would go stale when an integration is reassigned. `NULL` means unassigned and visible to everyone. |
| `0021_notification_integration` | An `integration_id` column on notifications, so delivery records scope by team too. The one place where denormalisation is right: a copy of `team_id` would go stale on reassignment, a copy of `integration_id` cannot, because an alert group does not move between integrations. Before this, notifications and delivery attempts — including the message text — were readable by any authenticated user. |
| `0022_audit_request_id` | A `request_id` column on audit events plus a partial index over non-empty values. Every request already carries `X-Request-ID` in the access log and in error responses; without it on the audit row, "what else happened in that same request" was answered by matching timestamps, which stops working exactly when it is needed — under load, and when one request produces several events. |
| `0023_notification_policy_runs` | `nxs_anomaly_notification_policy_runs` plus indexes on `next_step_at` (active runs) and on the (group, user) pair. A personal notification policy is a long-lived state machine — notify → wait → fall back — driven by the worker cycle, and keeping it in the database is what lets it survive a restart. Finished runs are bookkeeping rather than history: they are marked `done` and swept during archival. |
| `0024_login_rate_buckets` | `nxs_anomaly_rate_buckets` plus an index on `updated_at`. The sign-in token bucket lives in the database so the limit is cluster-wide rather than per pod — otherwise a password guess would get N attempts against N replicas. The rows are ephemeral and cleaned by the worker's retention sweep. See [SECURITY_PROFILE.md](SECURITY_PROFILE.md). |
| `0025_audit_trace_id` | A `trace_id` column on audit events plus a partial index. It sits beside `request_id` from 0022: a request id ties together what one request did, a trace id ties a chain across processes and time — ingest in an API pod and delivery in a worker cycle an hour later are different requests. See [TRACING.md](TRACING.md). |
| `0026_maintenance_windows` | `nxs_anomaly_maintenance_windows` plus an index on `ends_at`. Planned work: integrations listed in a window still record alerts inside its bounds but do not page — the group is created `silenced`, and the dead-man switch stays quiet for them. Expand-only, and nothing outside the feature reads the table, so rolling back to an earlier release simply stops looking at it. |
| `0027_retention_indexes` | Indexes for the retention sweep: a partial one on `updated_at` of terminal notifications, ones on `created_at` of delivery attempts and web sessions, and one on `actor_id` in the audit trail. The sweep asks the same question — "the oldest N rows past the cutoff" — every worker cycle; without indexes that is a sequential scan of the largest tables every few seconds, on exactly the installation where they are large because retention was just switched on. The actor index also serves pseudonymisation when a user is deleted. See [DATA_INVENTORY.md](DATA_INVENTORY.md). |

## The connection to the store code

`internal/store/store.go` defines:

- `EntityTables` — collection name → PostgreSQL table;
- `TypedColumns` — the extra typed columns written on upsert;
- `typedValues` — the values for those columns.

Adding or removing a typed column in a migration means updating `TypedColumns`
and `typedValues` in the same change. Integration tests cover this path, because
the store writes through the real migrations.

The domain entities, their relationships and their life cycles are described in
[DOMAIN_MODEL.md](DOMAIN_MODEL.md). Migrations must stay compatible with that
model: a table holds the whole JSONB document, and typed columns are an indexable
projection of the hot-path fields.

## Adding a migration

1. Create the next file in order:

```text
internal/store/migrations/0028_short_description.sql
```

2. Make the operations idempotent where you can:

```sql
ALTER TABLE nxs_anomaly_alert_groups
    ADD COLUMN IF NOT EXISTS example text;

CREATE INDEX IF NOT EXISTS nxs_anomaly_example_idx
    ON nxs_anomaly_alert_groups (example);
```

3. If it adds a typed column, update `TypedColumns` and `typedValues` in
   `store.go`.

4. Add or update the tests covering the new path through the schema.

5. Check:

```bash
go test ./...
tests/run_postgres_integration.sh
```

## Operational notes

- Migrations are automatic; there is no separate CLI to run them.
- Back the database up before a production upgrade.
- For a heavy migration — backfilling a large table — roll out a single instance
  first.
- Avoid blocking DDL in an ordinary migration. The preferred pattern is additive
  nullable column → backfill → index → validate.
- Add constraints over existing data `NOT VALID`, then validate in a separate
  migration, as `0010_validate_constraints` does.

### Non-blocking migrations (`CREATE INDEX CONCURRENTLY`)

An ordinary migration runs inside a transaction, which is incompatible with
`CREATE INDEX CONCURRENTLY` — and which takes an ACCESS EXCLUSIVE lock on large
tables. To add an index without blocking, start the file with a marker:

```sql
-- nxs:no-transaction
CREATE INDEX CONCURRENTLY IF NOT EXISTS nxs_anomaly_example_idx
    ON nxs_anomaly_alerts (example);
```

Such a migration runs **outside a transaction**, and therefore **must** be
idempotent (`IF NOT EXISTS`): if it fails it can be left partly applied and
unrecorded. After a failed `CONCURRENTLY`, PostgreSQL leaves an INVALID index
behind, which has to be dropped before retrying.

## Checking what has been applied

```sql
SELECT version, applied_at
FROM nxs_anomaly_schema_migrations
ORDER BY version;
```

The expected set of versions is asserted by the Go integration tests.

## Retired tables

The Grafana plugin compatibility layer has been removed, together with the
`grafana_plugins` entity and the QR verification tokens. Five tables remain in
the schema and are read by nothing:

- `nxs_anomaly_grafana_plugins` (from `0001`);
- `nxs_anomaly_grafana_notification_policies`,
  `nxs_anomaly_grafana_channel_filters`, `nxs_anomaly_grafana_heartbeats`
  (from `0011`);
- `nxs_anomaly_mobile_verification_tokens` (from `0014`).

They were deliberately not dropped in the same release. The expand/contract rule
in [BACKUP_RESTORE.md](BACKUP_RESTORE.md) requires the previous binary to run
against the migrated schema — which the rollback drill verifies. A `DROP TABLE`
in the release that removes the code would make rolling back to the previous tag
impossible, because that binary still touches these tables.

They should be dropped by a separate migration in the **next** release, once
rolling back to a version with the layer is no longer a scenario.
