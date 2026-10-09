package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func init() {
	store.RegisterRecordWrapper("notifications", func(m map[string]any) store.Record {
		return WrapNotification(m)
	})
}

// Notification statuses.
const (
	NotificationDeliveryScheduled = "delivery_scheduled"
	NotificationBatched           = "batched"
	NotificationRetryScheduled    = "retry_scheduled"
	NotificationDelivered         = "delivered"
	NotificationFailed            = "failed"
	// NotificationSkipped is terminal and means the notification was never
	// handed to a provider, because there is no provider to hand it to: the
	// channel is experimental or unconfigured on this deployment. It is
	// deliberately not "delivered" (nothing was) and not "failed" (nothing
	// broke, and retrying cannot help).
	NotificationSkipped = "skipped"
	// Transient claim statuses: a worker atomically moves a row into one of these
	// before it performs the (slow, unlocked) provider call, so no other worker
	// claims the same row. A crashed worker's claim is reset by the reaper.
	NotificationDelivering = "delivering"
	NotificationRetrying   = "retrying"
)

// ntfData is the typed backing for a notification (Step B). Promoted fields are
// present and non-null in every notification shape (all rows come from
// NewNotification); Extra carries the nullable columns (user_id, next_retry_at,
// last_error, batch_id, batch_key, provider_status, payload) and any unknown
// keys verbatim so nil/absent distinctions round-trip byte-for-byte.
type ntfData struct {
	ID             string
	AlertGroupID   string
	Channel        string
	Target         string
	Status         string
	Reason         string
	IdempotencyKey string
	RetryCount     int
	CreatedAt      string
	UpdatedAt      string
	Extra          map[string]any
}

func newNtfData(m map[string]any) *ntfData {
	d := &ntfData{Extra: make(map[string]any, len(m))}
	for k, v := range m {
		switch k {
		case "id":
			d.ID = asString(v)
		case "alert_group_id":
			d.AlertGroupID = asString(v)
		case "channel":
			d.Channel = asString(v)
		case "target":
			d.Target = asString(v)
		case "status":
			d.Status = asString(v)
		case "reason":
			d.Reason = asString(v)
		case "idempotency_key":
			d.IdempotencyKey = asString(v)
		case "retry_count":
			d.RetryCount = asInt(v)
		case "created_at":
			d.CreatedAt = asString(v)
		case "updated_at":
			d.UpdatedAt = asString(v)
		default:
			d.Extra[k] = v
		}
	}
	return d
}

func (d *ntfData) toMap() map[string]any {
	m := make(map[string]any, len(d.Extra)+10)
	m["id"] = d.ID
	m["alert_group_id"] = d.AlertGroupID
	m["channel"] = d.Channel
	m["target"] = d.Target
	m["status"] = d.Status
	m["reason"] = d.Reason
	m["idempotency_key"] = d.IdempotencyKey
	m["retry_count"] = d.RetryCount
	m["created_at"] = d.CreatedAt
	m["updated_at"] = d.UpdatedAt
	for k, v := range d.Extra {
		m[k] = v
	}
	return m
}

// Notification is a typed view over a notification, owning the delivery/retry
// state machine (delivery_scheduled → delivered | retry_scheduled → … → failed).
// Like AlertGroup it is backed by a pointer to typed fields, so copies of a
// Notification alias the same state.
type Notification struct {
	d *ntfData
}

// Notification is held in State as a store.Record (typed collection).
var _ store.Record = Notification{}

// WrapNotification wraps an existing raw notification map into typed fields. The
// map must not be nil.
func WrapNotification(raw map[string]any) Notification {
	return Notification{d: newNtfData(raw)}
}

// store.Record implementation: MarshalData rebuilds the map from typed fields +
// Extra; for an unchanged row the bytes equal the loaded row, so snapshot-diff
// skips it.

func (n Notification) RecordID() string             { return n.ID() }
func (n Notification) MarshalData() ([]byte, error) { return json.Marshal(n.d.toMap()) }

// MarshalJSON surfaces the rebuilt map directly to encoding/json, so print-state
// and similar callers serialize the row's data, not an empty struct.
func (n Notification) MarshalJSON() ([]byte, error) { return n.MarshalData() }

// TypedValues mirrors the store's typedValues("notifications", ...) exactly:
// alert_group_id, user_id, channel, status, idempotency_key, retry_count,
// next_retry_at, last_error, batch_id, batch_key, provider_status. Empty/nil
// string columns become NULL; retry_count passes through.
func (n Notification) TypedValues() []any {
	ev := func(k string) any {
		v, ok := n.d.Extra[k]
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
		nz(n.d.AlertGroupID), ev("user_id"), nz(n.d.Channel), nz(n.d.Status),
		nz(n.d.IdempotencyKey), n.d.RetryCount, ev("next_retry_at"),
		ev("last_error"), ev("batch_id"), ev("batch_key"), ev("provider_status"),
		ev("integration_id"),
	}
}

// NewNotification constructs a notification in its initial state. The default
// status is "delivered": channels without a real delivery step (log, chatops,
// mobile) are recorded as immediately delivered; callers move the notification
// to delivery_scheduled (ScheduleDelivery) or batched (AttachToBatch) when a
// provider delivery is required. An empty idempotencyKey derives the default
// alertGroupID:userID:channel:target:reason key; an empty userID is stored as
// NULL (escalation webhooks have no user).
//
// integrationID is denormalised from the alert group so that a notification can
// be team-scoped without a join. Unlike a copied team_id, it can never go stale:
// an alert group never moves between integrations.
func NewNotification(alertGroupID, integrationID, userID, channel, target, reason, timestamp, idempotencyKey string) Notification {
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("%s:%s:%s:%s:%s", alertGroupID, userID, channel, target, reason)
	}
	var uid any = userID
	if userID == "" {
		uid = nil
	}
	var integ any = integrationID
	if integrationID == "" {
		integ = nil
	}
	return Notification{d: &ntfData{
		ID:             utils.MakeID("ntf"),
		AlertGroupID:   alertGroupID,
		Channel:        channel,
		Target:         target,
		Status:         NotificationDelivered,
		Reason:         reason,
		IdempotencyKey: idempotencyKey,
		RetryCount:     0,
		CreatedAt:      timestamp,
		UpdatedAt:      timestamp,
		Extra: map[string]any{
			"user_id":         uid,
			"integration_id":  integ,
			"next_retry_at":   nil,
			"last_error":      nil,
			"batch_id":        nil,
			"batch_key":       nil,
			"provider_status": nil,
			"payload":         nil,
		},
	}}
}

// Raw returns a freshly rebuilt map view of the notification. Unlike the
// previous raw-backed wrapper this is a copy, not the live backing store —
// mutate through methods, not the returned map.
func (n Notification) Raw() map[string]any { return n.d.toMap() }

func (n Notification) ID() string      { return n.d.ID }
func (n Notification) Status() string  { return n.d.Status }
func (n Notification) Channel() string { return n.d.Channel }
func (n Notification) RetryCount() int { return n.d.RetryCount }

func (n Notification) IsDeliveryScheduled() bool { return n.Status() == NotificationDeliveryScheduled }
func (n Notification) IsRetryScheduled() bool    { return n.Status() == NotificationRetryScheduled }
func (n Notification) IsDelivering() bool        { return n.Status() == NotificationDelivering }
func (n Notification) IsRetrying() bool          { return n.Status() == NotificationRetrying }

// ReleaseClaim returns a claimed notification to a pending status without
// counting a delivery attempt — used when a delivery is consciously skipped
// (e.g. an open circuit breaker) so the next worker cycle re-picks it.
func (n Notification) ReleaseClaim(pendingStatus string) { n.d.Status = pendingStatus }

// Typed field accessors. Promoted fields read the struct; nullable fields read
// Extra.
func (n Notification) Target() string         { return n.d.Target }
func (n Notification) UserID() string         { return utils.StrVal(n.d.Extra, "user_id") }
func (n Notification) AlertGroupID() string   { return n.d.AlertGroupID }
func (n Notification) Reason() string         { return n.d.Reason }
func (n Notification) LastError() string      { return utils.StrVal(n.d.Extra, "last_error") }
func (n Notification) IdempotencyKey() string { return n.d.IdempotencyKey }
func (n Notification) CreatedAt() string      { return n.d.CreatedAt }

// IntegrationID is denormalised from the alert group (migration 0021); it is
// what scopes a notification to a team without a join.
func (n Notification) IntegrationID() string { return utils.StrVal(n.d.Extra, "integration_id") }

// EpisodeID and AnalyticsTeamID are the analytics context, snapshotted from the
// alert group when the notification is built.
//
// Snapshotted rather than looked up: the delivery worker's transaction loads
// notifications and attempts, not groups, and widening that hot write set so an
// analytics event can name an episode is the wrong trade. Copying is safe here
// in a way it would not be for a mutable field — a notification belongs to the
// pass of the group that produced it, permanently, and a later reopen must not
// retroactively move its pages onto the new episode.
//
// Empty on notifications created before this was recorded; the analytics view
// falls back to attributing those by time.
func (n Notification) EpisodeID() string       { return utils.StrVal(n.d.Extra, "episode_id") }
func (n Notification) AnalyticsTeamID() string { return utils.StrVal(n.d.Extra, "analytics_team_id") }

// SetAnalyticsContext records that pair. Absent values are not written, so a
// group with no team keeps the notification's shape unchanged rather than
// adding an empty key to every row.
func (n Notification) SetAnalyticsContext(episodeID, teamID string) {
	if episodeID != "" {
		n.d.Extra["episode_id"] = episodeID
	}
	if teamID != "" {
		n.d.Extra["analytics_team_id"] = teamID
	}
}

// Payload returns the stored delivery payload, or nil if unset/wrong type.
func (n Notification) Payload() map[string]any {
	m, _ := n.d.Extra["payload"].(map[string]any)
	return m
}

// ScheduleDelivery queues the notification for provider delivery with the
// given payload (picked up by the worker's delivery cycle).
func (n Notification) ScheduleDelivery(payload map[string]any) {
	n.d.Extra["payload"] = payload
	n.d.Status = NotificationDeliveryScheduled
}

// AttachToBatch parks the notification in an open batch; it will return to
// delivery via ReleaseFromBatch when the batch is flushed.
func (n Notification) AttachToBatch(batchID any, batchKey string) {
	n.d.Status = NotificationBatched
	n.d.Extra["batch_id"] = batchID
	n.d.Extra["batch_key"] = batchKey
	n.d.Extra["provider_status"] = "waiting_for_batch"
}

// MarkSkipped finalizes a notification that had no transport to go through.
// reason names the condition (not_configured, experimental) so the UI and the
// operator can tell "nobody was told, and here is why" from a real failure.
func (n Notification) MarkSkipped(ts, reason, detail string) {
	n.d.Status = NotificationSkipped
	n.d.Extra["provider_status"] = reason
	n.d.Extra["last_error"] = nilString(detail)
	n.d.Extra["next_retry_at"] = nil
	n.d.UpdatedAt = ts
}

func nilString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// IsSkipped reports the terminal "no transport" outcome.
func (n Notification) IsSkipped() bool { return n.Status() == NotificationSkipped }

// MarkDelivered finalizes a successful delivery attempt.
func (n Notification) MarkDelivered(ts, providerStatus string) {
	n.d.Status = NotificationDelivered
	n.d.Extra["provider_status"] = providerStatus
	n.d.Extra["last_error"] = nil
	n.d.Extra["next_retry_at"] = nil
	n.d.UpdatedAt = ts
}

// ScheduleRetryOrFail records a failed delivery attempt: it increments
// retry_count and either schedules the next retry (delays are minutes, indexed
// by attempt and clamped to the last entry) or marks the notification
// permanently failed once maxRetries is reached. An empty delays list fails
// the notification immediately instead of panicking.
func (n Notification) ScheduleRetryOrFail(ts, errMsg string, maxRetries int, delays []int) {
	n.ScheduleRetryOrFailAfter(ts, errMsg, maxRetries, delays, 0)
}

// ScheduleRetryOrFailAfter is ScheduleRetryOrFail with the wait the provider
// asked for.
//
// A provider that answered 429 named a time; retrying before it is a request
// guaranteed to be refused, and it arrives precisely during the burst that
// caused the limit. The retry budget still applies — a rate limit postpones an
// attempt, it does not buy extra ones — so a provider that keeps saying "later"
// still exhausts the retries and dead-letters rather than looping forever.
func (n Notification) ScheduleRetryOrFailAfter(ts, errMsg string, maxRetries int, delays []int, retryAfter time.Duration) {
	if errMsg == "" {
		errMsg = "delivery failed"
	}
	retryCount := n.RetryCount() + 1
	n.d.RetryCount = retryCount
	n.d.Extra["provider_status"] = "delivery_failed"
	n.d.Extra["last_error"] = errMsg
	n.d.UpdatedAt = ts
	if retryCount >= maxRetries || (len(delays) == 0 && retryAfter <= 0) {
		n.d.Status = NotificationFailed
		n.d.Extra["next_retry_at"] = nil
		return
	}
	wait := retryAfter
	if wait <= 0 {
		idx := retryCount - 1
		if idx >= len(delays) {
			idx = len(delays) - 1
		}
		wait = time.Duration(delays[idx]) * time.Minute
	}
	t, _ := utils.ParseDatetime(ts)
	n.d.Status = NotificationRetryScheduled
	n.d.Extra["next_retry_at"] = utils.ToISO(t.Add(wait))
}

// FailRetryContext marks a retry permanently failed because the delivery
// context (alert group / user payload) could not be reconstructed.
func (n Notification) FailRetryContext() {
	n.d.Status = NotificationFailed
	n.d.Extra["last_error"] = "retry context not found"
	n.d.Extra["next_retry_at"] = nil
}

// ReleaseFromBatch moves a batched notification to delivery with the given
// payload when its batch is flushed.
func (n Notification) ReleaseFromBatch(ts string, payload map[string]any) {
	n.d.Extra["payload"] = payload
	n.d.Status = NotificationDeliveryScheduled
	n.d.Extra["provider_status"] = "batch_flushed"
	n.d.UpdatedAt = ts
}

// FailBatchContext marks a batched notification failed because its delivery
// context could not be reconstructed at flush time.
func (n Notification) FailBatchContext(ts string) {
	n.d.Status = NotificationFailed
	n.d.Extra["last_error"] = "batch delivery context not found"
	n.d.Extra["provider_status"] = "batch_flushed"
	n.d.UpdatedAt = ts
}
