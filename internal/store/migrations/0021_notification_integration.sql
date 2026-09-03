-- Integration ownership on notifications, so delivery records can be team-scoped.
--
-- This is the one place where denormalising is right rather than merely
-- convenient. Copying team_id onto a notification would go stale as soon as an
-- integration were reassigned; copying integration_id cannot, because an alert
-- group never moves between integrations. Scoping then follows the same path as
-- alerts and alert groups: integration → team.
--
-- Without it, notifications and delivery attempts were readable by any
-- authenticated user regardless of team, which included the notification
-- message text.

ALTER TABLE nxs_anomaly_notifications ADD COLUMN IF NOT EXISTS integration_id TEXT;

-- Backfill from the alert group each notification belongs to. Rows whose group
-- has already been archived stay NULL and are therefore visible to everyone —
-- the same rule unassigned objects follow elsewhere, and the alternative
-- (hiding them from everyone) would silently drop history.
UPDATE nxs_anomaly_notifications n
   SET integration_id = g.integration_id
  FROM nxs_anomaly_alert_groups g
 WHERE n.alert_group_id = g.id
   AND n.integration_id IS NULL;

-- Scoped listing filters on this column together with the status/created_at
-- ordering the notifications page already uses.
CREATE INDEX IF NOT EXISTS nxs_anomaly_notifications_integration_idx
    ON nxs_anomaly_notifications (integration_id)
    WHERE integration_id IS NOT NULL;
