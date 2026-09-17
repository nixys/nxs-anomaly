// Package engine implements the OnCall business logic.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Escalation step kinds.
const (
	StepWait            = "WAIT"
	StepNotifyUser      = "NOTIFY_USER"
	StepNotifySchedule  = "NOTIFY_SCHEDULE"
	StepNotifyTeam      = "NOTIFY_TEAM"
	StepNotifyEmergency = "NOTIFY_EMERGENCY"
	StepNotifyDutyUsers = "NOTIFY_DUTY_USERS"
	StepTriggerWebhook  = "TRIGGER_WEBHOOK"
	StepCreateIssue     = "CREATE_ISSUE"
	StepResolve         = "RESOLVE"
	StepRepeat          = "REPEAT"
)

var supportedSteps = map[string]bool{
	StepWait: true, StepNotifyUser: true, StepNotifySchedule: true,
	StepNotifyTeam: true, StepNotifyEmergency: true, StepNotifyDutyUsers: true,
	StepTriggerWebhook: true, StepCreateIssue: true, StepResolve: true, StepRepeat: true,
}

var supportedShiftRecurrences = map[string]bool{"none": true, "daily": true, "weekly": true}

var supportedPriorities = map[string]bool{"low": true, "medium": true, "high": true}

var priorityOrder = []string{"high", "medium", "low"}

var resolvedStatuses = map[string]bool{"resolved": true, "ok": true, "closed": true}

var supportedNotificationTargets = map[string]bool{
	"log": true, "webhook": true, "telegram": true, "email": true,
	"call": true, "slack": true, "mattermost": true,
}

var defaultNotificationPolicy = map[string]any{
	"channels":               []any{},
	"batch_timeout_seconds":  0,
	"batch_deadline_seconds": 0,
	"emergency_user_id":      nil,
	"epic_user_id":           nil,
	"epic_threshold_count":   0,
	"epic_threshold_seconds": 0,
}

// Advisory lock keys — must be unique across all operations.
var advisoryLock = map[string]int64{
	// worker pipeline
	"ingest_alert": 72544101,
	// Reserved (never reuse): 72544102 was the global escalation xact lock and
	// 72544300 the single escalation gate; escalation now shards by integration
	// (escalationShardKey, range 72546000+) so replicas escalate different
	// integrations in parallel. 72544103 / 72544105 were the deliveries/retries
	// save locks, dropped when per-row claims (FOR UPDATE SKIP LOCKED) replaced them.
	"process_notification_batches": 72544104,
	"publish_kafka_outbox":         72544106,
	// entity creation
	"create_user":             72544200,
	"create_team":             72544201,
	"create_schedule":         72544202,
	"create_escalation_chain": 72544203,
	"create_integration":      72544204,
	"create_chatops_channel":  72544206,
	"post_chatops_command":    72544207,
	"register_mobile_device":  72544208,
	"create_mobile_session":   72544209,
	// alert group state transitions
	"acknowledge_group":   72544210,
	"resolve_group":       72544211,
	"unresolve_group":     72544216,
	"unacknowledge_group": 72544217,
	// user profile
	"toggle_user_duty": 72544212,
	"update_user":      72544213,
	// schedule overrides
	"create_schedule_override": 72544214,
	"update_schedule_override": 72544232,
	"delete_schedule_override": 72544233,
	// schedule maintenance
	"purge_user_from_schedules": 72544234,
	"notify_schedule_shift":     72544235,
	"duty_checkins":             72544236,
	"heartbeat_state":           72544237,
	// entity updates
	"update_team":             72544220,
	"update_schedule":         72544221,
	"update_escalation_chain": 72544222,
	"update_integration":      72544223,
	"update_chatops_channel":  72544224,
	// key rotation
	"rotate_integration_key": 72544231,
	// bulk operations
	"bulk_resolve_groups":     72544215,
	"silence_group":           72544218,
	"bulk_acknowledge_groups": 72544219,
	"bulk_silence_groups":     72544225,
	// personal notification policy runs (BETA-031)
	"advance_policy_runs": 72544240,
	// planned-maintenance windows
	"create_maintenance_window": 72544241,
	"update_maintenance_window": 72544242,
	"generate_oncall_report":    72544243,
	// delivery outcomes written back to the group timeline
	"record_delivery_failures": 72544244,
}

// Engine is the OnCall business logic engine.
type Engine struct {
	store         store.PostgreSQLStore
	deliveryCfg   DeliveryConfig
	templateCache sync.Map  // "integrationID:channel" → templateCacheEntry; lazy, TTL-bounded
	refCache      *refCache // short-TTL cache of reference collections; nil in unit tests
	kafkaProducer OutboxProducer
	// kafkaTopic and kafkaOutboxCycleBudget are set here and read only by files
	// tagged !community — the analytics emitters and the outbox drain. The cut
	// deletes every one of those readers, so in that edition the fields really
	// are unused, and `unused` says so. A struct cannot be split across build
	// tags, so the exemption is stated at the field rather than the field moved.
	kafkaTopic string //nolint:unused // read only by the enterprise build
	// kafkaOutboxCycleBudget overrides outboxCycleBudget when non-zero. Tests
	// use this to remove the wall-clock race between the drain loop and the
	// count-based outboxCycleLimit (the default budget is real time, and a
	// slow test run — e.g. under -race — can hit it before the count does).
	kafkaOutboxCycleBudget time.Duration   //nolint:unused // read only by the enterprise build
	metrics                MetricsSink     // engine-level metric sink; noopMetrics until SetMetricsSink
	breaker                *circuitBreaker // per-channel/target delivery breaker; nil when disabled
	workerID               string          // identifies this process when claiming notifications
	// lastCoverageCheck throttles the standing schedule-coverage check. Only
	// the worker goroutine touches it, and RunWorkerCycle is serialised against
	// itself, so it needs no lock.
	lastCoverageCheck time.Time
	// lastHeartbeat throttles the worker heartbeat that readiness reads. Same
	// ownership as lastCoverageCheck: worker goroutine only.
	lastHeartbeat time.Time
	// reopenAckedOnNewAlert is the deployment's policy for an alert arriving on
	// an acknowledged group. Off by default: an acknowledgement means an
	// operator answered, and a source that keeps re-sending the same alert must
	// not be able to page them again for the event they already took. See
	// ingestOneLocked.
	reopenAckedOnNewAlert bool
	// reportSource is the ClickHouse-backed reader the on-call quality report
	// queries. Set by SetReportSource (enterprise builds only, when
	// NXS_ANOMALY_CLICKHOUSE_HOST is configured); nil in the community edition
	// and in any enterprise install that has not configured ClickHouse, which
	// GenerateOnCallQualityReport reports as ErrReportsNotAvailable rather than
	// treating as a bug.
	reportSource reportDataSource
	// reportRecipients is the digest's email distribution list
	// (NXS_ANOMALY_REPORT_RECIPIENTS), read once at startup like the rest of
	// DeliveryConfig.
	reportRecipients []string
}

// coverageCheckInterval is how often the worker re-checks schedule coverage.
// The check reads the schedule, user and chain tables; coverage only changes
// when someone edits a schedule, so running it per worker cycle would be
// several hundred pointless reads an hour.
const coverageCheckInterval = time.Minute

// OutboxMessage is one outbox event on its way to the bus. It carries its own
// topic because a single batch mixes topics: an integration may override the
// default one.
//
// It is declared here, next to the port that consumes it, rather than in
// internal/kafka: that package is enterprise-only and is removed wholesale from
// the community tree, so a type defined there could not appear in this file.
type OutboxMessage struct {
	Topic string
	Key   string
	Value []byte
}

// OutboxProducer is the engine's view of the message bus that drains the
// transactional outbox. It is declared here rather than imported from
// internal/kafka so that the engine compiles in a build that ships no producer
// at all; kafka.Producer satisfies it, because Go interfaces are structural. In
// such a build the field simply stays nil, which is the same state a deployment
// with no broker configured has always been in.
//
// It is exported so that NewWithKafka can name it, which is what keeps the
// dependency pointing one way: the adapter knows the engine, the engine does
// not know the adapter.
type OutboxProducer interface {
	PublishBatch(ctx context.Context, msgs []OutboxMessage) error
	Close() error
}

// New creates a new Engine backed by the given store.
// Delivery adapter config is loaded from environment variables once at startup.
func New(s store.PostgreSQLStore) *Engine {
	cache := newRefCache()
	cfg := DeliveryConfigFromEnv()
	cfg.warnClaimTimeoutRisk()
	cfg.proxies.logStartup()
	return &Engine{
		store:                 refInvalidatingStore{PostgreSQLStore: s, cache: cache},
		refCache:              cache,
		deliveryCfg:           cfg,
		metrics:               noopMetrics{},
		breaker:               newCircuitBreaker(cfg.CircuitBreakerThreshold, cfg.CircuitBreakerCooldown),
		workerID:              utils.MakeID("wkr"),
		reopenAckedOnNewAlert: os.Getenv("NXS_ANOMALY_REOPEN_ACKED_ON_NEW_ALERT") == "true",
		reportRecipients:      splitAndTrim(os.Getenv("NXS_ANOMALY_REPORT_RECIPIENTS")),
	}
}

// splitAndTrim splits a comma-separated env var into trimmed, non-empty parts.
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// SetReportSource wires the ClickHouse-backed reader the on-call quality
// report queries. Left nil, GenerateOnCallQualityReport reports
// ErrReportsNotAvailable — the same state a community build or an enterprise
// install with no ClickHouse DSN configured is always in.
func (e *Engine) SetReportSource(src reportDataSource) { e.reportSource = src }

// Close releases resources held by the engine. Must be called on shutdown to
// flush and close the Kafka producer connection.
func (e *Engine) Close() {
	if closer, ok := e.reportSource.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			slog.Error("clickhouse_report_source_close_failed", "error", err)
		}
	}
	if e.kafkaProducer != nil {
		if err := e.kafkaProducer.Close(); err != nil {
			slog.Error("kafka_producer_close_failed", "error", err)
		}
	}
}

// ingestLockKey derives a per-integration advisory lock key using FNV-1a hash
// mapped to 1024 buckets starting at 72545000. Two integrations may share a bucket
// (birthday collision) but concurrent ingests from different integrations usually
// run in parallel.
//
// KNOWN AND ACCEPTED: this window, [72545000, 72546023], overlaps the escalation
// shard window [72546000, 72547023] in its last 24 keys. PostgreSQL keeps
// session-level and transaction-level advisory locks in ONE key space, so on
// those 24 keys an in-flight ingest makes an escalation shard's
// pg_try_advisory_lock fail: the shard is skipped for that cycle and logs
// "another worker holds the shard" with no such worker in existence. The reverse
// costs an ingest a short wait.
//
// It is left alone deliberately. Correctness is unaffected — the shard lock still
// excludes other escalators — and the effect self-heals on the next cycle, about
// five seconds later. Every available fix changes which key an integration maps
// to, which during a rolling upgrade means the old and new replicas take
// different locks for the same integration and can escalate it twice (duplicate
// pages) or create duplicate alert groups. That is a worse failure than the one
// being fixed. Moving either range therefore needs a transitional release that
// takes both the old and the new key before the old one is dropped.
//
// TestHashedLockWindowsOverlapIsTheKnownOne pins the arithmetic so that widening
// a range, or moving one without doing the migration, fails loudly.
func ingestLockKey(integrationID string) int64 {
	h := uint32(2166136261)
	for i := 0; i < len(integrationID); i++ {
		h ^= uint32(integrationID[i])
		h *= 16777619
	}
	return 72545000 + int64(h%1024)
}

// FindItem returns a single item by collection and ID, or (nil, nil) if not found.
func (e *Engine) FindItem(ctx context.Context, collection, id string) (map[string]any, error) {
	return e.store.GetItem(ctx, collection, id)
}

// UpsertItem inserts or updates a single item in the given collection.
func (e *Engine) UpsertItem(ctx context.Context, collection string, item map[string]any) error {
	return e.store.UpsertItem(ctx, collection, item)
}

// RemoveItem deletes an item and returns it, or nil if not found.
func (e *Engine) RemoveItem(ctx context.Context, collection, id string) (map[string]any, error) {
	return e.store.DeleteItem(ctx, collection, id)
}

// CountCollection returns the number of items matching optional structured filters.
func (e *Engine) CountCollection(ctx context.Context, collection string, filters map[string]any) (int, error) {
	return e.store.CountCollection(ctx, collection, filters)
}

// ListCollection returns all items in a collection.
func (e *Engine) ListCollection(ctx context.Context, name string) ([]map[string]any, error) {
	items, err := e.store.ListCollection(ctx, name)
	if err != nil {
		return nil, err
	}
	// Same redaction as the paginated read: delivery reads these rows through
	// the store, never through here, so masking cannot break a send.
	return redactListForReader(ctx, name, items), nil
}

// GetItem returns a single item by collection and ID, or an error if not found.
func (e *Engine) GetItem(ctx context.Context, collection, id string) (map[string]any, error) {
	item, err := e.store.GetItem(ctx, collection, id)
	if err != nil {
		return nil, err
	}
	// A soft-deleted row is gone as far as the API is concerned. The lists
	// already hide it; answering 200 to a direct read made "delete it, then
	// check it is gone" — what the chart's own acceptance test does — impossible
	// to satisfy. Delivery still reads these rows straight from the store, so an
	// in-flight notification about a deleted integration is unaffected.
	if item == nil || softDeleted(item) {
		return nil, errNotFound(fmt.Sprintf("%s %s not found", collection, id))
	}
	if err := e.authorizeItem(ctx, collection, item); err != nil {
		return nil, err
	}
	return redactForReader(ctx, collection, item), nil
}

// ListCollectionPage returns a paginated result for a collection.
func (e *Engine) ListCollectionPage(ctx context.Context, collection string, params map[string]any) (map[string]any, error) {
	limit := clampInt(intFromAny(params["limit"], 100), 1, 1000)
	offset := maxInt(intFromAny(params["offset"], 0), 0)
	filterKeys := map[string]bool{"status": true, "severity": true, "integration_id": true,
		"channel": true, "user_id": true, "alert_group_id": true, "route_id": true}
	filters := map[string]any{}
	for k, v := range params {
		if filterKeys[k] && v != nil && v != "" {
			filters[k] = v
		}
	}
	// `ids` fetches a named set in one request. Without it a page that shows
	// something about each of fifty rows — the incident a delivery was about,
	// say — either asks fifty times or shows the id it already had, and showing
	// the id is the thing such a page exists to stop doing.
	if raw := utils.StrVal(params, "ids"); raw != "" {
		wanted := make([]any, 0, 8)
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				wanted = append(wanted, id)
			}
		}
		// An explicit but empty list asks for nothing, and gets nothing: the
		// alternative — silently listing everything — is how a scoped page turns
		// into a leak.
		filters["id"] = wanted
		limit = clampInt(limit, 1, 500)
	}
	// Narrow to what the actor's teams may see. A scope that cannot match
	// anything returns an empty page rather than an error: "no alert groups
	// reachable from your teams" is a legitimate answer, not a failure.
	reachable, err := e.applyScopeFilters(ctx, collection, filters)
	if err != nil {
		return nil, err
	}
	if !reachable {
		return pageEnvelope(nil, 0, limit, offset), nil
	}
	sortSpec, err := listSort(collection, params)
	if err != nil {
		return nil, err
	}
	items, total, err := e.store.ListCollectionPage(ctx, collection, filters, limit, offset, sortSpec)
	if err != nil {
		return nil, err
	}
	items = redactListForReader(ctx, collection, items)
	return pageEnvelope(items, total, limit, offset), nil
}

// listSort reads the sort/order parameters a listing was asked for.
//
// An unknown column is refused rather than ignored: a page that silently drops
// "sort=whatever" looks exactly like a page that sorted by it, and the person
// reading the list has no way to tell.
func listSort(collection string, params map[string]any) (store.SortSpec, error) {
	field := strings.TrimSpace(utils.StrVal(params, "sort"))
	order := strings.ToLower(strings.TrimSpace(utils.StrVal(params, "order")))
	if order != "" && order != "asc" && order != "desc" {
		return store.SortSpec{}, errValidation("order must be asc or desc")
	}
	spec, err := store.ResolveSort(collection, store.SortSpec{Field: field, Desc: order == "desc"})
	if err != nil {
		return store.SortSpec{}, errValidation(err.Error())
	}
	// "sort=x" without a direction means ascending, but a bare "order=desc"
	// must still reverse the collection's default column rather than fall back
	// to ascending id.
	if field == "" && order != "" {
		spec.Desc = order == "desc"
	}
	return spec, nil
}

// pageEnvelope builds the published pagination envelope.
//
// It exists for one reason: `items` must marshal as `[]`, never `null`. A query
// that matched nothing yields a nil slice all the way from scanRows, and JSON
// turns that into `null` — which is not what the OpenAPI contract promises, and
// which took the whole web UI down on a fresh installation (every list page does
// `data.items.length`, and one thrown TypeError blanks the SPA). An empty page is
// an ordinary answer, so it must have an ordinary shape.
func pageEnvelope(items []map[string]any, total, limit, offset int) map[string]any {
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}
}

// DeleteEntity removes an entity from an allowed collection.
// Integrations use soft-delete (deleted_at = NOW()) to preserve audit history.
func (e *Engine) DeleteEntity(ctx context.Context, collection, id string) (map[string]any, error) {
	allowed := map[string]bool{
		"users": true, "teams": true, "schedules": true,
		"escalation_chains": true, "integrations": true,
		"chatops_channels": true, "maintenance_windows": true,
	}
	if !allowed[collection] {
		// A validation error, not a bare one: the router reaches this only for
		// a collection it exposes a DELETE route for, and answering "Internal
		// Error" to a request the API documents made a missing entry here look
		// like a server fault. Which is exactly how the maintenance-window
		// delete was reported.
		return nil, errValidation(fmt.Sprintf("delete not supported for %s", collection))
	}
	// Checked before the delete, not after: SoftDeleteItem/DeleteItem have
	// already happened by the time we could inspect the returned row.
	if existing, err := e.store.GetItem(ctx, collection, id); err != nil {
		return nil, err
	} else if err := e.authorizeItem(ctx, collection, existing); err != nil {
		return nil, err
	} else if err := guardProvisioned(ctx, collection, existing); err != nil {
		// Deleting is the edit that cannot be undone by the next apply: the
		// pipeline would recreate the object with a new id, and everything
		// pointing at the old one — an escalation chain naming a schedule, an
		// integration naming a chain — would be pointing at nothing.
		return nil, err
	} else if existing != nil {
		if err := e.deleteBlockedByReferences(ctx, collection, id); err != nil {
			return nil, err
		}
	}
	var item map[string]any
	var err error
	// The delete and its audit record commit in one transaction, so a deleted
	// entity is never left without a "who deleted it" record.
	ev := e.buildAuditEvent(ctx, AuditDelete, singular(collection), id, nil)
	if collection == "integrations" {
		item, err = e.store.SoftDeleteItemAudited(ctx, collection, id, ev)
	} else {
		item, err = e.store.DeleteItemAudited(ctx, collection, id, ev)
	}
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, errNotFound(fmt.Sprintf("%s %s not found", collection, id))
	}
	if collection == "integrations" {
		e.invalidateTemplateCache(id)
	}
	if collection == "users" {
		// The FKs added in migration 0019 cascade, but only for deployments
		// where they were validated, and only for the real store. Clearing
		// explicitly makes "this person is gone" mean the same thing
		// everywhere: no credential left to sign in with, no session left
		// alive.
		if err := e.store.DeleteUserPassword(ctx, id); err != nil {
			slog.Error("delete_user_credentials_failed", "user_id", id, "error", err)
		}
		if _, err := e.store.RevokeUserSessions(ctx, id); err != nil {
			slog.Error("revoke_user_sessions_failed", "user_id", id, "error", err)
		}
		// A schedule that still names a deleted person pages nobody for that
		// slot, and the slot looks covered. Remove them so the hole becomes a
		// visible gap instead.
		if n, err := e.purgeUserFromSchedules(ctx, id); err != nil {
			slog.Error("purge_user_from_schedules_failed", "user_id", id, "error", err)
		} else if n > 0 {
			slog.Info("user_removed_from_schedules", "user_id", id, "schedules", n)
		}
	}
	return map[string]any{"deleted": true, "id": id, "item": item}, nil
}

// singular maps a collection name to the entity type recorded in the audit
// trail, so a filter reads entity_type=user rather than entity_type=users and
// matches the names used by the alert-group events.
func singular(collection string) string {
	return strings.TrimSuffix(collection, "s")
}

// --- schedule helpers ---

// getScheduleUserIDsAt returns the user IDs on-call for the given schedule at time at.
// It is a package-level function so callers outside Engine can use it without a receiver.
// The layer that answered (override, rotation or legacy shift) is resolved by
// resolveScheduleAt in schedule.go.
func getScheduleUserIDsAt(schedule map[string]any, at time.Time) []string {
	ids, _ := resolveScheduleAt(schedule, at)
	return ids
}

func (e *Engine) getScheduleUserIDs(schedule map[string]any, at time.Time) []string {
	return getScheduleUserIDsAt(schedule, at)
}

func shiftActive(shift map[string]any, at time.Time) bool {
	startAt, err1 := utils.ParseDatetime(utils.StrVal(shift, "start_at"))
	endAt, err2 := utils.ParseDatetime(utils.StrVal(shift, "end_at"))
	if err1 != nil || err2 != nil {
		return false
	}
	recurrence := strings.ToLower(utils.StrVal(shift, "recurrence"))
	if recurrence == "" {
		recurrence = "none"
	}
	if recurrence == "none" {
		return !at.Before(startAt) && at.Before(endAt)
	}
	if at.Before(startAt) {
		return false
	}
	duration := endAt.Sub(startAt)
	var period time.Duration
	switch recurrence {
	case "daily":
		period = 24 * time.Hour
	case "weekly":
		period = 7 * 24 * time.Hour
	default:
		return !at.Before(startAt) && at.Before(endAt)
	}
	elapsed := at.Sub(startAt)
	phase := elapsed % period
	return phase < duration
}

// --- group helpers ---

// getGroupOrError fetches an alert group by id from state and returns the
// typed wrapper. Returns errNotFound when the row is missing.
func getGroupOrError(state *store.State, groupID string) (model.AlertGroup, error) {
	g, ok := groupAG(state.AlertGroups[groupID])
	if !ok {
		return model.AlertGroup{}, errNotFound(fmt.Sprintf("alert group %s not found", groupID))
	}
	return g, nil
}

// groupAG type-asserts a State row to model.AlertGroup so engine helpers can
// work on the typed wrapper instead of a raw map. The second return is false
// when the row is missing or a different Record type.
func groupAG(r store.Record) (model.AlertGroup, bool) {
	if g, ok := r.(model.AlertGroup); ok {
		return g, true
	}
	return model.AlertGroup{}, false
}

// notificationMap unwraps a typed notification Record back into its raw map for
// the map-based delivery/retry helpers (deliverNotificationViaAdapter,
// notificationPayload, dead-letter event). notifications are stored in State as
// model.Notification Records; their internal representation is still a shared
// map, so callers keep mutating it in place.
func notificationMap(r store.Record) map[string]any {
	if n, ok := r.(model.Notification); ok {
		return n.Raw()
	}
	return nil
}

// --- ensure-exist helpers ---

func (e *Engine) ensureUsersExist(ctx context.Context, ids []string) error {
	return e.ensureItemsExist(ctx, "users", "unknown users", ids)
}

func (e *Engine) ensureTeamsExist(ctx context.Context, ids []string) error {
	return e.ensureItemsExist(ctx, "teams", "unknown teams", ids)
}

func (e *Engine) ensureSchedulesExist(ctx context.Context, ids []string) error {
	return e.ensureItemsExist(ctx, "schedules", "unknown schedules", ids)
}

func (e *Engine) ensureItemsExist(ctx context.Context, collection, label string, ids []string) error {
	seen := map[string]bool{}
	var missing []string
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		item, err := e.store.GetItem(ctx, collection, id)
		if err != nil {
			return err
		}
		if item == nil {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return errValidation(fmt.Sprintf("%s: %s", label, strings.Join(missing, ", ")))
	}
	return nil
}

func (e *Engine) ensureChainsExist(ctx context.Context, ids []string) error {
	return e.ensureItemsExist(ctx, "escalation_chains", "unknown escalation chains", ids)
}
