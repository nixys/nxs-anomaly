-- 10.1 — add alert_group_id typed column to notification_batches for SQL history filtering
ALTER TABLE nxs_anomaly_notification_batches
    ADD COLUMN IF NOT EXISTS alert_group_id text,
    ADD COLUMN IF NOT EXISTS integration_id text;

UPDATE nxs_anomaly_notification_batches
SET alert_group_id = data->>'alert_group_id',
    integration_id = data->>'integration_id'
WHERE alert_group_id IS NULL;

CREATE INDEX IF NOT EXISTS nxs_anomaly_notification_batches_group_idx
    ON nxs_anomaly_notification_batches (alert_group_id)
    WHERE alert_group_id IS NOT NULL;

-- 10.2 — index for TTL cleanup: resolved groups older than cutoff
CREATE INDEX IF NOT EXISTS nxs_anomaly_alert_groups_resolved_at_idx
    ON nxs_anomaly_alert_groups (resolved_at)
    WHERE status = 'resolved' AND resolved_at IS NOT NULL;
