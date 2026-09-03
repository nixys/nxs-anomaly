-- Index for process_notification_deliveries hot path:
-- list all notifications with status='delivery_scheduled' ordered by created_at.
-- After the A1 fix (HTTP outside advisory lock), this query runs on every worker cycle.
CREATE INDEX IF NOT EXISTS nxs_anomaly_notifications_delivery_scheduled_idx
    ON nxs_anomaly_notifications (created_at, id)
    WHERE status = 'delivery_scheduled';
