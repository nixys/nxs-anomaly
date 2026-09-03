ALTER TABLE nxs_anomaly_users
    ADD COLUMN IF NOT EXISTS username text,
    ADD COLUMN IF NOT EXISTS email text;

ALTER TABLE nxs_anomaly_teams
    ADD COLUMN IF NOT EXISTS name text;

ALTER TABLE nxs_anomaly_schedules
    ADD COLUMN IF NOT EXISTS team_id text;

ALTER TABLE nxs_anomaly_integrations
    ADD COLUMN IF NOT EXISTS key text,
    ADD COLUMN IF NOT EXISTS name text,
    ADD COLUMN IF NOT EXISTS type text;

ALTER TABLE nxs_anomaly_chatops_channels
    ADD COLUMN IF NOT EXISTS team_id text,
    ADD COLUMN IF NOT EXISTS user_id text,
    ADD COLUMN IF NOT EXISTS notifications_enabled boolean;

ALTER TABLE nxs_anomaly_mobile_devices
    ADD COLUMN IF NOT EXISTS user_id text,
    ADD COLUMN IF NOT EXISTS platform text,
    ADD COLUMN IF NOT EXISTS active boolean;

ALTER TABLE nxs_anomaly_mobile_sessions
    ADD COLUMN IF NOT EXISTS token text,
    ADD COLUMN IF NOT EXISTS user_id text,
    ADD COLUMN IF NOT EXISTS device_id text,
    ADD COLUMN IF NOT EXISTS revoked_at timestamptz;

ALTER TABLE nxs_anomaly_alerts
    ADD COLUMN IF NOT EXISTS integration_id text,
    ADD COLUMN IF NOT EXISTS route_id text,
    ADD COLUMN IF NOT EXISTS status text,
    ADD COLUMN IF NOT EXISTS severity text,
    ADD COLUMN IF NOT EXISTS received_at timestamptz;

ALTER TABLE nxs_anomaly_alert_groups
    ADD COLUMN IF NOT EXISTS integration_id text,
    ADD COLUMN IF NOT EXISTS route_id text,
    ADD COLUMN IF NOT EXISTS escalation_chain_id text,
    ADD COLUMN IF NOT EXISTS dedupe_key text,
    ADD COLUMN IF NOT EXISTS status text,
    ADD COLUMN IF NOT EXISTS severity text,
    ADD COLUMN IF NOT EXISTS next_run_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_received_at timestamptz,
    ADD COLUMN IF NOT EXISTS acknowledged_at timestamptz,
    ADD COLUMN IF NOT EXISTS resolved_at timestamptz;

ALTER TABLE nxs_anomaly_notifications
    ADD COLUMN IF NOT EXISTS alert_group_id text,
    ADD COLUMN IF NOT EXISTS user_id text,
    ADD COLUMN IF NOT EXISTS channel text,
    ADD COLUMN IF NOT EXISTS status text;

UPDATE nxs_anomaly_users
SET username = data->>'username',
    email = data->>'email'
WHERE username IS NULL OR email IS NULL;

UPDATE nxs_anomaly_teams
SET name = data->>'name'
WHERE name IS NULL;

UPDATE nxs_anomaly_schedules
SET team_id = data->>'team_id'
WHERE team_id IS NULL;

UPDATE nxs_anomaly_integrations
SET key = data->>'key',
    name = data->>'name',
    type = data->>'type'
WHERE key IS NULL OR name IS NULL OR type IS NULL;

UPDATE nxs_anomaly_chatops_channels
SET team_id = data->>'team_id',
    user_id = data->>'user_id',
    notifications_enabled = COALESCE((data->>'notifications_enabled')::boolean, true)
WHERE notifications_enabled IS NULL;

UPDATE nxs_anomaly_mobile_devices
SET user_id = data->>'user_id',
    platform = data->>'platform',
    active = COALESCE((data->>'active')::boolean, true)
WHERE user_id IS NULL OR platform IS NULL OR active IS NULL;

UPDATE nxs_anomaly_mobile_sessions
SET token = data->>'token',
    user_id = data->>'user_id',
    device_id = data->>'device_id',
    revoked_at = NULLIF(data->>'revoked_at', '')::timestamptz
WHERE token IS NULL OR user_id IS NULL OR device_id IS NULL;

UPDATE nxs_anomaly_alerts
SET integration_id = data->>'integration_id',
    route_id = data->>'route_id',
    status = data->>'status',
    severity = data->>'severity',
    received_at = NULLIF(data->>'received_at', '')::timestamptz
WHERE integration_id IS NULL OR status IS NULL OR received_at IS NULL;

UPDATE nxs_anomaly_alert_groups
SET integration_id = data->>'integration_id',
    route_id = data->>'route_id',
    escalation_chain_id = data->>'escalation_chain_id',
    dedupe_key = data->>'dedupe_key',
    status = data->>'status',
    severity = data->>'severity',
    next_run_at = NULLIF(data->>'next_run_at', '')::timestamptz,
    last_received_at = NULLIF(data->>'last_received_at', '')::timestamptz,
    acknowledged_at = NULLIF(data->>'acknowledged_at', '')::timestamptz,
    resolved_at = NULLIF(data->>'resolved_at', '')::timestamptz
WHERE integration_id IS NULL OR status IS NULL OR dedupe_key IS NULL;

UPDATE nxs_anomaly_notifications
SET alert_group_id = data->>'alert_group_id',
    user_id = data->>'user_id',
    channel = data->>'channel',
    status = data->>'status'
WHERE alert_group_id IS NULL OR user_id IS NULL OR channel IS NULL OR status IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS nxs_anomaly_integrations_key_unique_idx
    ON nxs_anomaly_integrations (key);

CREATE INDEX IF NOT EXISTS nxs_anomaly_alert_groups_active_lookup_idx
    ON nxs_anomaly_alert_groups (integration_id, dedupe_key)
    WHERE status <> 'resolved';

CREATE INDEX IF NOT EXISTS nxs_anomaly_alert_groups_due_idx
    ON nxs_anomaly_alert_groups (next_run_at)
    WHERE status = 'open' AND next_run_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS nxs_anomaly_alert_groups_status_typed_idx
    ON nxs_anomaly_alert_groups (status);

CREATE UNIQUE INDEX IF NOT EXISTS nxs_anomaly_mobile_sessions_token_unique_idx
    ON nxs_anomaly_mobile_sessions (token);

CREATE INDEX IF NOT EXISTS nxs_anomaly_mobile_sessions_user_idx
    ON nxs_anomaly_mobile_sessions (user_id);

CREATE INDEX IF NOT EXISTS nxs_anomaly_mobile_devices_user_idx
    ON nxs_anomaly_mobile_devices (user_id)
    WHERE active IS TRUE;

CREATE INDEX IF NOT EXISTS nxs_anomaly_alerts_integration_received_idx
    ON nxs_anomaly_alerts (integration_id, received_at DESC);

CREATE INDEX IF NOT EXISTS nxs_anomaly_notifications_group_user_idx
    ON nxs_anomaly_notifications (alert_group_id, user_id);
