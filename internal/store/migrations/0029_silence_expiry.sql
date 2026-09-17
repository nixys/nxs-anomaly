-- Time-limited silences now end on their own.
--
-- A silence with silenced_until is due at that moment: the worker picks the
-- group up by next_run_at and returns it to open. Groups silenced before this
-- change were stored with next_run_at = NULL and would stay silent forever, so
-- they are made due at their silenced_until. An already expired silence becomes
-- due immediately; an indefinite one (no silenced_until) is left alone.

UPDATE nxs_anomaly_alert_groups
SET data = jsonb_set(data, '{next_run_at}', to_jsonb(data->>'silenced_until')),
    next_run_at = (data->>'silenced_until')::timestamptz
WHERE status = 'silenced'
  AND COALESCE(data->>'silenced_until', '') <> '';
