-- Shrink stored ChatOps `status` replies to the shape 1.9.2 writes.
--
-- Until 1.9.2 a status reply carried every open alert group whole, logs
-- included, and was stored with its chat message: 10 MB per call on an
-- installation with 7,000 open groups. The reply now lists at most 50 groups as
-- id/title/severity/status next to the exact open_count. Rows written before
-- are rewritten to that shape; the count they reported is kept. Only replies
-- whose listed groups still carry logs are touched, so a second run changes
-- nothing.

UPDATE nxs_anomaly_chatops_messages m
SET data = jsonb_set(
    jsonb_set(
        jsonb_set(m.data, '{response,open_alert_groups}', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                       'id', g->'id', 'title', g->'title',
                       'severity', g->'severity', 'status', g->'status') ORDER BY ord)
            FROM jsonb_array_elements(m.data->'response'->'open_alert_groups') WITH ORDINALITY AS e(g, ord)
            WHERE ord <= 50), '[]'::jsonb)),
        '{response,open_count}', to_jsonb(jsonb_array_length(m.data->'response'->'open_alert_groups'))),
    '{response,truncated}', to_jsonb(jsonb_array_length(m.data->'response'->'open_alert_groups') > 50))
WHERE jsonb_typeof(m.data->'response'->'open_alert_groups') = 'array'
  AND EXISTS (
      SELECT 1 FROM jsonb_array_elements(m.data->'response'->'open_alert_groups') g
      WHERE g ? 'logs');
