ALTER TABLE nxs_anomaly_alerts
    ADD COLUMN IF NOT EXISTS alert_group_id text;

UPDATE nxs_anomaly_alerts
    SET alert_group_id = data->>'alert_group_id'
    WHERE alert_group_id IS NULL;

CREATE INDEX IF NOT EXISTS nxs_anomaly_alerts_alert_group_id_idx
    ON nxs_anomaly_alerts (alert_group_id);
