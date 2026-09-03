-- Soft-delete for integrations.
-- Deleted integrations are hidden from key lookups but retained for audit/history.
ALTER TABLE nxs_anomaly_integrations ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS nxs_anomaly_integrations_deleted_at_idx
    ON nxs_anomaly_integrations (deleted_at) WHERE deleted_at IS NOT NULL;
