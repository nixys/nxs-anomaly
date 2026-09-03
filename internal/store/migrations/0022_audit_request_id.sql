-- Correlation id on audit events.
--
-- Every request already carries an X-Request-ID (generated when the caller does
-- not supply one) and it appears in the access log and in error responses.
-- Without it on the audit row, "what else happened in the request that changed
-- this integration" can only be answered by matching timestamps, which stops
-- working exactly when it matters: under load, or when one request produces
-- several events.

ALTER TABLE nxs_anomaly_audit_events ADD COLUMN IF NOT EXISTS request_id TEXT NOT NULL DEFAULT '';

-- The lookup this enables is "everything from that one request", so the index
-- covers only rows that have one.
CREATE INDEX IF NOT EXISTS nxs_anomaly_audit_events_request_idx
    ON nxs_anomaly_audit_events (request_id)
    WHERE request_id <> '';
