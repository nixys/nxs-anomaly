ALTER TABLE nxs_anomaly_notifications
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS retry_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_retry_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_error text;

UPDATE nxs_anomaly_notifications
SET idempotency_key = data->>'idempotency_key',
    retry_count = COALESCE((data->>'retry_count')::integer, retry_count, 0),
    next_retry_at = NULLIF(data->>'next_retry_at', '')::timestamptz,
    last_error = data->>'last_error'
WHERE idempotency_key IS NULL
   OR retry_count IS NULL
   OR next_retry_at IS NULL
   OR last_error IS NULL;

ALTER TABLE nxs_anomaly_teams
    ADD CONSTRAINT nxs_anomaly_teams_name_not_empty
    CHECK (name IS NULL OR length(name) > 0) NOT VALID;

ALTER TABLE nxs_anomaly_integrations
    ADD CONSTRAINT nxs_anomaly_integrations_key_not_empty
    CHECK (key IS NULL OR length(key) > 0) NOT VALID;

ALTER TABLE nxs_anomaly_integrations
    ADD CONSTRAINT nxs_anomaly_integrations_type_not_empty
    CHECK (type IS NULL OR length(type) > 0) NOT VALID;

ALTER TABLE nxs_anomaly_mobile_sessions
    ADD CONSTRAINT nxs_anomaly_mobile_sessions_user_fk
    FOREIGN KEY (user_id) REFERENCES nxs_anomaly_users(id)
    ON DELETE CASCADE NOT VALID;

ALTER TABLE nxs_anomaly_mobile_sessions
    ADD CONSTRAINT nxs_anomaly_mobile_sessions_device_fk
    FOREIGN KEY (device_id) REFERENCES nxs_anomaly_mobile_devices(id)
    ON DELETE CASCADE NOT VALID;

ALTER TABLE nxs_anomaly_alerts
    ADD CONSTRAINT nxs_anomaly_alerts_integration_fk
    FOREIGN KEY (integration_id) REFERENCES nxs_anomaly_integrations(id)
    ON DELETE CASCADE NOT VALID;

ALTER TABLE nxs_anomaly_alert_groups
    ADD CONSTRAINT nxs_anomaly_alert_groups_integration_fk
    FOREIGN KEY (integration_id) REFERENCES nxs_anomaly_integrations(id)
    ON DELETE CASCADE NOT VALID;

ALTER TABLE nxs_anomaly_notifications
    ADD CONSTRAINT nxs_anomaly_notifications_group_fk
    FOREIGN KEY (alert_group_id) REFERENCES nxs_anomaly_alert_groups(id)
    ON DELETE CASCADE NOT VALID;

ALTER TABLE nxs_anomaly_notifications
    ADD CONSTRAINT nxs_anomaly_notifications_user_fk
    FOREIGN KEY (user_id) REFERENCES nxs_anomaly_users(id)
    ON DELETE CASCADE NOT VALID;

CREATE UNIQUE INDEX IF NOT EXISTS nxs_anomaly_notifications_idempotency_unique_idx
    ON nxs_anomaly_notifications (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS nxs_anomaly_notifications_retry_due_idx
    ON nxs_anomaly_notifications (next_retry_at)
    WHERE status = 'retry_scheduled' AND next_retry_at IS NOT NULL;
