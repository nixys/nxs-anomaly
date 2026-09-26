-- Index the unresolved alert groups by their latest alert.
--
-- A chat's "status" and "alerts" count the open groups the chat may see and
-- list the newest of them. They used to load every unresolved group under the
-- ChatOps command lock and sort in Go: twelve seconds on a sandbox with 89,000
-- open groups, with every other chat command waiting behind it, and Slack gives
-- a slash command three. They now count and page in the database, ordered by
-- this index.

CREATE INDEX IF NOT EXISTS nxs_anomaly_alert_groups_recent_unresolved_idx
    ON nxs_anomaly_alert_groups (last_received_at DESC NULLS LAST, id)
    WHERE status <> 'resolved';
