-- nxs:no-transaction
-- Drop the expression index on data->>'next_run_at' of alert groups: replaced by
-- the typed due index (0002, 0016), never scanned, and the other half of what
-- kept every alert group update from being HOT (see 0037).
DROP INDEX CONCURRENTLY IF EXISTS nxs_anomaly_alert_groups_next_run_idx;
