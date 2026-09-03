-- Mobile verification tokens persisted in PostgreSQL.
-- Replaces the in-memory map in grafana/compat.go.
CREATE TABLE IF NOT EXISTS nxs_anomaly_mobile_verification_tokens (
    token      TEXT        PRIMARY KEY,
    user_id    TEXT        NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS nxs_anomaly_mobile_verification_tokens_expires_idx
    ON nxs_anomaly_mobile_verification_tokens (expires_at);
