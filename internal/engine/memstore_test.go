package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// memStore is an in-memory store.PostgreSQLStore for unit-testing the engine's
// hot paths (delivery / retry / batch round-trips) without PostgreSQL. It models
// collection→id→row storage and a faithful UpdateCollectionsFiltered
// load/mutate/save cycle, including the typed Record collections. Methods the
// tested paths don't exercise are minimal stubs.
type memStore struct {
	mu          sync.Mutex
	data        map[string]map[string]map[string]any
	gotDeadline bool // set per cycle: did ClaimDeliverableNotifications get a ctx deadline
	audit       []store.AuditEvent
	// loads records what each UpdateCollections* call pulled into memory, so
	// tests can assert on the cost of a worker step and not only its result.
	loads []store.LoadSpec
	// metadata stands in for the single nxs_anomaly_metadata row (worker
	// heartbeat, last reported backup, readiness acknowledgement); pingErr lets
	// a test drive the database-down branch of readiness.
	metadata map[string]any
	pingErr  error
}

// GetMetadata returns a copy of the deployment metadata document.
func (m *memStore) GetMetadata(context.Context) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]any{}
	for k, v := range m.metadata {
		out[k] = v
	}
	return out, nil
}

// PatchMetadata merges patch into the document, deleting keys set to nil.
func (m *memStore) PatchMetadata(_ context.Context, patch map[string]any) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.metadata == nil {
		m.metadata = map[string]any{}
	}
	for k, v := range patch {
		if v == nil {
			delete(m.metadata, k)
			continue
		}
		m.metadata[k] = v
	}
	out := map[string]any{}
	for k, v := range m.metadata {
		out[k] = v
	}
	return out, nil
}

// resetLoadLog clears the record of loaded collections.
func (m *memStore) resetLoadLog() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads = nil
}

// loadSpecs returns the load specs seen since the last reset.
func (m *memStore) loadSpecs() []store.LoadSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.LoadSpec(nil), m.loads...)
}

// loadedCollections returns the distinct collection names loaded since reset.
func (m *memStore) loadedCollections() []string {
	seen := map[string]bool{}
	var out []string
	for _, spec := range m.loadSpecs() {
		if !seen[spec.Collection] {
			seen[spec.Collection] = true
			out = append(out, spec.Collection)
		}
	}
	return out
}

var _ store.PostgreSQLStore = (*memStore)(nil)

func newMemStore() *memStore {
	return &memStore{data: map[string]map[string]map[string]any{}}
}

func (m *memStore) seed(collection string, rows ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[collection] == nil {
		m.data[collection] = map[string]map[string]any{}
	}
	for _, r := range rows {
		m.data[collection][r["id"].(string)] = deepCopyItem(r)
	}
}

// row returns a copy of a stored row (nil if absent).
func (m *memStore) row(collection, id string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.data[collection][id]; ok {
		return deepCopyItem(r)
	}
	return nil
}

func (m *memStore) count(collection string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.data[collection])
}

func memMatch(row, filters map[string]any) bool {
	for k, want := range filters {
		got := row[k]
		switch w := want.(type) {
		case []any:
			found := false
			for _, v := range w {
				if v == got {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		case store.NotEqualFilter:
			if got == w.Value {
				return false
			}
		default:
			if got != want {
				return false
			}
		}
	}
	return true
}

func recordToMap(r store.Record) map[string]any {
	b, _ := r.MarshalData()
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// UpdateCollectionsWriteAll delegates to UpdateCollectionsFiltered: the fake has
// no baseline/diff, so the writeAll hint is a no-op and behavior is identical.
func (m *memStore) UpdateCollectionsWriteAll(ctx context.Context, loads []store.LoadSpec, save, _ []string, mutator func(*store.State) (any, error), lockKey int64) (any, error) {
	return m.UpdateCollectionsFiltered(ctx, loads, save, mutator, lockKey)
}

func (m *memStore) UpdateCollectionsFiltered(ctx context.Context, loads []store.LoadSpec, save []string, mutator func(*store.State) (any, error), _ int64) (any, error) {
	state := store.NewState()

	m.mu.Lock()
	m.loads = append(m.loads, loads...)
	for _, spec := range loads {
		generic := map[string]map[string]any{}
		for id, row := range m.data[spec.Collection] {
			if !memMatch(row, spec.Filters) {
				continue
			}
			cp := deepCopyItem(row)
			switch spec.Collection {
			case "alerts":
				state.Alerts[id] = model.WrapAlert(cp)
			case "alert_groups":
				state.AlertGroups[id] = model.WrapAlertGroup(cp)
			case "notifications":
				state.Notifications[id] = model.WrapNotification(cp)
			case "notification_batches":
				state.NotificationBatches[id] = model.WrapNotificationBatch(cp)
			default:
				generic[id] = cp
			}
		}
		switch spec.Collection {
		case "alerts", "alert_groups", "notifications", "notification_batches":
		default:
			state.SetCollection(spec.Collection, generic)
		}
	}
	m.mu.Unlock()

	result, err := mutator(state)
	if err != nil {
		return nil, err
	}

	// Mirror the real store's transactional audit flush.
	for _, ev := range state.AuditEvents {
		_ = m.InsertAuditEvent(ctx, ev)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, col := range save {
		if m.data[col] == nil {
			m.data[col] = map[string]map[string]any{}
		}
		switch col {
		case "alerts", "alert_groups", "notifications", "notification_batches":
			for id, rec := range stateRecords(state, col) {
				m.data[col][id] = recordToMap(rec)
			}
		default:
			for id, row := range state.GetCollection(col) {
				m.data[col][id] = row
			}
		}
	}
	return result, nil
}

func stateRecords(s *store.State, col string) map[string]store.Record {
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
	return nil
}

func (m *memStore) UpdateCollections(ctx context.Context, load, save []string, mutator func(*store.State) (any, error), lock int64) (any, error) {
	specs := make([]store.LoadSpec, len(load))
	for i, c := range load {
		specs[i] = store.LoadSpec{Collection: c}
	}
	return m.UpdateCollectionsFiltered(ctx, specs, save, mutator, lock)
}

func (m *memStore) where(col string, pred func(map[string]any) bool) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []map[string]any
	for _, r := range m.data[col] {
		if pred(r) {
			out = append(out, deepCopyItem(r))
		}
	}
	return out
}

// claim atomically transitions matching rows to claimStatus (recording
// claimed_at/claimed_by) and returns copies of the claimed rows — the in-memory
// analogue of the SQL FOR UPDATE SKIP LOCKED claim.
func (m *memStore) claim(claimStatus, nowISO, workerID string, pred func(map[string]any) bool) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []map[string]any
	for _, r := range m.data["notifications"] {
		if !pred(r) {
			continue
		}
		r["status"] = claimStatus
		r["claimed_at"] = nowISO
		r["claimed_by"] = workerID
		out = append(out, deepCopyItem(r))
	}
	return out
}

func (m *memStore) ClaimDeliverableNotifications(ctx context.Context, workerID, nowISO string) ([]map[string]any, error) {
	_, hasDeadline := ctx.Deadline()
	m.mu.Lock()
	m.gotDeadline = hasDeadline
	m.mu.Unlock()
	return m.claim(model.NotificationDelivering, nowISO, workerID, func(r map[string]any) bool {
		return r["status"] == model.NotificationDeliveryScheduled
	}), nil
}

func (m *memStore) ClaimRetryableNotifications(_ context.Context, workerID, nowISO string) ([]map[string]any, error) {
	return m.claim(model.NotificationRetrying, nowISO, workerID, func(r map[string]any) bool {
		nr, _ := r["next_retry_at"].(string)
		return r["status"] == model.NotificationRetryScheduled && nr != "" && nr <= nowISO
	}), nil
}

func (m *memStore) ReclaimStaleClaims(_ context.Context, cutoffISO string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.data["notifications"] {
		st, _ := r["status"].(string)
		ca, _ := r["claimed_at"].(string)
		if (st != model.NotificationDelivering && st != model.NotificationRetrying) || ca == "" || ca >= cutoffISO {
			continue
		}
		if st == model.NotificationDelivering {
			r["status"] = model.NotificationDeliveryScheduled
		} else {
			r["status"] = model.NotificationRetryScheduled
		}
		n++
	}
	return n, nil
}

func (m *memStore) ListDueNotificationBatches(_ context.Context, nowISO string) ([]map[string]any, error) {
	return m.where("notification_batches", func(r map[string]any) bool {
		fa, _ := r["flush_at"].(string)
		return r["status"] == "open" && fa != "" && fa <= nowISO
	}), nil
}

func (m *memStore) ListItemsIn(_ context.Context, col, field string, values []any) ([]map[string]any, error) {
	return m.where(col, func(r map[string]any) bool {
		for _, v := range values {
			if r[field] == v {
				return true
			}
		}
		return false
	}), nil
}

func (m *memStore) PageUnresolvedAlertGroups(_ context.Context, hidden []string, limit, offset int) ([]map[string]any, int, error) {
	hide := make(map[string]bool, len(hidden))
	for _, id := range hidden {
		hide[id] = true
	}
	rows := m.where("alert_groups", func(r map[string]any) bool {
		if r["status"] == "resolved" {
			return false
		}
		integ, _ := r["integration_id"].(string)
		return integ == "" || !hide[integ]
	})
	sort.SliceStable(rows, func(i, j int) bool {
		a, _ := rows[i]["last_received_at"].(string)
		b, _ := rows[j]["last_received_at"].(string)
		if a != b {
			return a > b
		}
		ai, _ := rows[i]["id"].(string)
		bi, _ := rows[j]["id"].(string)
		return ai < bi
	})
	total := len(rows)
	if offset > total {
		offset = total
	}
	rows = rows[offset:]
	if limit < len(rows) {
		rows = rows[:limit]
	}
	return rows, total, nil
}

func (m *memStore) ListUnresolvedAlertGroups(_ context.Context, field string, values []any) ([]map[string]any, error) {
	return m.where("alert_groups", func(r map[string]any) bool {
		if r["status"] == "resolved" {
			return false
		}
		for _, v := range values {
			if r[field] == v {
				return true
			}
		}
		return false
	}), nil
}

func (m *memStore) ListItemsByIDs(_ context.Context, col string, ids []string) ([]map[string]any, error) {
	idset := make(map[string]bool, len(ids))
	for _, id := range ids {
		idset[id] = true
	}
	return m.where(col, func(r map[string]any) bool {
		id, _ := r["id"].(string)
		return idset[id]
	}), nil
}

func (m *memStore) GetItem(_ context.Context, col, id string) (map[string]any, error) {
	return m.row(col, id), nil
}

func (m *memStore) UpsertItem(_ context.Context, col string, item map[string]any) error {
	m.seed(col, item)
	return nil
}

// --- unused-by-tested-paths stubs ---

// ReadCollections mirrors the load half of UpdateCollectionsFiltered, so read
// paths (schedule on-call and preview) see the rows the write paths stored.
func (m *memStore) ReadCollections(_ context.Context, collections []string) (*store.State, error) {
	state := store.NewState()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, col := range collections {
		switch col {
		case "alerts", "alert_groups", "notifications", "notification_batches":
			for id, row := range m.data[col] {
				cp := deepCopyItem(row)
				switch col {
				case "alerts":
					state.Alerts[id] = model.WrapAlert(cp)
				case "alert_groups":
					state.AlertGroups[id] = model.WrapAlertGroup(cp)
				case "notifications":
					state.Notifications[id] = model.WrapNotification(cp)
				case "notification_batches":
					state.NotificationBatches[id] = model.WrapNotificationBatch(cp)
				}
			}
		default:
			generic := map[string]map[string]any{}
			for id, row := range m.data[col] {
				generic[id] = deepCopyItem(row)
			}
			state.SetCollection(col, generic)
		}
	}
	return state, nil
}

// ListCollection returns every row of a collection, like the real store. It
// was a nil stub until reference-cache reads (schedule coverage, shift
// handoffs) started going through it.
func (m *memStore) ListCollection(_ context.Context, collection string) ([]map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, 0, len(m.data[collection]))
	for _, row := range m.data[collection] {
		out = append(out, deepCopyItem(row))
	}
	return out, nil
}

// DeleteItem removes the row and returns it, like the real store: engine code
// distinguishes "deleted" from "was not there" by the returned row.
func (m *memStore) DeleteItem(_ context.Context, collection, id string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.data[collection][id]
	if !ok {
		return nil, nil
	}
	delete(m.data[collection], id)
	return deepCopyItem(row), nil
}
func (m *memStore) SoftDeleteItem(context.Context, string, string) (map[string]any, error) {
	return nil, nil
}
func (m *memStore) UpsertItemAudited(ctx context.Context, col string, item map[string]any, ev store.AuditEvent) error {
	if err := m.UpsertItem(ctx, col, item); err != nil {
		return err
	}
	return m.InsertAuditEvent(ctx, ev)
}
func (m *memStore) DeleteItemAudited(ctx context.Context, col, id string, ev store.AuditEvent) (map[string]any, error) {
	item, err := m.DeleteItem(ctx, col, id)
	if err != nil || item == nil {
		return item, err
	}
	return item, m.InsertAuditEvent(ctx, ev)
}
func (m *memStore) SoftDeleteItemAudited(ctx context.Context, col, id string, ev store.AuditEvent) (map[string]any, error) {
	item, err := m.SoftDeleteItem(ctx, col, id)
	if err != nil || item == nil {
		return item, err
	}
	return item, m.InsertAuditEvent(ctx, ev)
}
func (m *memStore) CountCollection(context.Context, string, map[string]any) (int, error) {
	return 0, nil
}
func (m *memStore) CollectionsHaveAny(context.Context, []string) (bool, error) { return false, nil }
func (m *memStore) ClearCollections(context.Context, []string) error           { return nil }

// FindIntegrationByKey mirrors the real lookup: match key or routing_key, skip
// soft-deleted rows. It was a nil stub until the heartbeat tests needed ingest
// to actually find an integration — and a stub that answers "no such
// integration" makes every ingest-driven test agree with itself while proving
// nothing.
func (m *memStore) FindIntegrationByKey(_ context.Context, key string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.data["integrations"] {
		if utils.StrVal(row, "deleted_at") != "" {
			continue
		}
		if utils.StrVal(row, "key") == key || utils.StrVal(row, "routing_key") == key {
			return deepCopyItem(row), nil
		}
	}
	return nil, nil
}

// LastAlertReceivedAt mirrors the SQL: the newest received_at among that
// integration's alerts. Modelled rather than stubbed, because a stub answering
// "never" would let a heartbeat test pass whatever the code decided.
func (m *memStore) LastAlertReceivedAt(_ context.Context, integrationID string) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var newest time.Time
	found := false
	for _, row := range m.data["alerts"] {
		if utils.StrVal(row, "integration_id") != integrationID {
			continue
		}
		if labels, _ := row["labels"].(map[string]any); utils.StrVal(labels, "alertname") == "SourceSilent" &&
			utils.StrVal(labels, "integration_id") == integrationID {
			continue
		}
		at, err := utils.ParseDatetime(utils.StrVal(row, "received_at"))
		if err != nil {
			continue
		}
		if !found || at.After(newest) {
			newest, found = at, true
		}
	}
	return newest, found, nil
}

// FindMobileSessionByToken mirrors the real lookup: match on token hash, skip
// revoked and expired sessions. It was a nil stub until a test needed to prove that a
// mobile action is attributed to the session's user.
func (m *memStore) FindMobileSessionByToken(_ context.Context, token string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.data["mobile_sessions"] {
		if row["token"] != token {
			continue
		}
		if revoked, ok := row["revoked_at"]; ok && revoked != nil {
			continue
		}
		if exp, err := time.Parse(time.RFC3339, fmt.Sprint(row["expires_at"])); err != nil || !exp.After(time.Now()) {
			continue
		}
		return row, nil
	}
	return nil, nil
}
func (m *memStore) CreateMobilePairingCode(context.Context, string, string, time.Time) error {
	return nil
}
func (m *memStore) RedeemMobilePairingCode(context.Context, string) (string, error) {
	return "", nil
}
func (m *memStore) FindActiveAlertGroup(context.Context, string, string) (map[string]any, error) {
	return nil, nil
}
func (m *memStore) ListDueAlertGroups(context.Context, string) ([]map[string]any, error) {
	return nil, nil
}
func (m *memStore) OldestDueEscalationAgeSeconds(context.Context) (float64, error)   { return 0, nil }
func (m *memStore) OldestPendingDeliveryAgeSeconds(context.Context) (float64, error) { return 0, nil }

// memSortKey renders a field the way the SQL orders it. Severity is ranked
// rather than compared as text, matching store.orderClause — alphabetically
// "critical" would sort between "alert" and "debug".
func memSortKey(row map[string]any, field string) string {
	v := utils.StrVal(row, field)
	if field == "severity" && v != "" {
		return fmt.Sprintf("%02d", store.SeverityRank(v))
	}
	return v
}

// ListCollectionPage mirrors the real store closely enough for read-path tests:
// filter, order (the collection's default, or the requested column, with id as
// the tie-break), then apply offset/limit. It was a nil stub until read-path
// redaction needed rows to come back.
func (m *memStore) ListCollectionPage(_ context.Context, collection string, filters map[string]any, limit, offset int, spec store.SortSpec) ([]map[string]any, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	spec, err := store.ResolveSort(collection, spec)
	if err != nil {
		return nil, 0, err
	}
	var matched []map[string]any
	for _, row := range m.data[collection] {
		if memMatch(row, filters) {
			matched = append(matched, deepCopyItem(row))
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if spec.Field != "" && spec.Field != "id" {
			a, b := memSortKey(matched[i], spec.Field), memSortKey(matched[j], spec.Field)
			if a == b && spec.Field == "severity" {
				// Newest first within a severity, as store.orderClause does.
				tie := store.SeverityTieBreak(collection)
				if ta, tb := utils.StrVal(matched[i], tie), utils.StrVal(matched[j], tie); ta != tb {
					return ta > tb
				}
			}
			if a != b {
				// Absent sorts last either way, matching NULLS LAST.
				if a == "" || b == "" {
					return b == ""
				}
				if spec.Desc {
					return a > b
				}
				return a < b
			}
		}
		if spec.Desc {
			return utils.StrVal(matched[i], "id") > utils.StrVal(matched[j], "id")
		}
		return utils.StrVal(matched[i], "id") < utils.StrVal(matched[j], "id")
	})
	total := len(matched)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	return matched[offset:end], total, nil
}
func (m *memStore) QueryHistoryGroups(context.Context, map[string]any, int, int) ([]map[string]any, int, error) {
	return nil, 0, nil
}
func (m *memStore) InsightsSummaryQuery(context.Context, string, []string, time.Time, time.Time) (store.InsightsSummary, error) {
	return store.InsightsSummary{}, nil
}
func (m *memStore) DeleteOldResolvedGroups(context.Context, string) (int, error) { return 0, nil }

// SetAlertStatusForGroups is modelled rather than stubbed: whether a closed
// group's alerts stop reading "firing" is what the tests around it assert.
func (m *memStore) SetAlertStatusForGroups(_ context.Context, groupIDs []string, status string) (int, error) {
	if len(groupIDs) == 0 || status == "" {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	wanted := make(map[string]bool, len(groupIDs))
	for _, id := range groupIDs {
		wanted[id] = true
	}
	n := 0
	for id, row := range m.data["alerts"] {
		if !wanted[utils.StrVal(row, "alert_group_id")] || utils.StrVal(row, "status") == status {
			continue
		}
		updated := deepCopyItem(row)
		updated["status"] = status
		m.data["alerts"][id] = updated
		n++
	}
	return n, nil
}

// audit records everything the engine writes to the trail, so tests can assert
// on attribution instead of only on the returned value.
func (m *memStore) InsertAuditEvent(_ context.Context, ev store.AuditEvent) error {
	m.audit = append(m.audit, ev)
	return nil
}

func (m *memStore) ListAuditEvents(context.Context, map[string]any, int, int) ([]map[string]any, int, error) {
	return nil, 0, nil
}
func (m *memStore) PruneAuditEvents(context.Context, string) (int, error) { return 0, nil }

// Rate buckets: the engine only ever prunes them; the limiter itself lives in
// the server package and is tested there against storetest.
func (m *memStore) ConsumeRateToken(context.Context, string, float64, float64) (bool, error) {
	return true, nil
}
func (m *memStore) RefundRateToken(context.Context, string, float64) error { return nil }
func (m *memStore) PruneRateBuckets(context.Context, string) (int, error)  { return 0, nil }

// Identity tables. The engine never touches credentials or sessions — that is
// entirely the HTTP layer's business — so these are stubs here. The faithful
// in-memory model lives in internal/storetest, which the server tests use.
func (m *memStore) SetUserPassword(context.Context, string, string) error { return nil }
func (m *memStore) GetUserPasswordHash(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (m *memStore) DeleteUserPassword(context.Context, string) error    { return nil }
func (m *memStore) CountUsersWithPassword(context.Context) (int, error) { return 0, nil }
func (m *memStore) FindUserByLogin(context.Context, string) (map[string]any, error) {
	return nil, nil
}
func (m *memStore) ListTeamIDsForUser(context.Context, string) ([]string, error) {
	return nil, nil
}
func (m *memStore) CreateWebSession(context.Context, store.WebSession) error { return nil }
func (m *memStore) FindSessionUser(context.Context, string) (map[string]any, string, error) {
	return nil, "", nil
}
func (m *memStore) ListWebSessions(context.Context, string) ([]store.WebSession, error) {
	return nil, nil
}
func (m *memStore) RevokeWebSessionByID(context.Context, string, string) (bool, error) {
	return false, nil
}
func (m *memStore) RevokeWebSession(context.Context, string) error          { return nil }
func (m *memStore) RevokeUserSessions(context.Context, string) (int, error) { return 0, nil }
func (m *memStore) DeleteExpiredWebSessions(context.Context) (int, error)   { return 0, nil }

func (m *memStore) InsertVerificationToken(context.Context, string, string, time.Time) error {
	return nil
}
func (m *memStore) FindVerificationToken(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (m *memStore) DeleteVerificationToken(context.Context, string) error { return nil }
func (m *memStore) CleanExpiredVerificationTokens(context.Context) error  { return nil }
func (m *memStore) ListKafkaOutboxEvents(context.Context, int) ([]map[string]any, error) {
	return nil, nil
}
func (m *memStore) DeleteKafkaOutboxEvents(context.Context, []string) error { return nil }
func (m *memStore) CountKafkaOutboxByTopic(context.Context) (map[string]int, error) {
	return nil, nil
}
func (m *memStore) DeleteOldChatopsMessages(context.Context, string) (int, error) { return 0, nil }
func (m *memStore) DeleteOldNotifications(context.Context, string) (int, error)   { return 0, nil }

// memStore keeps no session table; the erasure tests that care use
// storetest.Store, which models one.
func (m *memStore) DeleteUserWebSessions(context.Context, string) (int, error) { return 0, nil }

// ScrubUserNotificationTargets is modelled for real: the rows stay, the address
// on them is replaced. A stub returning 0 would let an erasure that deletes the
// paging history pass.
func (m *memStore) ScrubUserNotificationTargets(_ context.Context, userID, replacement string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	scrubbed := map[string]bool{}
	for id, row := range m.data["notifications"] {
		if uid, _ := row["user_id"].(string); uid != userID {
			continue
		}
		scrubbed[id] = true
		if target, _ := row["target"].(string); target != replacement {
			row["target"] = replacement
			n++
		}
	}
	for _, att := range m.data["notification_delivery_attempts"] {
		nid, _ := att["notification_id"].(string)
		if !scrubbed[nid] {
			continue
		}
		if target, _ := att["target"].(string); target != replacement {
			att["target"] = replacement
			n++
		}
	}
	return n, nil
}
func (m *memStore) DeleteOldDeliveryAttempts(context.Context, string) (int, error) { return 0, nil }
func (m *memStore) DeleteOldWebSessions(context.Context, string) (int, error)      { return 0, nil }
func (m *memStore) PseudonymiseAuditActor(context.Context, string, string) (int, error) {
	return 0, nil
}
func (m *memStore) TryAdvisoryLock(context.Context, int64) (bool, func(), error) {
	return true, func() {}, nil
}
func (m *memStore) NotifyWake(context.Context) error                { return nil }
func (m *memStore) WaitForWake(context.Context, time.Duration) bool { return false }
func (m *memStore) PoolStats() store.PoolStats                      { return store.PoolStats{} }
func (m *memStore) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pingErr
}
func (m *memStore) Close() {}

// adminCtx is the actor context the engine tests run mutators under. It lives
// here, with the rest of the shared test scaffolding, rather than beside the
// first test that happened to need it: several test files across both editions
// use it, and a helper that ships in only one of them takes the other build's
// tests down with it.
func adminCtx() context.Context {
	return authz.NewContext(context.Background(), authz.Actor{
		ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin,
	})
}
