// Package model contains typed domain models over the raw jsonb item maps.
//
// AlertGroup owns the group status state machine and exposes a compile-checked
// API instead of stringly-typed field writes. Step B of the typed-model track:
// the internal representation is now a struct of typed fields (agData) rather
// than a raw map. Stable, always-present fields are promoted to typed struct
// fields; nullable/optional/shape-dependent fields (integration_id, the *_at
// timestamps, escalation_chain_id, notification_channels, emergency_alert,
// silenced_*, direct-paging extras and any unknown keys) live in Extra. On
// save, MarshalData rebuilds the map from these fields and merges Extra, which
// keeps the JSON byte-identical to the loaded row so the store's snapshot-diff
// only rewrites groups a transition actually changed.
//
// The struct is wrapped behind a pointer (AlertGroup{d *agData}) so the value
// type keeps its in-place aliasing semantics: copies of an AlertGroup share the
// same agData, exactly as the previous raw-map wrapper shared the map. Engine
// helper signatures (g model.AlertGroup) are therefore unchanged.
package model

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func init() {
	store.RegisterRecordWrapper("alert_groups", func(m map[string]any) store.Record {
		return WrapAlertGroup(m)
	})
}

// Alert group statuses.
const (
	StatusOpen         = "open"
	StatusAcknowledged = "acknowledged"
	StatusResolved     = "resolved"
	StatusSilenced     = "silenced"
)

// maxGroupLogs caps the embedded log entries per group; older entries are dropped.
const maxGroupLogs = 200

// Transition guard errors. Texts are part of the API surface (returned to
// clients as validation errors), keep them stable.
var (
	ErrAcknowledgeResolved   = errors.New("resolved alert group cannot be acknowledged")
	ErrUnresolveNotResolved  = errors.New("only resolved alert groups can be unresolved")
	ErrUnacknowledgeNotAcked = errors.New("only acknowledged alert groups can be unacknowledged")
	ErrSilenceResolved       = errors.New("resolved alert group cannot be silenced")
)

// agData is the typed backing for an alert group. The promoted fields are the
// columns present and non-null in every group shape (canonical, direct-paging,
// silenced); Extra carries the rest verbatim so nil/absent distinctions and
// unmodeled vendor keys round-trip byte-for-byte.
type agData struct {
	ID             string
	RouteID        string
	DedupeKey      string
	Status         string
	Title          string
	Severity       string
	CreatedAt      string
	UpdatedAt      string
	LastReceivedAt string
	AlertCount     int
	CurrentStep    int
	RepeatCount    int
	Labels         map[string]any
	AlertIDs       []any
	Logs           []any
	// Extra holds nullable/optional/shape-dependent keys: integration_id,
	// escalation_chain_id, notification_channels, emergency_alert, epic_sent_at,
	// next_run_at, acknowledged_at, resolved_at, integration_type,
	// integration_name, description, silenced_at, silenced_until, and any
	// unknown keys. It is the verbatim remainder of the source map.
	Extra map[string]any
}

// newAGData decodes a raw group map into typed fields + Extra. Promoted keys go
// to struct fields; everything else is preserved in Extra exactly as decoded.
func newAGData(m map[string]any) *agData {
	d := &agData{Extra: make(map[string]any, len(m))}
	for k, v := range m {
		switch k {
		case "id":
			d.ID = asString(v)
		case "route_id":
			d.RouteID = asString(v)
		case "dedupe_key":
			d.DedupeKey = asString(v)
		case "status":
			d.Status = asString(v)
		case "title":
			d.Title = asString(v)
		case "severity":
			d.Severity = asString(v)
		case "created_at":
			d.CreatedAt = asString(v)
		case "updated_at":
			d.UpdatedAt = asString(v)
		case "last_received_at":
			d.LastReceivedAt = asString(v)
		case "alert_count":
			d.AlertCount = asInt(v)
		case "current_step":
			d.CurrentStep = asInt(v)
		case "repeat_count":
			d.RepeatCount = asInt(v)
		case "labels":
			d.Labels, _ = v.(map[string]any)
		case "alert_ids":
			d.AlertIDs, _ = v.([]any)
		case "logs":
			d.Logs, _ = v.([]any)
		default:
			d.Extra[k] = v
		}
	}
	return d
}

// toMap rebuilds the raw map representation: promoted fields first, then Extra
// merged on top. The promoted keys are emitted unconditionally because they are
// present in every generated group shape; Extra reproduces the remaining keys
// (including present-but-nil ones) so json.Marshal yields the loaded row's
// bytes when nothing changed.
func (d *agData) toMap() map[string]any {
	m := make(map[string]any, len(d.Extra)+15)
	m["id"] = d.ID
	m["route_id"] = d.RouteID
	m["dedupe_key"] = d.DedupeKey
	m["status"] = d.Status
	m["title"] = d.Title
	m["severity"] = d.Severity
	m["created_at"] = d.CreatedAt
	m["updated_at"] = d.UpdatedAt
	m["last_received_at"] = d.LastReceivedAt
	m["alert_count"] = d.AlertCount
	m["current_step"] = d.CurrentStep
	m["repeat_count"] = d.RepeatCount
	m["labels"] = d.Labels
	m["alert_ids"] = d.AlertIDs
	m["logs"] = d.Logs
	for k, v := range d.Extra {
		m[k] = v
	}
	return m
}

// asString coerces a decoded JSON value to string (promoted string columns are
// always strings in every generated shape; a non-string yields "").
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// asInt coerces a decoded JSON number (int when freshly created, float64 when
// loaded from jsonb) to int. Integer-valued int and float64 marshal identically,
// so the count columns stay byte-stable across the load round-trip.
func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// AlertGroup is a typed view over an alert group. The backing agData is shared
// through a pointer so copies of an AlertGroup alias the same group state.
type AlertGroup struct {
	d *agData
}

// AlertGroup is held in State as a store.Record (typed collection).
var _ store.Record = AlertGroup{}

// WrapAlertGroup wraps an existing raw group map into typed fields. The map must
// not be nil.
func WrapAlertGroup(raw map[string]any) AlertGroup {
	return AlertGroup{d: newAGData(raw)}
}

// NewAlertGroupParams holds the inputs for creating an alert group from an
// ingested alert.
type NewAlertGroupParams struct {
	IntegrationID        string
	RouteID              string
	EscalationChainID    string
	DedupeKey            string
	Title                string
	Severity             string
	Labels               map[string]any
	NotificationChannels []string
	EmergencyAlert       bool
	Timestamp            string
	// TraceParent is the W3C traceparent of the ingesting request, kept so the
	// delivery this group eventually causes can be linked back to it. Empty when
	// tracing is off.
	TraceParent string
}

// NewAlertGroup constructs an open alert group in its canonical shape, due for
// immediate escalation (next_run_at = Timestamp). The first alert is attached
// by the caller (alert_ids/alert_count start empty).
func NewAlertGroup(p NewAlertGroupParams) AlertGroup {
	labels := p.Labels
	if labels == nil {
		labels = map[string]any{}
	}
	channels := make([]any, len(p.NotificationChannels))
	for i, ch := range p.NotificationChannels {
		channels[i] = ch
	}
	g := AlertGroup{d: &agData{
		ID:             utils.MakeID("grp"),
		RouteID:        p.RouteID,
		DedupeKey:      p.DedupeKey,
		Status:         StatusOpen,
		Title:          p.Title,
		Severity:       p.Severity,
		CreatedAt:      p.Timestamp,
		UpdatedAt:      p.Timestamp,
		LastReceivedAt: p.Timestamp,
		AlertCount:     0,
		CurrentStep:    0,
		RepeatCount:    0,
		Labels:         labels,
		AlertIDs:       []any{},
		Logs:           []any{},
		Extra: map[string]any{
			"integration_id":        p.IntegrationID,
			"escalation_chain_id":   p.EscalationChainID,
			"notification_channels": channels,
			"emergency_alert":       p.EmergencyAlert,
			"epic_sent_at":          nil,
			"next_run_at":           p.Timestamp,
			"acknowledged_at":       nil,
			"resolved_at":           nil,
			// One group can be lived through more than once: it is reopened by
			// a new alert after an acknowledgement, or reopened by an operator
			// after a resolve. Each of those is a separate response with its own
			// time-to-acknowledge and time-to-resolve, and averaging them
			// together under one group id makes a group that flapped four times
			// look like one slow incident. The episode is that unit.
			"episode_id": utils.MakeID("epd"),
		},
	}}
	// Only set when there is one. Tracing is off by default, and an
	// unconditional "trace_parent": "" would change the canonical shape of every
	// group ever created — including the stored JSON of deployments that never
	// enable tracing.
	if p.TraceParent != "" {
		g.d.Extra["trace_parent"] = p.TraceParent
	}
	return g
}

// Raw returns a freshly rebuilt map view of the group. Unlike the previous
// raw-backed wrapper this is a copy, not the live backing store — mutate the
// group through its methods, not the returned map.
func (g AlertGroup) Raw() map[string]any { return g.d.toMap() }

// store.Record implementation: AlertGroup is held in State as a typed Record.
// MarshalData rebuilds the map from typed fields + Extra; for an unchanged row
// the bytes equal the loaded row, so snapshot-diff skips it.

func (g AlertGroup) RecordID() string             { return g.ID() }
func (g AlertGroup) MarshalData() ([]byte, error) { return json.Marshal(g.d.toMap()) }

// MarshalJSON makes the wrapper transparent to encoding/json (the rebuilt map
// surfaces directly), so callers that json.Marshal a State — like the
// print-state CLI — see the row's data instead of an empty struct.
func (g AlertGroup) MarshalJSON() ([]byte, error) { return g.MarshalData() }

// TypedValues mirrors the store's typedValues("alert_groups", ...) exactly:
// integration_id, route_id, escalation_chain_id, dedupe_key, status, severity,
// next_run_at, last_received_at, acknowledged_at, resolved_at, alert_count,
// epic_sent_at. Empty/nil string columns become NULL; alert_count passes through.
func (g AlertGroup) TypedValues() []any {
	ev := func(k string) any {
		v, ok := g.d.Extra[k]
		if !ok || v == nil || v == "" {
			return nil
		}
		return fmt.Sprintf("%v", v)
	}
	nz := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	return []any{
		ev("integration_id"), nz(g.d.RouteID), ev("escalation_chain_id"), nz(g.d.DedupeKey),
		nz(g.d.Status), nz(g.d.Severity), ev("next_run_at"), nz(g.d.LastReceivedAt),
		ev("acknowledged_at"), ev("resolved_at"), g.d.AlertCount, ev("epic_sent_at"),
	}
}

func (g AlertGroup) ID() string     { return g.d.ID }
func (g AlertGroup) Status() string { return g.d.Status }

func (g AlertGroup) IsResolved() bool     { return g.Status() == StatusResolved }
func (g AlertGroup) IsAcknowledged() bool { return g.Status() == StatusAcknowledged }

// Typed field accessors. Promoted fields read the struct; nullable/optional
// fields read Extra. Either way callers get a compile-checked, stringly-key-free
// API in place of utils.StrVal(group, "field") / group["field"].
func (g AlertGroup) IntegrationID() string     { return utils.StrVal(g.d.Extra, "integration_id") }
func (g AlertGroup) RouteID() string           { return g.d.RouteID }
func (g AlertGroup) EscalationChainID() string { return utils.StrVal(g.d.Extra, "escalation_chain_id") }
func (g AlertGroup) DedupeKey() string         { return g.d.DedupeKey }
func (g AlertGroup) Title() string             { return g.d.Title }
func (g AlertGroup) Severity() string          { return g.d.Severity }
func (g AlertGroup) NextRunAt() string         { return utils.StrVal(g.d.Extra, "next_run_at") }

// TraceParent is the W3C traceparent of the request that first created this
// group, stored so the delivery a worker performs minutes later can be linked
// back to the ingest that caused it — the two are joined by this row, not by a
// call. Empty when tracing was off at ingest, which is the default. Lives in
// Extra rather than getting a column: nothing queries or indexes it.
func (g AlertGroup) TraceParent() string    { return utils.StrVal(g.d.Extra, "trace_parent") }
func (g AlertGroup) LastReceivedAt() string { return g.d.LastReceivedAt }
func (g AlertGroup) CreatedAt() string      { return g.d.CreatedAt }
func (g AlertGroup) AcknowledgedAt() string { return utils.StrVal(g.d.Extra, "acknowledged_at") }
func (g AlertGroup) ResolvedAt() string     { return utils.StrVal(g.d.Extra, "resolved_at") }
func (g AlertGroup) EpicSentAt() string     { return utils.StrVal(g.d.Extra, "epic_sent_at") }
func (g AlertGroup) CurrentStep() int       { return g.d.CurrentStep }
func (g AlertGroup) AlertCount() int        { return g.d.AlertCount }
func (g AlertGroup) RepeatCount() int       { return g.d.RepeatCount }
func (g AlertGroup) EmergencyAlert() bool   { return utils.BoolVal(g.d.Extra, "emergency_alert", false) }
func (g AlertGroup) AlertIDs() []string     { return strSlice(g.d.AlertIDs) }
func (g AlertGroup) NotificationChannels() []string {
	return strSlice(g.d.Extra["notification_channels"])
}

// NotifiedUserIDs are the people this group actually woke, in the order they
// were first notified.
//
// It is kept on the group rather than derived from the notifications table so
// that "tell whoever was paged that this is over" costs no extra load on any
// path — including ingest, which resolves a group without ever knowing its id
// until it is already inside the mutator. The group's log records usernames for
// people to read; this records ids for the code to use.
func (g AlertGroup) NotifiedUserIDs() []string { return strSlice(g.d.Extra["notified_user_ids"]) }

// AddNotifiedUser records that userID was paged about this group, ignoring
// repeats so a re-escalation to the same person does not grow the list.
func (g AlertGroup) AddNotifiedUser(userID string) {
	if userID == "" {
		return
	}
	existing := g.NotifiedUserIDs()
	for _, id := range existing {
		if id == userID {
			return
		}
	}
	next := make([]any, 0, len(existing)+1)
	for _, id := range existing {
		next = append(next, id)
	}
	g.d.Extra["notified_user_ids"] = append(next, userID)
}

// ResolveNotifiedAt is when the "this is over" notice went out, empty when it
// has not. Resolve itself is idempotent and allowed from any state, so without
// this a second resolve would page everyone again.
func (g AlertGroup) ResolveNotifiedAt() string { return utils.StrVal(g.d.Extra, "resolve_notified_at") }

// MarkResolveNotified records that the resolution notice has been sent.
func (g AlertGroup) MarkResolveNotified(ts string) { g.d.Extra["resolve_notified_at"] = ts }

// Optional fields not always present (direct-paging shape, silenced state).
func (g AlertGroup) Description() string     { return utils.StrVal(g.d.Extra, "description") }
func (g AlertGroup) IntegrationType() string { return utils.StrVal(g.d.Extra, "integration_type") }
func (g AlertGroup) IntegrationName() string { return utils.StrVal(g.d.Extra, "integration_name") }
func (g AlertGroup) SilencedAt() string      { return utils.StrVal(g.d.Extra, "silenced_at") }
func (g AlertGroup) SilencedUntil() string   { return utils.StrVal(g.d.Extra, "silenced_until") }

// AnalyticsTeamID and AnalyticsTopic are the analytics context: the team the
// group's integration belonged to, and the Kafka topic its events go to.
//
// Snapshotted onto the group when it is created because the lifecycle
// transitions run in mutators that load only alert_groups — reading the
// integration there would be a nested database call inside an advisory lock.
// Neither is set for a group created before this shipped, and both fall back
// safely: an empty team and the deployment's default topic.
func (g AlertGroup) AnalyticsTeamID() string { return utils.StrVal(g.d.Extra, "analytics_team_id") }
func (g AlertGroup) AnalyticsTopic() string  { return utils.StrVal(g.d.Extra, "analytics_topic") }

// SetAnalyticsContext records the pair. Absent values are not written, so a
// deployment without teams or per-integration topics keeps the canonical group
// shape it already had.
func (g AlertGroup) SetAnalyticsContext(teamID, topic string) {
	if teamID != "" {
		g.d.Extra["analytics_team_id"] = teamID
	}
	if topic != "" {
		g.d.Extra["analytics_topic"] = topic
	}
}

// EpisodeID identifies the current pass through the group's lifecycle. See the
// comment in NewAlertGroup for why a group id is not enough.
func (g AlertGroup) EpisodeID() string { return utils.StrVal(g.d.Extra, "episode_id") }

// EnsureEpisodeID assigns an episode to a group created before episodes
// existed, and returns the current one either way.
//
// Lazy rather than backfilled by a migration: the groups that matter are the
// ones still being worked on, and they get an episode in their first
// transition. A group that was resolved before this shipped has no episode and
// never needed one — it will not appear in episode-keyed analytics, which is
// the honest result for a group whose response nobody recorded.
func (g AlertGroup) EnsureEpisodeID() string {
	if id := g.EpisodeID(); id != "" {
		return id
	}
	id := utils.MakeID("epd")
	g.d.Extra["episode_id"] = id
	return id
}

// StartEpisode begins a new pass through the lifecycle and returns its id.
func (g AlertGroup) StartEpisode() string {
	id := utils.MakeID("epd")
	g.d.Extra["episode_id"] = id
	return id
}

// Logs returns the group's embedded log slice, or nil if unset.
func (g AlertGroup) Logs() []any { return g.d.Logs }

// AlertCountRaw returns the alert_count as any for byte-stable JSON forwarding
// (an integer marshals the same whether int or float64).
func (g AlertGroup) AlertCountRaw() any { return g.d.AlertCount }

// ResolvedAtRaw returns the raw resolved_at value (nil or string) so callers
// that forward it into JSON events preserve the original type.
func (g AlertGroup) ResolvedAtRaw() any { return g.d.Extra["resolved_at"] }

// Labels returns the group's label map, or nil if unset/wrong type.
func (g AlertGroup) Labels() map[string]any { return g.d.Labels }

// LabelsRaw returns the labels value (map[string]any or nil), preserving its
// type for JSON forwarding (notification payloads, outbox events, previews).
func (g AlertGroup) LabelsRaw() any { return g.d.Labels }

// Typed field setters. Each writes the exact value the engine wrote before, so
// the store's snapshot-diff byte representation is unchanged.
func (g AlertGroup) SetSeverity(s string)            { g.d.Severity = s }
func (g AlertGroup) SetTitle(s string)               { g.d.Title = s }
func (g AlertGroup) SetUpdatedAt(ts string)          { g.d.UpdatedAt = ts }
func (g AlertGroup) SetLastReceivedAt(ts string)     { g.d.LastReceivedAt = ts }
func (g AlertGroup) SetEpicSentAt(ts string)         { g.d.Extra["epic_sent_at"] = ts }
func (g AlertGroup) SetCurrentStep(n int)            { g.d.CurrentStep = n }
func (g AlertGroup) SetRepeatCount(n int)            { g.d.RepeatCount = n }
func (g AlertGroup) SetLabels(labels map[string]any) { g.d.Labels = labels }
func (g AlertGroup) MarkEmergency()                  { g.d.Extra["emergency_alert"] = true }

// SetNextRunAt schedules the next escalation run; ClearNextRunAt stops escalation.
func (g AlertGroup) SetNextRunAt(iso string) { g.d.Extra["next_run_at"] = iso }
func (g AlertGroup) ClearNextRunAt()         { g.d.Extra["next_run_at"] = nil }

// SetNotificationChannels replaces the channel list (stored as []any to match
// the persisted representation).
func (g AlertGroup) SetNotificationChannels(channels []string) {
	out := make([]any, len(channels))
	for i, c := range channels {
		out[i] = c
	}
	g.d.Extra["notification_channels"] = out
}

// AddAlertID appends an alert id to the group's alert_ids list.
func (g AlertGroup) AddAlertID(id string) {
	g.d.AlertIDs = append(g.d.AlertIDs, id)
}

// IncAlertCount increments alert_count by one.
func (g AlertGroup) IncAlertCount() { g.d.AlertCount++ }

// AppendLog appends a structured log entry attributed to the system actor.
// Escalation steps, delivery outcomes and reopen-on-new-alert genuinely have no
// human behind them, so they use this form; operator actions use AppendLogBy.
func (g AlertGroup) AppendLog(eventType, message string, data map[string]any) {
	g.AppendLogBy(authz.SystemActor, eventType, message, data)
}

// AppendLogBy appends a structured log entry attributed to actor, capped at
// maxGroupLogs.
//
// The cap is why this timeline is not an audit trail: entries fall off the
// front. Durable attribution lives in nxs_anomaly_audit_events; what is
// recorded here is the operator-facing story of one group.
func (g AlertGroup) AppendLogBy(actor authz.Actor, eventType, message string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	entry := map[string]any{
		"id":         utils.MakeID("log"),
		"type":       eventType,
		"message":    message,
		"data":       data,
		"created_at": utils.ToISO(utils.UTCNow()),
		"actor": map[string]any{
			"id":   actor.ID,
			"kind": actor.Kind,
			"name": actor.Describe(),
			"role": actor.Role.String(),
		},
	}
	logs := append(g.d.Logs, entry)
	if len(logs) > maxGroupLogs {
		logs = logs[len(logs)-maxGroupLogs:]
	}
	g.d.Logs = logs
}

// Acknowledge moves the group to acknowledged and stops escalation.
// Acknowledging an already acknowledged group is allowed (refreshes timestamps).
func (g AlertGroup) Acknowledge(ts, message string, actor authz.Actor) error {
	if g.IsResolved() {
		return ErrAcknowledgeResolved
	}
	g.d.Status = StatusAcknowledged
	g.d.Extra["acknowledged_at"] = ts
	g.d.Extra["acknowledged_by"] = actorRef(actor)
	g.d.Extra["next_run_at"] = nil
	g.d.UpdatedAt = ts
	g.AppendLogBy(actor, "acknowledged", message, nil)
	return nil
}

// Resolve moves the group to resolved and stops escalation. It has no status
// guard: resolving is idempotent and allowed from any state (callers that need
// to distinguish already-resolved groups check Status first).
func (g AlertGroup) Resolve(ts, reason string, actor authz.Actor) {
	g.d.Status = StatusResolved
	g.d.Extra["resolved_at"] = ts
	g.d.Extra["resolved_by"] = actorRef(actor)
	g.d.Extra["next_run_at"] = nil
	g.d.UpdatedAt = ts
	g.AppendLogBy(actor, "resolved", reason, nil)
}

// Unresolve reopens a resolved group and restarts escalation from step 0.
func (g AlertGroup) Unresolve(ts string, actor authz.Actor) error {
	if !g.IsResolved() {
		return ErrUnresolveNotResolved
	}
	g.d.Status = StatusOpen
	g.d.Extra["resolved_at"] = nil
	g.d.Extra["resolved_by"] = nil
	g.d.Extra["acknowledged_at"] = nil
	g.d.Extra["acknowledged_by"] = nil
	g.d.CurrentStep = 0
	g.d.Extra["next_run_at"] = ts
	g.d.UpdatedAt = ts
	// A reopened group is a new response, not a continuation of the one that
	// was closed. Keeping the old episode would let this resolve overwrite the
	// previous MTTR with the sum of both.
	g.StartEpisode()
	g.AppendLogBy(actor, "unresolved", "Alert group reopened by operator", nil)
	return nil
}

// Unacknowledge returns an acknowledged group to open and resumes escalation.
func (g AlertGroup) Unacknowledge(ts string, actor authz.Actor) error {
	if !g.IsAcknowledged() {
		return ErrUnacknowledgeNotAcked
	}
	g.d.Status = StatusOpen
	g.d.Extra["acknowledged_at"] = nil
	g.d.Extra["acknowledged_by"] = nil
	g.d.Extra["next_run_at"] = ts
	g.d.UpdatedAt = ts
	g.AppendLogBy(actor, "unacknowledged", "Alert group unacknowledged by operator", nil)
	return nil
}

// ReopenOnNewAlert returns an acknowledged group to open when a new alert
// arrives for it; escalation resumption (next_run_at) is the caller's concern.
// No-op for any other status; reports whether the group was reopened.
func (g AlertGroup) ReopenOnNewAlert() bool {
	if !g.IsAcknowledged() {
		return false
	}
	g.d.Status = StatusOpen
	g.d.Extra["acknowledged_at"] = nil
	// The chain restarts from the top, as it does for Unresolve, because a
	// reopen is a new episode and a new episode is escalated from the
	// beginning. Resuming from the stored position would run whatever step the
	// chain had already reached — for a group parked behind a WAIT, that is the
	// step the WAIT was still counting down to, executed the moment the alert
	// came back.
	g.d.CurrentStep = 0
	g.d.RepeatCount = 0
	// Same reasoning as Unresolve: somebody acknowledged this, it came back,
	// and the acknowledgement of the next pass is a second response time.
	g.StartEpisode()
	g.AppendLog("group_reopened", "New alert reopened an acknowledged group", nil)
	return true
}

// strSlice coerces a raw []any (as decoded from jsonb) into a []string. It
// mirrors the engine's anyToStringSlice: every element is stringified with %v
// and a nil/non-list input yields a nil slice.
func strSlice(raw any) []string {
	list, _ := raw.([]any)
	var out []string
	for _, v := range list {
		out = append(out, fmt.Sprintf("%v", v))
	}
	return out
}

// Silence mutes the group. silencedUntil may be empty for an indefinite
// silence; it is stored as-is to match the existing representation.
func (g AlertGroup) Silence(ts, silencedUntil, message string, durationMinutes int, actor authz.Actor) error {
	if g.IsResolved() {
		return ErrSilenceResolved
	}
	g.d.Status = StatusSilenced
	g.d.Extra["silenced_at"] = ts
	g.d.Extra["silenced_by"] = actorRef(actor)
	g.d.Extra["silenced_until"] = silencedUntil
	g.d.Extra["next_run_at"] = nil
	g.d.UpdatedAt = ts
	g.AppendLogBy(actor, "silenced", message, map[string]any{"duration_minutes": durationMinutes})
	return nil
}

// actorRef is the compact attribution stored alongside a state transition, so
// "who acknowledged this" is answerable from the group itself without joining
// the audit table. Returns nil for the system actor: an unattended transition
// has no one to attribute it to, and a null reads better than a fake record.
func actorRef(actor authz.Actor) any {
	if actor.IsSystem() {
		return nil
	}
	return map[string]any{
		"id":   actor.ID,
		"kind": actor.Kind,
		"name": actor.Describe(),
		"role": actor.Role.String(),
	}
}

// AcknowledgedBy / ResolvedBy / SilencedBy return the stored attribution, or nil
// when the transition was unattended or predates attribution being recorded.
func (g AlertGroup) AcknowledgedBy() any { return g.d.Extra["acknowledged_by"] }
func (g AlertGroup) ResolvedBy() any     { return g.d.Extra["resolved_by"] }
func (g AlertGroup) SilencedBy() any     { return g.d.Extra["silenced_by"] }
