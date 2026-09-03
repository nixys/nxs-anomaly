-- 1.1/1.2 — on_duty flag and priority per user
ALTER TABLE nxs_anomaly_users
    ADD COLUMN IF NOT EXISTS on_duty boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS priority text NOT NULL DEFAULT 'medium';

-- 1.5 — epic alert tracking per alert group
ALTER TABLE nxs_anomaly_alert_groups
    ADD COLUMN IF NOT EXISTS alert_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS epic_sent_at timestamptz;

CREATE INDEX IF NOT EXISTS nxs_anomaly_users_on_duty_idx
    ON nxs_anomaly_users (on_duty)
    WHERE on_duty = true;

CREATE INDEX IF NOT EXISTS nxs_anomaly_alert_groups_epic_pending_idx
    ON nxs_anomaly_alert_groups (alert_count, last_received_at)
    WHERE status <> 'resolved' AND epic_sent_at IS NULL;
