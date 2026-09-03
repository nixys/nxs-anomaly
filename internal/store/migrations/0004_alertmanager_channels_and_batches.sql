CREATE TABLE IF NOT EXISTS nxs_anomaly_notification_batches (
    id text PRIMARY KEY,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    batch_key text,
    status text,
    flush_at timestamptz,
    deadline_at timestamptz
);

ALTER TABLE nxs_anomaly_integrations
    ADD COLUMN IF NOT EXISTS source_type text;

ALTER TABLE nxs_anomaly_notifications
    ADD COLUMN IF NOT EXISTS batch_id text,
    ADD COLUMN IF NOT EXISTS batch_key text,
    ADD COLUMN IF NOT EXISTS provider_status text;

UPDATE nxs_anomaly_integrations
SET source_type = COALESCE(data->>'source_type', data->>'type', type)
WHERE source_type IS NULL;

UPDATE nxs_anomaly_notifications
SET batch_id = data->>'batch_id',
    batch_key = data->>'batch_key',
    provider_status = data->>'provider_status'
WHERE batch_id IS NULL
   OR batch_key IS NULL
   OR provider_status IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS nxs_anomaly_notification_batches_key_open_idx
    ON nxs_anomaly_notification_batches (batch_key)
    WHERE status = 'open';

CREATE INDEX IF NOT EXISTS nxs_anomaly_notification_batches_due_idx
    ON nxs_anomaly_notification_batches (flush_at, deadline_at)
    WHERE status = 'open';

CREATE INDEX IF NOT EXISTS nxs_anomaly_notifications_batch_idx
    ON nxs_anomaly_notifications (batch_id)
    WHERE batch_id IS NOT NULL;
