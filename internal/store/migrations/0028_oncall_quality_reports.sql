-- On-call quality reports: the periodic executive digest (Enterprise).
--
-- One row per (team, period): missed ACKs, channel/delivery reliability, night
-- load, repeat escalations and noisy alert sources for the trailing window,
-- computed from the ClickHouse analytics views documented in
-- docs/enterprise/ru/INCIDENT_ANALYTICS_DASHBOARDS.md. The full digest lives in
-- `data`; team_id/period_start/period_end/generated_at are promoted to real
-- columns because listing "this team's reports, newest first" is the only
-- access pattern this table has (see internal/engine/report.go).
--
-- id is derived from (team_id, period_start), so a re-run for a period already
-- reported is a no-op rather than a duplicate row — see reportID.

CREATE TABLE IF NOT EXISTS nxs_anomaly_reports (
    id           text primary key,
    data         jsonb not null,
    team_id      text,
    period_start timestamptz,
    period_end   timestamptz,
    generated_at timestamptz,
    format       text,
    created_at   timestamptz default now(),
    updated_at   timestamptz default now()
);

CREATE INDEX IF NOT EXISTS nxs_anomaly_reports_team_generated_idx
    ON nxs_anomaly_reports (team_id, generated_at desc);
