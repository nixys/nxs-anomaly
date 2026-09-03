-- Indexes for the retention sweep.
--
-- Each sweep asks the same shape of question — "the oldest N rows older than a
-- cutoff" — every worker cycle, forever. Without these it is a sequential scan
-- of the largest tables in the schema, once every few seconds, on an
-- installation where those tables are large precisely because retention was just
-- turned on.
--
-- The notifications index is partial: only terminal rows are ever eligible for
-- deletion (a notification still in flight is work, not history), and the
-- in-flight rows are the ones the hot delivery path writes.

CREATE INDEX IF NOT EXISTS nxs_anomaly_notifications_retention_idx
    ON nxs_anomaly_notifications (updated_at)
    WHERE status IN ('delivered', 'failed', 'skipped');

CREATE INDEX IF NOT EXISTS nxs_anomaly_delivery_attempts_retention_idx
    ON nxs_anomaly_notification_delivery_attempts (created_at);

CREATE INDEX IF NOT EXISTS nxs_anomaly_web_sessions_retention_idx
    ON nxs_anomaly_web_sessions (created_at);

-- Audit retention and the erasure pseudonymisation both filter on actor_id;
-- the existing indexes cover occurred_at and the entity, not the actor.
CREATE INDEX IF NOT EXISTS nxs_anomaly_audit_events_actor_idx
    ON nxs_anomaly_audit_events (actor_id);
