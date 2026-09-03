-- Trace id on audit events, alongside the request id added by 0022.
--
-- request_id ties together everything one request did inside this service.
-- trace_id ties that request to the causal chain it belongs to, which for an
-- alerting system usually starts outside: Alertmanager posts a webhook, the
-- ingest handler groups it, a worker cycle minutes later escalates it, and an
-- adapter finally calls a provider. Those are separate processes and separate
-- requests — a request id cannot join them, and a timestamp only appears to.
--
-- Storing it on the audit row is what makes the join survive the trace itself.
-- Spans expire out of the collector in days or weeks; an audit event is kept for
-- as long as the retention policy says. Reading a row six weeks later and finding
-- the trace id is how "who acknowledged this, and what was the system doing at
-- the time" stays answerable after the spans are gone.
--
-- Empty when tracing is disabled, which is the default. The column is therefore
-- NOT NULL DEFAULT '' rather than nullable: "no trace" and "tracing was off" are
-- the same fact here, and a nullable column would invite three-valued logic in
-- every query for no gain.

ALTER TABLE nxs_anomaly_audit_events ADD COLUMN IF NOT EXISTS trace_id TEXT NOT NULL DEFAULT '';

-- The lookup is "every audit event in this trace", so like the request_id index
-- this one covers only rows that have a trace to look up.
CREATE INDEX IF NOT EXISTS nxs_anomaly_audit_events_trace_idx
    ON nxs_anomaly_audit_events (trace_id)
    WHERE trace_id <> '';
