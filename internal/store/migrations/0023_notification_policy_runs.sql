-- Personal notification policies (BETA-031): a per-user notify/wait/fallback
-- sequence run per alert group.
--
-- A user's escalation notification is no longer a single blast of every target
-- at once. When a user has a policy, notifyUsers starts one run row here; the
-- worker advances it over time — fire a channel, wait, fall back to the next
-- channel — and stops as soon as the group is acknowledged or resolved. The run
-- is the durable state machine, so escalation timing survives restarts and is
-- driven off the same worker cycle as everything else.
--
-- Runs are ephemeral bookkeeping, not history: a completed run is marked
-- status='done' and pruned in the archival stage. The delivery record of each
-- step lives in nxs_anomaly_notifications like every other notification.

CREATE TABLE IF NOT EXISTS nxs_anomaly_notification_policy_runs (
    id             text primary key,
    data           jsonb not null,
    alert_group_id text,
    user_id        text,
    status         text,
    next_step_at   timestamptz,
    created_at     timestamptz default now(),
    updated_at     timestamptz default now()
);

-- The worker's due-scan: active runs whose next step is due, oldest first.
CREATE INDEX IF NOT EXISTS nxs_anomaly_npr_due_idx
    ON nxs_anomaly_notification_policy_runs (next_step_at)
    WHERE status = 'active';

-- Dedupe an in-flight run for a (group, user) pair, and prune by group.
CREATE INDEX IF NOT EXISTS nxs_anomaly_npr_group_user_idx
    ON nxs_anomaly_notification_policy_runs (alert_group_id, user_id);
