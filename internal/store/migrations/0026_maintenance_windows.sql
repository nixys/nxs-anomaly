-- Maintenance windows: planned work during which an integration's alerts are
-- recorded but nobody is paged.
--
-- The service already has everything needed to not page: a group in status
-- 'silenced' is excluded from the worker's due-scan (see ListDueAlertGroups),
-- shows as silenced in the UI, and keeps its full alert history. A window is
-- therefore not a new suppression mechanism — it is a rule that silences the
-- groups an integration opens while the window is open, until the window ends.
--
-- This matters most for the dead-man switch (0025-era heartbeat work): a source
-- taken down for planned work is a silent source, and without windows every
-- planned maintenance raises SourceSilent at the exact moment the people who
-- would answer it are already busy doing the maintenance.
--
-- Expand-only: creating a table is reversible by dropping it, and nothing
-- outside this feature reads it, so a rollback to the previous release leaves
-- the rows in place and simply stops consulting them.

CREATE TABLE IF NOT EXISTS nxs_anomaly_maintenance_windows (
    id         text primary key,
    data       jsonb not null,
    team_id    text,
    starts_at  timestamptz,
    ends_at    timestamptz,
    created_at timestamptz default now(),
    updated_at timestamptz default now()
);

-- The lookup on every ingest: windows that have not ended yet. Past windows are
-- kept as a record of what was planned, but they are never scanned again.
CREATE INDEX IF NOT EXISTS nxs_anomaly_maintenance_windows_ends_idx
    ON nxs_anomaly_maintenance_windows (ends_at);
