-- Immutable audit trail.
--
-- The alert group timeline (data->'logs') cannot serve as an audit log: it is
-- capped at maxGroupLogs, rewritten whenever the group is written, and it only
-- covers alert groups. This table is append-only and covers every mutating
-- operation, so "who acknowledged this / who changed that integration" has a
-- durable answer.
--
-- Append-only is enforced by a trigger rather than by grants, because the
-- service owns its schema and connects as the table owner. Pruning therefore
-- requires deliberately disabling the trigger, which is itself an act an
-- administrator has to take on purpose.

CREATE TABLE IF NOT EXISTS nxs_anomaly_audit_events (
    id           TEXT        PRIMARY KEY,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_id     TEXT        NOT NULL DEFAULT '',
    actor_kind   TEXT        NOT NULL,
    actor_name   TEXT        NOT NULL DEFAULT '',
    actor_role   TEXT        NOT NULL DEFAULT '',
    action       TEXT        NOT NULL,
    entity_type  TEXT        NOT NULL,
    entity_id    TEXT        NOT NULL DEFAULT '',
    request_ip   TEXT        NOT NULL DEFAULT '',
    data         JSONB       NOT NULL DEFAULT '{}'::jsonb
);

-- The audit view is almost always "recent events", optionally narrowed to one
-- entity or one actor, so order by time and support both narrowings.
CREATE INDEX IF NOT EXISTS nxs_anomaly_audit_events_occurred_at_idx
    ON nxs_anomaly_audit_events (occurred_at DESC);
CREATE INDEX IF NOT EXISTS nxs_anomaly_audit_events_entity_idx
    ON nxs_anomaly_audit_events (entity_type, entity_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS nxs_anomaly_audit_events_actor_idx
    ON nxs_anomaly_audit_events (actor_id, occurred_at DESC);

CREATE OR REPLACE FUNCTION nxs_anomaly_audit_events_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'nxs_anomaly_audit_events is append-only (attempted %)', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS nxs_anomaly_audit_events_append_only ON nxs_anomaly_audit_events;
CREATE TRIGGER nxs_anomaly_audit_events_append_only
    BEFORE UPDATE OR DELETE ON nxs_anomaly_audit_events
    FOR EACH ROW EXECUTE FUNCTION nxs_anomaly_audit_events_append_only();
