-- Index the unresolved alert groups by escalation chain.
--
-- A paired phone asks, on every event-stream tick, which open groups concern
-- its person. That used to read every unresolved group in the installation —
-- on a sandbox with 89,000 of them, five seconds and half a gigabyte of API
-- memory per tick, for one phone. The question is now asked by the chains that
-- name the person (and the groups they were notified about), which needs this
-- index to stay a lookup. Partial, like the other unresolved-group indexes:
-- resolved groups are the bulk of the table and never asked about here.

CREATE INDEX IF NOT EXISTS nxs_anomaly_alert_groups_chain_unresolved_idx
    ON nxs_anomaly_alert_groups (escalation_chain_id)
    WHERE status <> 'resolved';
