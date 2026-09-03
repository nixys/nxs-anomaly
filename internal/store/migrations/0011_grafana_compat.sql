-- Persistent storage for Grafana OnCall compatibility-layer resources.
-- Replaces the in-memory maps in internal/grafana/compat.go so the data
-- survives restarts and is consistent across replicas.

CREATE TABLE IF NOT EXISTS nxs_anomaly_grafana_notification_policies (
    id         text primary key,
    data       jsonb not null,
    user_id    text,
    created_at timestamptz default now(),
    updated_at timestamptz default now()
);

CREATE INDEX IF NOT EXISTS nxs_anomaly_grafana_np_user_idx
    ON nxs_anomaly_grafana_notification_policies (user_id);

CREATE TABLE IF NOT EXISTS nxs_anomaly_grafana_channel_filters (
    id         text primary key,
    data       jsonb not null,
    created_at timestamptz default now(),
    updated_at timestamptz default now()
);

CREATE TABLE IF NOT EXISTS nxs_anomaly_grafana_heartbeats (
    id         text primary key,
    data       jsonb not null,
    created_at timestamptz default now(),
    updated_at timestamptz default now()
);
