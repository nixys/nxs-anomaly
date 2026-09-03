package engine

import (
	"context"
	"log/slog"
	"sort"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/tracing"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Audit action names. They are coarse on purpose — one per operation the API
// exposes — so the trail stays greppable and stable across refactors.
const (
	AuditAcknowledge   = "alert_group.acknowledge"
	AuditUnacknowledge = "alert_group.unacknowledge"
	AuditResolve       = "alert_group.resolve"
	AuditUnresolve     = "alert_group.unresolve"
	AuditSilence       = "alert_group.silence"
	AuditCreate        = "create"
	AuditUpdate        = "update"
	AuditDelete        = "delete"
)

// audit appends one event to the append-only trail.
//
// Failures are logged and swallowed rather than propagated: the operation the
// event describes has already been committed, so returning an error here would
// report a failure that did not happen and, worse, invite the caller to retry a
// state transition that already took effect. A dropped audit record is visible
// in the service log, which is the lesser of the two evils.
//
// The actor comes from the context, which the HTTP layer populates. Unattended
// callers (the worker) carry no actor and are recorded as the system actor.
func (e *Engine) audit(ctx context.Context, action, entityType, entityID string, data map[string]any) {
	if err := e.store.InsertAuditEvent(ctx, e.buildAuditEvent(ctx, action, entityType, entityID, data)); err != nil {
		slog.Error("audit_write_failed",
			"action", action, "entity_type", entityType, "entity_id", entityID, "error", err)
	}
}

// buildAuditEvent assembles the event from the context's actor and request tags.
func (e *Engine) buildAuditEvent(ctx context.Context, action, entityType, entityID string, data map[string]any) store.AuditEvent {
	actor := authz.FromContext(ctx)
	return store.AuditEvent{
		ID:         utils.MakeID("aud"),
		OccurredAt: utils.ToISO(utils.UTCNow()),
		ActorID:    actor.ID,
		ActorKind:  actor.Kind,
		ActorName:  actor.Describe(),
		ActorRole:  actor.Role.String(),
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		RequestIP:  RequestIPFromContext(ctx),
		RequestID:  RequestIDFromContext(ctx),
		TraceID:    tracing.TraceID(ctx),
		Data:       data,
	}
}

// auditIn enrolls an audit event to be written in the same transaction as the
// state change. Call it from inside an UpdateCollections mutator; the store
// flushes state.AuditEvents after the mutator succeeds, so the record is atomic
// with the operation it describes. This is the transactional counterpart of
// audit (which writes a separate statement, used only where there is no
// enclosing mutation transaction — e.g. the auth endpoints).
func (e *Engine) auditIn(state *store.State, ctx context.Context, action, entityType, entityID string, data map[string]any) {
	state.AuditEvents = append(state.AuditEvents, e.buildAuditEvent(ctx, action, entityType, entityID, data))
}

// AuditEvent records an event on behalf of a caller outside the engine — the
// HTTP layer's authentication endpoints, which own state (sessions,
// credentials) the engine knows nothing about but whose changes belong in the
// same trail as everything else.
func (e *Engine) AuditEvent(ctx context.Context, action, entityType, entityID string, data map[string]any) {
	e.audit(ctx, action, entityType, entityID, data)
}

// ListAuditEvents returns the audit trail newest-first. Reading the trail is an
// administrative operation; the HTTP layer gates it.
func (e *Engine) ListAuditEvents(ctx context.Context, filters map[string]any, limit, offset int) (map[string]any, error) {
	items, total, err := e.store.ListAuditEvents(ctx, filters, limit, offset)
	if err != nil {
		return nil, err
	}
	return pageEnvelope(items, total, limit, offset), nil
}

// auditFields summarises a mutation payload for the trail: the *names* of the
// fields the caller supplied, plus the entity's name when it has one.
//
// Values are deliberately excluded. Configuration payloads carry webhook
// secrets, integration keys and provider tokens, and an audit trail that
// copies them turns a security control into a second place secrets live.
// Field names answer "who changed what" without that risk; the current value
// is always one GET away for anyone allowed to see it.
func auditFields(payload map[string]any, extra map[string]any) map[string]any {
	fields := make([]string, 0, len(payload))
	for k := range payload {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	data := map[string]any{"fields": fields}
	if name := utils.StrVal(payload, "name"); name != "" {
		data["name"] = name
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

type requestIPKey struct{}

// NewRequestIPContext tags ctx with the client IP, recorded alongside each audit
// event. It is separate from the actor because it describes the request, not the
// principal: the same principal can act from different addresses.
func NewRequestIPContext(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, requestIPKey{}, ip)
}

// RequestIPFromContext returns the client IP tagged onto ctx, or "".
func RequestIPFromContext(ctx context.Context) string {
	ip, _ := ctx.Value(requestIPKey{}).(string)
	return ip
}

type requestIDKey struct{}

// NewRequestIDContext tags ctx with the request's correlation id, so every
// audit event a single request produces can be found together — and matched
// against the access log line and the error response that carry the same id.
func NewRequestIDContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext returns the correlation id tagged onto ctx, or "". The
// worker has no request behind it, so its events legitimately carry none.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// auditBulkIn enrolls one event per group a bulk operation actually changed,
// into the mutator's state so the operation and its audit records commit
// together. Recording a single event listing the ids would make "what happened
// to this group" unanswerable by an entity_id lookup, which is the query the
// trail exists to serve. Call it from inside the mutator with the ids it
// actually changed.
func (e *Engine) auditBulkIn(state *store.State, ctx context.Context, action string, ids []string) {
	for _, id := range ids {
		e.auditIn(state, ctx, action, "alert_group", id, map[string]any{"bulk": true})
	}
}
