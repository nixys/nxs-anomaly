// Package store implements PostgreSQL persistence for nxs-anomaly.
package store

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// loadCollectionMaxRows is the threshold above which a warning is emitted when a
// collection is fully loaded into memory. It does not truncate the result —
// it surfaces the issue so operators know when the full-state load pattern
// needs to be replaced with pagination or point queries for that collection.
const loadCollectionMaxRows = 10_000

//go:embed migrations/*.sql
var migrationsFS embed.FS

// WorkerBatchLimit caps the number of rows fetched (and, for notifications,
// claimed) per worker cycle to bound memory and latency. Exported so the engine
// can size ClaimTimeout against the worst-case delivery-stage duration.
const WorkerBatchLimit = 500

// workerBatchLimit is the internal alias used by the store's own queries.
const workerBatchLimit = WorkerBatchLimit

// EntityTables maps collection names to PostgreSQL table names.
var EntityTables = map[string]string{
	"users":                          "nxs_anomaly_users",
	"teams":                          "nxs_anomaly_teams",
	"schedules":                      "nxs_anomaly_schedules",
	"escalation_chains":              "nxs_anomaly_escalation_chains",
	"integrations":                   "nxs_anomaly_integrations",
	"chatops_channels":               "nxs_anomaly_chatops_channels",
	"chatops_messages":               "nxs_anomaly_chatops_messages",
	"mobile_devices":                 "nxs_anomaly_mobile_devices",
	"mobile_sessions":                "nxs_anomaly_mobile_sessions",
	"alerts":                         "nxs_anomaly_alerts",
	"alert_groups":                   "nxs_anomaly_alert_groups",
	"notifications":                  "nxs_anomaly_notifications",
	"notification_batches":           "nxs_anomaly_notification_batches",
	"notification_delivery_attempts": "nxs_anomaly_notification_delivery_attempts",
	"notification_policy_runs":       "nxs_anomaly_notification_policy_runs",
	"kafka_outbox":                   "nxs_anomaly_kafka_outbox",
	"maintenance_windows":            "nxs_anomaly_maintenance_windows",
}

// TypedColumns lists extra indexed columns per collection (beyond id and data).
var TypedColumns = map[string][]string{
	"users":                          {"username", "email", "on_duty", "priority"},
	"integrations":                   {"key", "name", "type", "source_type", "deleted_at", "team_id"},
	"alert_groups":                   {"integration_id", "route_id", "escalation_chain_id", "dedupe_key", "status", "severity", "next_run_at", "last_received_at", "acknowledged_at", "resolved_at", "alert_count", "epic_sent_at"},
	"alerts":                         {"integration_id", "route_id", "status", "severity", "received_at", "alert_group_id"},
	"notifications":                  {"alert_group_id", "user_id", "channel", "status", "idempotency_key", "retry_count", "next_retry_at", "last_error", "batch_id", "batch_key", "provider_status", "integration_id"},
	"notification_batches":           {"batch_key", "status", "flush_at", "deadline_at", "alert_group_id", "integration_id"},
	"notification_delivery_attempts": {"notification_id", "channel", "target", "attempt", "status", "started_at", "finished_at"},
	"notification_policy_runs":       {"alert_group_id", "user_id", "status", "next_step_at"},
	"mobile_sessions":                {"token", "user_id", "device_id", "revoked_at"},
	"mobile_devices":                 {"user_id", "platform", "active"},
	"schedules":                      {"team_id"},
	"chatops_channels":               {"team_id", "user_id", "notifications_enabled"},
	"teams":                          {"name"},
	"kafka_outbox":                   {"topic", "created_at"},
	"maintenance_windows":            {"team_id", "starts_at", "ends_at"},
}

// SortSpec names the column a listing is ordered by. The zero value means the
// collection's default order, so a caller that does not care keeps the
// behaviour it had before sorting existed.
type SortSpec struct {
	Field string
	Desc  bool
}

// defaultSort is what a collection is ordered by when the caller asks for
// nothing.
//
// Ordering by id used to be the answer everywhere, and for a table of teams it
// still is. For the three collections below it was not an order at all: ids are
// random hex (utils.MakeID), so "the alert list" arrived in an arbitrary
// sequence that changed nothing when new alerts came in — the newest group
// could land on page four. Time is what a responder reads these lists by, so
// time is what they are ordered by.
var defaultSort = map[string]SortSpec{
	"alert_groups":  {Field: "last_received_at", Desc: true},
	"alerts":        {Field: "received_at", Desc: true},
	"notifications": {Field: "created_at", Desc: true},
}

// rowTimestampColumns are the columns every entity table carries besides its
// typed ones. kafka_outbox is the single exception — it has created_at but no
// updated_at — and it is not reachable from any list route, but the allow-list
// is what keeps a caller-supplied field out of the SQL, so it stays honest.
func rowTimestampColumns(collection string) []string {
	if collection == "kafka_outbox" {
		return []string{"created_at"}
	}
	return []string{"created_at", "updated_at"}
}

// SortableFields lists the columns a collection may be ordered by: its primary
// key, its typed columns and its row timestamps. Nothing else is accepted —
// the field is interpolated into the query, and this list is what makes that
// safe.
func SortableFields(collection string) []string {
	out := []string{"id"}
	out = append(out, TypedColumns[collection]...)
	return append(out, rowTimestampColumns(collection)...)
}

// ResolveSort validates a requested sort and fills in the collection's default.
// An unknown field is an error rather than a silent fallback: a listing that
// quietly ignores "sort=whatever" looks like it sorted.
func ResolveSort(collection string, spec SortSpec) (SortSpec, error) {
	if spec.Field == "" {
		return defaultSort[collection], nil
	}
	for _, allowed := range SortableFields(collection) {
		if spec.Field == allowed {
			return spec, nil
		}
	}
	return SortSpec{}, fmt.Errorf("unsupported sort field %q for collection %s", spec.Field, collection)
}

// SeverityLevels is the ladder this service orders incidents by, most important
// first. It is the vocabulary the UI offers in a filter, and the only one.
//
// Sources do not share it. Prometheus sends critical/warning/info, Zabbix sends
// high/average, a hand-written webhook sends P1 — and the alert is stored with
// the word its source used, because that word is what the source said and
// rewriting it would lose it. severityAliases is how the two are reconciled:
// every spelling this service recognises maps to one level, and ranking,
// sorting and filtering all go through that mapping.
//
// Before this existed the three disagreed: the UI filter offered five words,
// the badge coloured eight, and the rank knew five — so an alert stored as
// "high" was painted red, could not be selected by any filter, and sorted below
// "debug". Adding a spelling now means adding it here, once.
var SeverityLevels = []string{"critical", "error", "warning", "info", "debug"}

// severityAliases maps each level to every spelling that means it, the level's
// own name included. Kept deliberately short: a word lands here when a real
// source sends it, not because it could plausibly mean something. A spelling
// this table does not know keeps its own name, ranks below "debug" and is
// matched exactly — which is honest, and visible in the UI as a grey badge
// carrying the raw word.
//
// The frontend mirrors this table in src/domain/severity.ts, where it needs the
// same mapping to colour a badge without asking the server. Change one, change
// both.
var severityAliases = map[string][]string{
	"critical": {"critical", "crit", "fatal", "emergency", "disaster", "sev1", "p1"},
	"error":    {"error", "err", "high", "major", "sev2", "p2"},
	"warning":  {"warning", "warn", "medium", "average", "minor", "sev3", "p3"},
	"info":     {"info", "informational", "notice", "low", "sev4", "p4"},
	"debug":    {"debug", "trace", "sev5", "p5"},
}

// severityRank orders a severity by how much it matters rather than by how it
// spells: alphabetically, "critical" sorts between "alert" and "debug", so a
// list sorted by severity would put the page-somebody-now alerts in the middle.
// Unknown values rank lowest — a severity this service does not model is not a
// reason to push something to the top of an on-call queue.
var severityRank = func() map[string]int {
	ranks := map[string]int{}
	for i, level := range SeverityLevels {
		rank := len(SeverityLevels) - i
		for _, alias := range severityAliases[level] {
			ranks[alias] = rank
		}
	}
	return ranks
}()

// severityLevelOf maps any known spelling to its level.
var severityLevelOf = func() map[string]string {
	levels := map[string]string{}
	for level, aliases := range severityAliases {
		for _, alias := range aliases {
			levels[alias] = level
		}
	}
	return levels
}()

// SeverityRank is severityRank for callers outside this package (the in-memory
// test doubles, which have to order the same way the SQL does).
func SeverityRank(severity string) int { return severityRank[strings.ToLower(severity)] }

// SeverityFamily returns every spelling that ranks the same as severity —
// filtering by "critical" has to return the group a source labelled "P1", or
// the filter answers a question nobody asked. An unknown spelling is its own
// family of one, so a filter on it still matches exactly what it names.
func SeverityFamily(severity string) []any {
	level, ok := severityLevelOf[strings.ToLower(strings.TrimSpace(severity))]
	if !ok {
		return []any{severity}
	}
	aliases := severityAliases[level]
	family := make([]any, len(aliases))
	for i, alias := range aliases {
		family[i] = alias
	}
	return family
}

// severityRankSQL is the same table as a SQL expression. It is built from the
// map so the two cannot drift, and it takes no arguments because it is spliced
// into an ORDER BY, where placeholders are not allowed to carry the sort key.
func severityRankSQL() string {
	// Sorted for a stable statement: an ORDER BY that differs run to run
	// defeats PostgreSQL's plan cache for no reason.
	keys := make([]string, 0, len(severityRank))
	for k := range severityRank {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("CASE lower(severity)")
	for _, k := range keys {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", k, severityRank[k])
	}
	b.WriteString(" ELSE 0 END")
	return b.String()
}

// orderClause renders the ORDER BY for a resolved spec.
//
// Two details carry weight. NULLS LAST keeps rows that never got a timestamp
// (a group that has not received an alert yet) out of the top of a descending
// page. And id is always the last key: without a tie-break, two rows with the
// same timestamp can swap places between two queries, which in an OFFSET-paged
// listing shows one row twice and hides another entirely.
func orderClause(spec SortSpec) string {
	if spec.Field == "" || spec.Field == "id" {
		if spec.Desc {
			return " ORDER BY id DESC"
		}
		return " ORDER BY id"
	}
	dir := "ASC"
	if spec.Desc {
		dir = "DESC"
	}
	column := spec.Field
	if spec.Field == "severity" {
		column = severityRankSQL()
	}
	return fmt.Sprintf(" ORDER BY %s %s NULLS LAST, id %s", column, dir, dir)
}

// State is a snapshot of all in-memory collections loaded from the database.
type State struct {
	Users            map[string]map[string]any
	Teams            map[string]map[string]any
	Schedules        map[string]map[string]any
	EscalationChains map[string]map[string]any
	Integrations     map[string]map[string]any
	GrafanaPlugins   map[string]map[string]any
	ChatopsChannels  map[string]map[string]any
	ChatopsMessages  map[string]map[string]any
	MobileDevices    map[string]map[string]any
	MobileSessions   map[string]map[string]any
	// Alerts, AlertGroups, Notifications and NotificationBatches are typed
	// collections: rows are Records (model.Alert / model.AlertGroup /
	// model.Notification / *model.NotificationBatch), not raw maps. They are
	// excluded from the generic ptrs map and handled in recordsOf/setRecordsOf.
	Alerts                       map[string]Record
	AlertGroups                  map[string]Record
	Notifications                map[string]Record
	NotificationBatches          map[string]Record
	NotificationDeliveryAttempts map[string]map[string]any
	NotificationPolicyRuns       map[string]map[string]any
	GrafanaNotificationPolicies  map[string]map[string]any
	GrafanaChannelFilters        map[string]map[string]any
	GrafanaHeartbeats            map[string]map[string]any
	KafkaOutbox                  map[string]map[string]any
	MaintenanceWindows           map[string]map[string]any

	// AuditEvents are enrolled by a mutator to be written in the SAME transaction
	// as the state change, so a committed operation always has its audit record
	// and a rolled-back one leaves none. Flushed by updateCollections after the
	// mutator succeeds.
	AuditEvents []AuditEvent

	ptrs map[string]*map[string]map[string]any // cached field pointers, set by NewState
}

func buildCollectionPtrs(s *State) map[string]*map[string]map[string]any {
	return map[string]*map[string]map[string]any{
		"users":                          &s.Users,
		"teams":                          &s.Teams,
		"schedules":                      &s.Schedules,
		"escalation_chains":              &s.EscalationChains,
		"integrations":                   &s.Integrations,
		"grafana_plugins":                &s.GrafanaPlugins,
		"chatops_channels":               &s.ChatopsChannels,
		"chatops_messages":               &s.ChatopsMessages,
		"mobile_devices":                 &s.MobileDevices,
		"mobile_sessions":                &s.MobileSessions,
		"notification_delivery_attempts": &s.NotificationDeliveryAttempts,
		"notification_policy_runs":       &s.NotificationPolicyRuns,
		"grafana_notification_policies":  &s.GrafanaNotificationPolicies,
		"grafana_channel_filters":        &s.GrafanaChannelFilters,
		"grafana_heartbeats":             &s.GrafanaHeartbeats,
		"kafka_outbox":                   &s.KafkaOutbox,
		"maintenance_windows":            &s.MaintenanceWindows,
	}
}

// NewState returns a State with all maps initialized.
func NewState() *State {
	s := &State{}
	s.ptrs = buildCollectionPtrs(s)
	for _, p := range s.ptrs {
		*p = make(map[string]map[string]any)
	}
	s.Alerts = map[string]Record{}
	s.AlertGroups = map[string]Record{}
	s.Notifications = map[string]Record{}
	s.NotificationBatches = map[string]Record{}
	return s
}

// GetCollection returns the map for a given collection name.
func (s *State) GetCollection(name string) map[string]map[string]any {
	if p, ok := s.ptrs[name]; ok {
		return *p
	}
	return nil
}

// SetCollection replaces a collection map in state.
func (s *State) SetCollection(name string, m map[string]map[string]any) {
	if p, ok := s.ptrs[name]; ok {
		*p = m
	}
}

// rawBacked is implemented by every map-backed Record wrapper (model.User,
// model.Team, etc. via embedded mapBacked). setRecordsOf uses this interface
// to unwrap a Record back into its raw map when storing into a non-typed
// State collection. Typed-fields wrappers (AlertGroup/Notification/Alert/
// NotificationBatch) bypass this path entirely — they're stored as Records
// in the typed State fields below.
type rawBacked interface {
	Raw() map[string]any
}

// recordsOf returns a collection as id→Record for the generic load/save path.
// The four typed-fields collections return their State map directly; for the
// remaining collections each raw-map item is wrapped on the fly through the
// collection's registered wrapper (the same one used by decodeRecord on load).
func (s *State) recordsOf(col string) map[string]Record {
	switch col {
	case "alerts":
		return s.Alerts
	case "alert_groups":
		return s.AlertGroups
	case "notifications":
		return s.Notifications
	case "notification_batches":
		return s.NotificationBatches
	}
	m := s.GetCollection(col)
	if m == nil {
		return nil
	}
	out := make(map[string]Record, len(m))
	for id, item := range m {
		rec, err := wrapRecord(col, item)
		if err != nil {
			// A missing wrapper is a programming error (every EntityTables
			// collection must register one) — surface it loudly.
			panic(fmt.Sprintf("recordsOf %s: %v", col, err))
		}
		out[id] = rec
	}
	return out
}

// setRecordsOf stores a collection loaded as Records back into State. Typed
// collections keep the Record map; map-backed collections are unwrapped to
// raw maps via the rawBacked interface so engine code keeps reading from
// State.<Collection> as map[string]map[string]any.
func (s *State) setRecordsOf(col string, recs map[string]Record) {
	switch col {
	case "alerts":
		s.Alerts = recs
		return
	case "alert_groups":
		s.AlertGroups = recs
		return
	case "notifications":
		s.Notifications = recs
		return
	case "notification_batches":
		s.NotificationBatches = recs
		return
	}
	m := make(map[string]map[string]any, len(recs))
	for id, r := range recs {
		if rb, ok := r.(rawBacked); ok {
			m[id] = rb.Raw()
		}
	}
	s.SetCollection(col, m)
}

// Get returns an item by collection name and ID, or nil if not found.
func (s *State) Get(collection, id string) map[string]any {
	c := s.GetCollection(collection)
	if c == nil {
		return nil
	}
	return c[id]
}

// Set stores an item by collection name and ID.
func (s *State) Set(collection, id string, item map[string]any) {
	c := s.GetCollection(collection)
	if c != nil {
		c[id] = item
	}
}

// LoadSpec names a collection to load inside UpdateCollectionsFiltered and an
// optional filter over its typed columns (validated by buildWhere; values may
// be plain scalars, []any for IN, or NotEqualFilter). Nil Filters loads the
// whole collection.
type LoadSpec struct {
	Collection string
	Filters    map[string]any
}

// PostgreSQLStore is the interface for all persistence operations.
type PostgreSQLStore interface {
	UpdateCollections(ctx context.Context, loadCollections, saveCollections []string, mutator func(*State) (any, error), lockKey int64) (any, error)
	UpdateCollectionsFiltered(ctx context.Context, loads []LoadSpec, saveCollections []string, mutator func(*State) (any, error), lockKey int64) (any, error)
	UpdateCollectionsWriteAll(ctx context.Context, loads []LoadSpec, saveCollections, writeAll []string, mutator func(*State) (any, error), lockKey int64) (any, error)
	ReadCollections(ctx context.Context, collections []string) (*State, error)
	ListCollection(ctx context.Context, collection string) ([]map[string]any, error)
	GetItem(ctx context.Context, collection, id string) (map[string]any, error)
	UpsertItem(ctx context.Context, collection string, item map[string]any) error
	DeleteItem(ctx context.Context, collection, id string) (map[string]any, error)
	SoftDeleteItem(ctx context.Context, collection, id string) (map[string]any, error)
	// The *Audited variants write the item change and the audit event in one
	// transaction, so a single-item write is never committed without its record.
	UpsertItemAudited(ctx context.Context, collection string, item map[string]any, ev AuditEvent) error
	DeleteItemAudited(ctx context.Context, collection, id string, ev AuditEvent) (map[string]any, error)
	SoftDeleteItemAudited(ctx context.Context, collection, id string, ev AuditEvent) (map[string]any, error)
	CountCollection(ctx context.Context, collection string, filters map[string]any) (int, error)
	CollectionsHaveAny(ctx context.Context, collections []string) (bool, error)
	ClearCollections(ctx context.Context, collections []string) error
	FindIntegrationByKey(ctx context.Context, key string) (map[string]any, error)
	LastAlertReceivedAt(ctx context.Context, integrationID string) (time.Time, bool, error)
	FindMobileSessionByToken(ctx context.Context, token string) (map[string]any, error)
	FindActiveAlertGroup(ctx context.Context, integrationID, dedupeKey string) (map[string]any, error)
	ListDueAlertGroups(ctx context.Context, nowISO string) ([]map[string]any, error)
	ListDueNotificationBatches(ctx context.Context, nowISO string) ([]map[string]any, error)
	// OldestDueEscalationAgeSeconds / OldestPendingDeliveryAgeSeconds expose how
	// far behind the worker is, for the lag gauges (0 when nothing is pending).
	OldestDueEscalationAgeSeconds(ctx context.Context) (float64, error)
	OldestPendingDeliveryAgeSeconds(ctx context.Context) (float64, error)
	// ClaimDeliverableNotifications atomically moves up to a batch of
	// delivery_scheduled notifications to status 'delivering' (recording
	// claimed_at/claimed_by) and returns the claimed rows, so each row is
	// delivered by exactly one worker (FOR UPDATE SKIP LOCKED).
	ClaimDeliverableNotifications(ctx context.Context, workerID, nowISO string) ([]map[string]any, error)
	// ClaimRetryableNotifications is ClaimDeliverableNotifications for due
	// retry_scheduled notifications, moving them to status 'retrying'.
	ClaimRetryableNotifications(ctx context.Context, workerID, nowISO string) ([]map[string]any, error)
	// ReclaimStaleClaims resets notifications stuck in 'delivering'/'retrying'
	// whose claimed_at is older than cutoffISO (worker crashed mid-delivery)
	// back to delivery_scheduled/retry_scheduled. Returns the number reset.
	ReclaimStaleClaims(ctx context.Context, cutoffISO string) (int, error)
	ListCollectionPage(ctx context.Context, collection string, filters map[string]any, limit, offset int, sort SortSpec) ([]map[string]any, int, error)
	ListItemsIn(ctx context.Context, collection, field string, values []any) ([]map[string]any, error)
	ListItemsByIDs(ctx context.Context, collection string, ids []string) ([]map[string]any, error)
	QueryHistoryGroups(ctx context.Context, filters map[string]any, limit, offset int) ([]map[string]any, int, error)
	DeleteOldResolvedGroups(ctx context.Context, cutoffISO string) (int, error)
	// SetAlertStatusForGroups carries a group's resolution down to its alerts.
	// See store_alerts.go for why it is one statement outside the group's
	// transaction rather than part of the collection save cycle.
	SetAlertStatusForGroups(ctx context.Context, groupIDs []string, status string) (int, error)
	// InsertAuditEvent appends to the append-only audit trail. There is no
	// update or delete counterpart on purpose: the table rejects both.
	InsertAuditEvent(ctx context.Context, ev AuditEvent) error
	ListAuditEvents(ctx context.Context, filters map[string]any, limit, offset int) ([]map[string]any, int, error)
	// PruneAuditEvents is the only way rows leave the audit table. It is called
	// by the worker's retention sweep, never from a request path.
	PruneAuditEvents(ctx context.Context, cutoffISO string) (int, error)
	// PseudonymiseAuditActor replaces the personal fields an audit row carries
	// about one actor (their name and the request IP) with a pseudonym, leaving
	// the ids, actions and timestamps — the trail itself — untouched. Used by
	// user erasure; see store_erasure.go for why the trail is edited rather
	// than deleted.
	PseudonymiseAuditActor(ctx context.Context, actorID, pseudonym string) (int, error)
	// DeleteUserWebSessions and ScrubUserNotificationTargets are the other two
	// halves of user erasure: the session rows carry the client IP, and the
	// notification/attempt rows carry the address the person was paged at. See
	// store_erasure.go.
	DeleteUserWebSessions(ctx context.Context, userID string) (int, error)
	ScrubUserNotificationTargets(ctx context.Context, userID, replacement string) (int, error)
	// Retention deletes. Called only from the worker's retention sweep; see
	// store_retention.go.
	DeleteOldNotifications(ctx context.Context, cutoffISO string) (int, error)
	DeleteOldDeliveryAttempts(ctx context.Context, cutoffISO string) (int, error)
	DeleteOldWebSessions(ctx context.Context, cutoffISO string) (int, error)
	// Cluster-wide token buckets (migration 0024), used by the sign-in limiter
	// so the limit does not multiply by the replica count. See
	// store_rate_limit.go; the ingest limiter stays in memory by design.
	ConsumeRateToken(ctx context.Context, key string, ratePerSecond, capacity float64) (bool, error)
	RefundRateToken(ctx context.Context, key string, capacity float64) error
	PruneRateBuckets(ctx context.Context, cutoffISO string) (int, error)
	// Identity: local credentials and browser sessions (migration 0019). These
	// tables are deliberately outside the State collections — see
	// store_identity.go for why.
	SetUserPassword(ctx context.Context, userID, passwordHash string) error
	GetUserPasswordHash(ctx context.Context, userID string) (hash string, ok bool, err error)
	DeleteUserPassword(ctx context.Context, userID string) error
	CountUsersWithPassword(ctx context.Context) (int, error)
	FindUserByLogin(ctx context.Context, login string) (map[string]any, error)
	ListTeamIDsForUser(ctx context.Context, userID string) ([]string, error)
	CreateWebSession(ctx context.Context, sess WebSession) error
	FindSessionUser(ctx context.Context, tokenHash string) (user map[string]any, sessionID string, err error)
	ListWebSessions(ctx context.Context, userID string) ([]WebSession, error)
	RevokeWebSessionByID(ctx context.Context, sessionID, userID string) (bool, error)
	RevokeWebSession(ctx context.Context, tokenHash string) error
	RevokeUserSessions(ctx context.Context, userID string) (int, error)
	DeleteExpiredWebSessions(ctx context.Context) (int, error)
	ListKafkaOutboxEvents(ctx context.Context, limit int) ([]map[string]any, error)
	CountKafkaOutboxByTopic(ctx context.Context) (map[string]int, error)
	DeleteKafkaOutboxEvents(ctx context.Context, ids []string) error
	DeleteOldChatopsMessages(ctx context.Context, cutoffISO string) (int, error)
	// TryAdvisoryLock attempts a non-blocking session-level advisory lock.
	// Returns (true, unlock, nil) on success; (false, nil, nil) if already held.
	// The caller MUST call unlock() to release the lock and return the connection.
	TryAdvisoryLock(ctx context.Context, key int64) (bool, func(), error)
	// NotifyWake signals worker loops (PostgreSQL NOTIFY) that new work exists.
	NotifyWake(ctx context.Context) error
	// WaitForWake blocks until a wake signal or timeout; true when woken early.
	WaitForWake(ctx context.Context, timeout time.Duration) bool
	// Deployment metadata: facts about the installation rather than about any
	// domain object (last reported backup, accepted readiness blockers). See
	// store_metadata.go. PatchMetadata merges key by key and returns the result;
	// a nil value deletes its key.
	GetMetadata(ctx context.Context) (map[string]any, error)
	PatchMetadata(ctx context.Context, patch map[string]any) (map[string]any, error)
	// PoolStats returns a snapshot of the connection pool for metrics.
	PoolStats() PoolStats
	Ping(ctx context.Context) error
	Close()
}

// PoolStats is a snapshot of the pgx connection pool state, surfaced for metrics
// so operators can see whether delivery concurrency is saturating the pool.
type PoolStats struct {
	AcquiredConns     int32
	IdleConns         int32
	TotalConns        int32
	MaxConns          int32
	EmptyAcquireCount int64 // cumulative acquires that had to wait for a free conn
}

// pgStore implements PostgreSQLStore using pgxpool.
type pgStore struct {
	pool *pgxpool.Pool

	// Dedicated LISTEN connection for WaitForWake; lazily acquired,
	// re-established after errors. Guarded by listenMu.
	listenMu   sync.Mutex
	listenConn *pgxpool.Conn
}

// NewPostgreSQLStore creates and initializes a store from env vars. When
// NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS > 0 it retries transient connectivity
// errors (DB not yet up — common when the app and its database start together)
// with capped backoff up to that budget; recommended for serve/run-worker in
// orchestrated deployments. Default 0 keeps the previous fail-fast behavior, so
// one-shot CLIs and tests don't hang on a down database. Non-connectivity errors
// (bad DSN, migration failure) always fail immediately.
func NewPostgreSQLStore(ctx context.Context) (PostgreSQLStore, error) {
	deadline := time.Now().Add(time.Duration(envInt("NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS", 0)) * time.Second)
	backoff := 500 * time.Millisecond
	for {
		s, err := newPostgreSQLStore(ctx)
		if err == nil {
			return s, nil
		}
		if !isRetryableConnectError(err) || !time.Now().Before(deadline) {
			return nil, err
		}
		slog.Warn("db_connect_retry", "error", err.Error(), "retry_in", backoff.String())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// isRetryableConnectError reports whether err is a transient DB-connectivity
// failure worth retrying (server not up yet / unreachable), as opposed to a
// deterministic error (bad config, migration failure) that retrying won't fix.
func isRetryableConnectError(err error) bool {
	msg := err.Error()
	// "connect: connection reset" is the reset seen while dialling; a PostgreSQL
	// that has begun listening but is not yet serving resets during the startup
	// handshake instead, which surfaces as "read: connection reset by peer" and
	// was not matched here. The load harness died at store init with no report on
	// exactly that, four profiles in a row, looking like a product failure rather
	// than a database that needed another second. The broader substring covers
	// both spellings; this function is only consulted while opening the store, so
	// it cannot mask a reset that happens mid-session.
	for _, s := range []string{"connection refused", "dial error", "no such host", "the database system is starting up", "connection reset"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func newPostgreSQLStore(ctx context.Context) (PostgreSQLStore, error) {
	dsn := getEnv("NXS_ANOMALY_DB_DSN", os.Getenv("ANOMALY_DB_DSN"))
	if dsn == "" {
		host := getEnv("NXS_ANOMALY_DB_HOST", getEnv("ANOMALY_DB_HOST", "localhost"))
		port := getEnv("NXS_ANOMALY_DB_PORT", getEnv("ANOMALY_DB_PORT", "5432"))
		name := getEnv("NXS_ANOMALY_DB_NAME", getEnv("ANOMALY_DB_NAME", "nxs_anomaly"))
		user := getEnv("NXS_ANOMALY_DB_USER", getEnv("ANOMALY_DB_USER", "postgres"))
		pass := getEnv("NXS_ANOMALY_DB_PASSWORD", os.Getenv("ANOMALY_DB_PASSWORD"))
		sslmode := getEnv("NXS_ANOMALY_DB_SSLMODE", "disable")
		dsn = fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=%s",
			host, port, name, user, pass, sslmode)
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	// Pool sizes come from env and are small positive integers, so the int32
	// casts cannot overflow in practice.
	maxConns := envInt("NXS_ANOMALY_DB_POOL_MAX", 10)
	minConns := envInt("NXS_ANOMALY_DB_POOL_MIN", 1)
	config.MaxConns = int32(maxConns) // #nosec G115
	if minConns > 0 && minConns <= maxConns {
		config.MinConns = int32(minConns) // #nosec G115
	}
	// Per-session statement_timeout bounds every query: a stuck or lock-blocked
	// statement is killed instead of hanging a worker cycle or HTTP request. Set
	// as a connection startup parameter so it applies to all pool connections.
	// Migrations disable it per-transaction (SET LOCAL) so a long DDL is exempt.
	if stmtTimeout := envInt("NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS", 30); stmtTimeout > 0 {
		if config.ConnConfig.RuntimeParams == nil {
			config.ConnConfig.RuntimeParams = map[string]string{}
		}
		config.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(stmtTimeout * 1000)
	}
	// Bound connection age/idle and probe health so the pool sheds connections to
	// a failed-over primary or stale balancer endpoint instead of pinning them.
	config.MaxConnLifetime = envDurationSeconds("NXS_ANOMALY_DB_POOL_MAX_CONN_LIFETIME_SECONDS", time.Hour)
	config.MaxConnIdleTime = envDurationSeconds("NXS_ANOMALY_DB_POOL_MAX_CONN_IDLE_SECONDS", 30*time.Minute)
	config.HealthCheckPeriod = envDurationSeconds("NXS_ANOMALY_DB_POOL_HEALTHCHECK_SECONDS", time.Minute)

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	s := &pgStore{pool: pool}
	if err := s.initialize(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return s, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envDurationSeconds(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return def
}

func (s *pgStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *pgStore) PoolStats() PoolStats {
	st := s.pool.Stat()
	return PoolStats{
		AcquiredConns:     st.AcquiredConns(),
		IdleConns:         st.IdleConns(),
		TotalConns:        st.TotalConns(),
		MaxConns:          st.MaxConns(),
		EmptyAcquireCount: st.EmptyAcquireCount(),
	}
}

func (s *pgStore) Close() {
	s.listenMu.Lock()
	s.dropListenerLocked()
	s.listenMu.Unlock()
	s.pool.Close()
}
