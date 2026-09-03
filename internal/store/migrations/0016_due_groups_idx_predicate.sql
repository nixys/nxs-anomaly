-- Align the due-groups partial index with the worker query.
-- ListDueAlertGroups filters status NOT IN ('resolved','silenced'), but the
-- 0002 index only covered status = 'open', so the planner could not use it
-- for due acknowledged groups and fell back to scanning.
DROP INDEX IF EXISTS nxs_anomaly_alert_groups_due_idx;

CREATE INDEX IF NOT EXISTS nxs_anomaly_alert_groups_due_idx
    ON nxs_anomaly_alert_groups (next_run_at)
    WHERE status NOT IN ('resolved', 'silenced') AND next_run_at IS NOT NULL;
