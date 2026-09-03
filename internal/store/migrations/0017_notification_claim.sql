-- Claim-then-deliver (lease) support for multi-worker delivery.
--
-- A worker atomically moves a notification to the transient status 'delivering'
-- or 'retrying' (recording data->>'claimed_at' / data->>'claimed_by') before the
-- unlocked provider call, so each row is delivered by exactly one worker. A
-- reaper resets claims whose worker died (claimed_at older than the claim TTL).
--
-- This partial index keeps the claimed set (always small: bounded by
-- workers x batch limit) cheap to scan for both the reaper and status filters,
-- without indexing the whole notifications table.
CREATE INDEX IF NOT EXISTS nxs_anomaly_notifications_claimed_idx
    ON nxs_anomaly_notifications (status)
    WHERE status IN ('delivering', 'retrying');
