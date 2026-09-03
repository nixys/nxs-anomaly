CREATE TABLE IF NOT EXISTS nxs_anomaly_notification_delivery_attempts (
    id text PRIMARY KEY,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    notification_id text,
    channel text,
    target text,
    attempt integer NOT NULL DEFAULT 0,
    status text,
    started_at timestamptz,
    finished_at timestamptz
);

ALTER TABLE nxs_anomaly_notification_delivery_attempts
    ADD CONSTRAINT nxs_anomaly_delivery_attempts_notification_fk
    FOREIGN KEY (notification_id) REFERENCES nxs_anomaly_notifications(id)
    ON DELETE CASCADE NOT VALID;

CREATE INDEX IF NOT EXISTS nxs_anomaly_delivery_attempts_notification_idx
    ON nxs_anomaly_notification_delivery_attempts (notification_id, attempt);

CREATE INDEX IF NOT EXISTS nxs_anomaly_delivery_attempts_channel_status_idx
    ON nxs_anomaly_notification_delivery_attempts (channel, status);
