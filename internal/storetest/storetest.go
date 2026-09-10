// Package storetest provides an in-memory store.PostgreSQLStore for unit tests
// that need a real *engine.Engine (or *server.Server) without PostgreSQL.
//
// It models collection→id→row storage and a faithful UpdateCollectionsFiltered
// load/mutate/save cycle, including the typed Record collections (alerts,
// alert_groups, notifications, notification_batches). ListCollectionPage and
// CountCollection are implemented for real so HTTP list/count handlers return
// meaningful data; methods the tested paths don't exercise are minimal stubs.
//
// It deliberately depends only on internal/store and internal/model (no engine
// import), so it is reusable across packages. It mirrors the engine package's
// internal memStore test double, which cannot be shared because it lives in a
// _test.go file and leans on engine-internal helpers.
package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
)

// Store is an in-memory store.PostgreSQLStore.
type Store struct {
	mu   sync.Mutex
	data map[string]map[string]map[string]any
	// audit is append-only, like the table it stands in for, so tests can assert
	// on what was recorded instead of only on what was returned.
	audit []store.AuditEvent
	// creds and sessions stand in for the two identity tables. They are modelled
	// for real rather than stubbed: sign-in, role revocation and session
	// invalidation are exactly the behaviours the tests exist to pin down.
	creds    map[string]string
	sessions map[string]*store.WebSession // keyed by token hash
	revoked  map[string]bool              // token hashes signed out
	// metadata stands in for the single nxs_anomaly_metadata row; pingErr lets a
	// test drive the database-down branch of readiness.
	metadata map[string]any
	pingErr  error
	// buckets models the cluster-wide token buckets for real, so a test can
	// exhaust the sign-in limit and see the same answer the SQL would give.
	buckets map[string]*rateBucket
}

type rateBucket struct {
	tokens    float64
	updatedAt time.Time
}

var _ store.PostgreSQLStore = (*Store)(nil)

// New returns an empty in-memory store.
func New() *Store {
	return &Store{
		data:     map[string]map[string]map[string]any{},
		creds:    map[string]string{},
		metadata: map[string]any{},
		sessions: map[string]*store.WebSession{},
		revoked:  map[string]bool{},
		buckets:  map[string]*rateBucket{},
	}
}

// Seed inserts rows (deep-copied) into a collection. Each row must carry an "id".
func (m *Store) Seed(collection string, rows ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[collection] == nil {
		m.data[collection] = map[string]map[string]any{}
	}
	for _, r := range rows {
		m.data[collection][r["id"].(string)] = deepCopy(r)
	}
}

// Row returns a copy of a stored row (nil if absent).
func (m *Store) Row(collection, id string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.data[collection][id]; ok {
		return deepCopy(r)
	}
	return nil
}

// Count returns the number of rows in a collection.
func (m *Store) Count(collection string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.data[collection])
}

// deepCopy structurally clones a jsonb-shaped value, preserving Go types
// (unlike a JSON round-trip, which would turn ints into float64).
func deepCopy(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return deepCopyValue(m).(map[string]any)
}

func deepCopyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopyValue(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = deepCopyValue(val)
		}
		return out
	default:
		return v
	}
}

func match(row, filters map[string]any) bool {
	for k, want := range filters {
		got := row[k]
		// Mirrors buildWhere: a severity filter names a level, so it matches
		// every spelling of that level. A double that matched only the exact
		// word would let a filter regression pass here and fail in production.
		if k == "severity" {
			if raw, ok := want.(string); ok {
				want = store.SeverityFamily(raw)
			}
		}
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
		case store.InOrNullFilter:
			// Mirrors the SQL: an unset column counts as NULL and matches.
			if got == nil || got == "" {
				continue
			}
			found := false
			for _, v := range w.Values {
				if v == got {
					found = true
					break
				}
			}
			if !found {
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
	var mm map[string]any
	_ = json.Unmarshal(b, &mm)
	return mm
}

// ── transactional mutation ────────────────────────────────────────────────────

func (m *Store) UpdateCollectionsWriteAll(ctx context.Context, loads []store.LoadSpec, save, _ []string, mutator func(*store.State) (any, error), lockKey int64) (any, error) {
	return m.UpdateCollectionsFiltered(ctx, loads, save, mutator, lockKey)
}

func (m *Store) UpdateCollectionsFiltered(ctx context.Context, loads []store.LoadSpec, save []string, mutator func(*store.State) (any, error), _ int64) (any, error) {
	state := store.NewState()

	m.mu.Lock()
	for _, spec := range loads {
		generic := map[string]map[string]any{}
		for id, row := range m.data[spec.Collection] {
			if !match(row, spec.Filters) {
				continue
			}
			cp := deepCopy(row)
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

	// Mirror the real store: audit events the mutator enrolled are written with
	// the same operation (here, before the save lock to avoid re-locking).
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

func (m *Store) UpdateCollections(ctx context.Context, load, save []string, mutator func(*store.State) (any, error), lock int64) (any, error) {
	specs := make([]store.LoadSpec, len(load))
	for i, c := range load {
		specs[i] = store.LoadSpec{Collection: c}
	}
	return m.UpdateCollectionsFiltered(ctx, specs, save, mutator, lock)
}

// ── reads ─────────────────────────────────────────────────────────────────────

func (m *Store) where(col string, pred func(map[string]any) bool) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []map[string]any
	for _, r := range m.data[col] {
		if pred(r) {
			out = append(out, deepCopy(r))
		}
	}
	return out
}

func (m *Store) GetItem(_ context.Context, col, id string) (map[string]any, error) {
	return m.Row(col, id), nil
}

func (m *Store) ListCollection(_ context.Context, col string) ([]map[string]any, error) {
	return m.sortedByID(col, nil), nil
}

// sortedByID returns rows matching filters, deterministically ordered by id.
func (m *Store) sortedByID(col string, filters map[string]any) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.data[col]))
	for id, row := range m.data[col] {
		if match(row, filters) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, deepCopy(m.data[col][id]))
	}
	return out
}

// sortedRows applies the same ordering contract as the PostgreSQL store: the
// collection's default when nothing is asked for, a validated column otherwise,
// and id as the tie-break so a paged listing cannot show one row twice.
func (m *Store) sortedRows(col string, filters map[string]any, spec store.SortSpec) ([]map[string]any, error) {
	spec, err := store.ResolveSort(col, spec)
	if err != nil {
		return nil, err
	}
	rows := m.sortedByID(col, filters)
	if spec.Field == "" || spec.Field == "id" {
		if spec.Desc {
			for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
		return rows, nil
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := sortKey(rows[i], spec.Field), sortKey(rows[j], spec.Field)
		if a == b {
			return false // sortedByID already ordered them; SliceStable keeps that
		}
		// A row without the field sorts last in both directions, matching
		// NULLS LAST.
		if a == "" || b == "" {
			return b == ""
		}
		if spec.Desc {
			return a > b
		}
		return a < b
	})
	return rows, nil
}

// sortKey renders a field as the string the comparison runs on. ISO timestamps
// and ids compare correctly as text; numbers are padded so 9 sorts below 10.
func sortKey(row map[string]any, field string) string {
	// Severity is ranked, not spelled: see store.orderClause for why.
	if field == "severity" {
		if s, _ := row[field].(string); s != "" {
			return fmt.Sprintf("%02d", store.SeverityRank(s))
		}
		return ""
	}
	switch v := row[field].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return fmt.Sprintf("%020.4f", v)
	case int:
		return fmt.Sprintf("%020.4f", float64(v))
	case bool:
		if v {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (m *Store) ListCollectionPage(_ context.Context, col string, filters map[string]any, limit, offset int, sortSpec store.SortSpec) ([]map[string]any, int, error) {
	rows, err := m.sortedRows(col, filters, sortSpec)
	if err != nil {
		return nil, 0, err
	}
	total := len(rows)
	if offset > total {
		offset = total
	}
	rows = rows[offset:]
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	if len(rows) == 0 {
		// Mirror the PostgreSQL store: scanRows returns a *nil* slice when the
		// query matched nothing, and encoding/json renders nil as `null`. A
		// double that hands back an empty non-nil slice hides that difference,
		// so a test can pass while the real API answers `"items": null`.
		return nil, total, nil
	}
	return rows, total, nil
}

func (m *Store) CountCollection(_ context.Context, col string, filters map[string]any) (int, error) {
	return len(m.sortedByID(col, filters)), nil
}

func (m *Store) UpsertItem(_ context.Context, col string, item map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[col] == nil {
		m.data[col] = map[string]map[string]any{}
	}
	id, _ := item["id"].(string)
	m.data[col][id] = deepCopy(item)
	return nil
}

func (m *Store) DeleteItem(_ context.Context, col, id string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.data[col][id]
	if !ok {
		return nil, nil
	}
	cp := deepCopy(row)
	delete(m.data[col], id)
	return cp, nil
}

func (m *Store) SoftDeleteItem(ctx context.Context, col, id string) (map[string]any, error) {
	return m.DeleteItem(ctx, col, id)
}

func (m *Store) UpsertItemAudited(ctx context.Context, col string, item map[string]any, ev store.AuditEvent) error {
	if err := m.UpsertItem(ctx, col, item); err != nil {
		return err
	}
	return m.InsertAuditEvent(ctx, ev)
}

func (m *Store) DeleteItemAudited(ctx context.Context, col, id string, ev store.AuditEvent) (map[string]any, error) {
	item, err := m.DeleteItem(ctx, col, id)
	if err != nil || item == nil {
		return item, err
	}
	return item, m.InsertAuditEvent(ctx, ev)
}

func (m *Store) SoftDeleteItemAudited(ctx context.Context, col, id string, ev store.AuditEvent) (map[string]any, error) {
	item, err := m.SoftDeleteItem(ctx, col, id)
	if err != nil || item == nil {
		return item, err
	}
	return item, m.InsertAuditEvent(ctx, ev)
}

// LastAlertReceivedAt mirrors the SQL: the newest received_at among the
// integration's alerts, and false when it has none. Modelled rather than
// stubbed — a stub returning "never" would make every heartbeat test agree with
// itself no matter what the code did.
func (m *Store) LastAlertReceivedAt(_ context.Context, integrationID string) (time.Time, bool, error) {
	var newest time.Time
	found := false
	for _, r := range m.where("alerts", func(r map[string]any) bool {
		return r["integration_id"] == integrationID
	}) {
		raw, _ := r["received_at"].(string)
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			continue
		}
		if !found || at.After(newest) {
			newest, found = at, true
		}
	}
	return newest, found, nil
}

func (m *Store) FindIntegrationByKey(_ context.Context, key string) (map[string]any, error) {
	rows := m.where("integrations", func(r map[string]any) bool {
		return r["key"] == key || r["routing_key"] == key
	})
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func (m *Store) FindMobileSessionByToken(_ context.Context, token string) (map[string]any, error) {
	rows := m.where("mobile_sessions", func(r map[string]any) bool {
		return r["token"] == token && r["revoked_at"] == nil
	})
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func (m *Store) FindActiveAlertGroup(_ context.Context, integrationID, dedupeKey string) (map[string]any, error) {
	rows := m.where("alert_groups", func(r map[string]any) bool {
		return r["integration_id"] == integrationID && r["dedupe_key"] == dedupeKey && r["status"] != "resolved"
	})
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func (m *Store) ListDueAlertGroups(_ context.Context, nowISO string) ([]map[string]any, error) {
	return m.where("alert_groups", func(r map[string]any) bool {
		nr, _ := r["next_run_at"].(string)
		return r["status"] == "open" && nr != "" && nr <= nowISO
	}), nil
}

func (m *Store) ListDueNotificationBatches(_ context.Context, nowISO string) ([]map[string]any, error) {
	return m.where("notification_batches", func(r map[string]any) bool {
		fa, _ := r["flush_at"].(string)
		return r["status"] == "open" && fa != "" && fa <= nowISO
	}), nil
}

func (m *Store) ListItemsIn(_ context.Context, col, field string, values []any) ([]map[string]any, error) {
	return m.where(col, func(r map[string]any) bool {
		for _, v := range values {
			if r[field] == v {
				return true
			}
		}
		return false
	}), nil
}

func (m *Store) ListItemsByIDs(_ context.Context, col string, ids []string) ([]map[string]any, error) {
	idset := make(map[string]bool, len(ids))
	for _, id := range ids {
		idset[id] = true
	}
	return m.where(col, func(r map[string]any) bool {
		id, _ := r["id"].(string)
		return idset[id]
	}), nil
}

func (m *Store) ReadCollections(_ context.Context, cols []string) (*store.State, error) {
	state := store.NewState()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, col := range cols {
		generic := map[string]map[string]any{}
		for id, row := range m.data[col] {
			generic[id] = deepCopy(row)
		}
		switch col {
		case "alerts", "alert_groups", "notifications", "notification_batches":
			// Typed collections are not needed by current ReadCollections callers
			// in handler tests; left unmodeled to keep the fake simple.
		default:
			state.SetCollection(col, generic)
		}
	}
	return state, nil
}

// ── notification claim/retry (faithful to the SQL claim semantics) ─────────────

func (m *Store) claim(claimStatus, nowISO, workerID string, pred func(map[string]any) bool) []map[string]any {
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
		out = append(out, deepCopy(r))
	}
	return out
}

func (m *Store) ClaimDeliverableNotifications(_ context.Context, workerID, nowISO string) ([]map[string]any, error) {
	return m.claim(model.NotificationDelivering, nowISO, workerID, func(r map[string]any) bool {
		return r["status"] == model.NotificationDeliveryScheduled
	}), nil
}

func (m *Store) ClaimRetryableNotifications(_ context.Context, workerID, nowISO string) ([]map[string]any, error) {
	return m.claim(model.NotificationRetrying, nowISO, workerID, func(r map[string]any) bool {
		nr, _ := r["next_retry_at"].(string)
		return r["status"] == model.NotificationRetryScheduled && nr != "" && nr <= nowISO
	}), nil
}

func (m *Store) ReclaimStaleClaims(_ context.Context, cutoffISO string) (int, error) {
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

// ── stubs (not exercised by handler tests) ─────────────────────────────────────

func (m *Store) CollectionsHaveAny(_ context.Context, cols []string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range cols {
		if len(m.data[c]) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (m *Store) ClearCollections(_ context.Context, cols []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range cols {
		delete(m.data, c)
	}
	return nil
}

func (m *Store) OldestDueEscalationAgeSeconds(context.Context) (float64, error)   { return 0, nil }
func (m *Store) OldestPendingDeliveryAgeSeconds(context.Context) (float64, error) { return 0, nil }

// InsightsSummaryQuery counts what the in-memory rows say, in the same shapes the
// SQL returns: by status, by severity *level* (so "high" lands under "error",
// which is the property the screen depends on), and per day.
func (m *Store) InsightsSummaryQuery(_ context.Context, integrationID string, integrationIDs []string, from, to time.Time) (store.InsightsSummary, error) {
	out := store.InsightsSummary{
		GroupsByStatus:       map[string]int{},
		GroupsByLevel:        map[string]int{},
		NotificationsByState: map[string]int{},
	}
	allowed := map[string]bool{}
	for _, id := range integrationIDs {
		allowed[id] = true
	}
	inScope := func(row map[string]any) bool {
		id, _ := row["integration_id"].(string)
		if integrationID != "" && id != integrationID {
			return false
		}
		if integrationIDs != nil && !allowed[id] {
			return false
		}
		return true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	byDay := map[string]*store.InsightsBucket{}
	day := from.UTC().Truncate(24 * time.Hour)
	for !day.After(to.UTC()) {
		key := day.Format("2006-01-02")
		byDay[key] = &store.InsightsBucket{Day: key}
		day = day.Add(24 * time.Hour)
	}
	bucketFor := func(row map[string]any) *store.InsightsBucket {
		created, _ := row["created_at"].(string)
		if len(created) < 10 {
			return nil
		}
		return byDay[created[:10]]
	}

	for _, row := range m.data["alert_groups"] {
		if !inScope(row) {
			continue
		}
		status, _ := row["status"].(string)
		out.GroupsByStatus[status]++
		severity, _ := row["severity"].(string)
		out.GroupsByLevel[store.SeverityLevel(severity)]++
		if b := bucketFor(row); b != nil {
			b.Opened++
			if status == "resolved" {
				b.Resolved++
			}
		}
	}
	for _, row := range m.data["notifications"] {
		if !inScope(row) {
			continue
		}
		status, _ := row["status"].(string)
		out.NotificationsByState[status]++
		if b := bucketFor(row); b != nil {
			switch status {
			case "delivered":
				b.Delivered++
			case "failed":
				b.Failed++
			}
		}
	}

	keys := make([]string, 0, len(byDay))
	for k := range byDay {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Trend = append(out.Trend, *byDay[k])
	}
	return out, nil
}

// QueryHistoryGroups models buildHistoryWhere closely enough for the filters the
// engine actually sets.
//
// integration_ids is handled explicitly rather than left to the generic matcher:
// it is a list, the matcher compares scalars, and every row would fail to match.
// A scoping test would then pass on an empty result whether the scope worked or
// not — which is the one outcome a scoping test must never be able to produce.
func (m *Store) QueryHistoryGroups(_ context.Context, filters map[string]any, limit, offset int) ([]map[string]any, int, error) {
	scoped, hasScope := filters["integration_ids"].([]any)
	rest := map[string]any{}
	for k, v := range filters {
		if k != "integration_ids" {
			rest[k] = v
		}
	}
	rows, _, err := m.ListCollectionPage(context.Background(), "alert_groups", rest, 0, 0, store.SortSpec{})
	if err != nil {
		return nil, 0, err
	}
	if hasScope {
		allowed := map[string]bool{}
		for _, id := range scoped {
			if s, ok := id.(string); ok {
				allowed[s] = true
			}
		}
		kept := rows[:0]
		for _, row := range rows {
			if s, _ := row["integration_id"].(string); allowed[s] {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	total := len(rows)
	if offset > total {
		offset = total
	}
	rows = rows[offset:]
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	if len(rows) == 0 {
		return nil, total, nil
	}
	return rows, total, nil
}
func (m *Store) DeleteOldResolvedGroups(context.Context, string) (int, error) { return 0, nil }

// SetAlertStatusForGroups is modelled for real rather than stubbed: whether a
// closed group's alerts stop reading "firing" is exactly what the tests around
// it exist to pin down.
func (m *Store) SetAlertStatusForGroups(_ context.Context, groupIDs []string, status string) (int, error) {
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
		gid, _ := row["alert_group_id"].(string)
		if !wanted[gid] || row["status"] == status {
			continue
		}
		updated := make(map[string]any, len(row))
		for k, v := range row {
			updated[k] = v
		}
		updated["status"] = status
		m.data["alerts"][id] = updated
		n++
	}
	return n, nil
}

// InsertAuditEvent appends to the in-memory trail. Like the real table, there
// is no way to modify or remove an event once written.
func (m *Store) InsertAuditEvent(_ context.Context, ev store.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, ev)
	return nil
}

// ListAuditEvents returns the trail newest-first, applying the same filters as
// the PostgreSQL implementation.
func (m *Store) ListAuditEvents(_ context.Context, filters map[string]any, limit, offset int) ([]map[string]any, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	match := func(ev store.AuditEvent) bool {
		for col, want := range map[string]string{
			"actor_id": ev.ActorID, "entity_type": ev.EntityType,
			"entity_id": ev.EntityID, "action": ev.Action,
			"request_id": ev.RequestID,
		} {
			if v, ok := filters[col]; ok {
				if s, _ := v.(string); s != "" && s != want {
					return false
				}
			}
		}
		return true
	}
	rows := []map[string]any{}
	for i := len(m.audit) - 1; i >= 0; i-- { // newest first
		ev := m.audit[i]
		if !match(ev) {
			continue
		}
		rows = append(rows, map[string]any{
			"id": ev.ID, "occurred_at": ev.OccurredAt,
			"actor_id": ev.ActorID, "actor_kind": ev.ActorKind,
			"actor_name": ev.ActorName, "actor_role": ev.ActorRole,
			"action": ev.Action, "entity_type": ev.EntityType,
			"entity_id": ev.EntityID, "request_ip": ev.RequestIP,
			"request_id": ev.RequestID, "data": ev.Data,
		})
	}
	total := len(rows)
	if offset > len(rows) {
		offset = len(rows)
	}
	rows = rows[offset:]
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	return rows, total, nil
}

// PruneAuditEvents drops events older than the cutoff. The fake models it for
// real so the retention test asserts on what survives, not on a stub.
func (m *Store) PruneAuditEvents(_ context.Context, cutoffISO string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.audit[:0]
	removed := 0
	for _, ev := range m.audit {
		if ev.OccurredAt != "" && ev.OccurredAt < cutoffISO {
			removed++
			continue
		}
		kept = append(kept, ev)
	}
	m.audit = kept
	return removed, nil
}

// ConsumeRateToken mirrors the SQL in store_rate_limit.go: refill by elapsed
// time, cap at capacity, take one token if there is one. Modelled rather than
// stubbed because "the fifth wrong password is refused" is the behaviour the
// sign-in tests are about.
func (m *Store) ConsumeRateToken(_ context.Context, key string, ratePerSecond, capacity float64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	b, ok := m.buckets[key]
	if !ok {
		b = &rateBucket{tokens: capacity, updatedAt: now}
		m.buckets[key] = b
	}
	b.tokens = min(capacity, b.tokens+now.Sub(b.updatedAt).Seconds()*ratePerSecond)
	b.updatedAt = now
	if b.tokens < 1 {
		return false, nil
	}
	b.tokens--
	return true, nil
}

// RefundRateToken returns a token, never exceeding capacity.
func (m *Store) RefundRateToken(_ context.Context, key string, capacity float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.buckets[key]; ok {
		b.tokens = min(capacity, b.tokens+1)
	}
	return nil
}

// PruneRateBuckets drops buckets untouched since the cutoff.
func (m *Store) PruneRateBuckets(_ context.Context, cutoffISO string) (int, error) {
	cutoff, err := time.Parse(time.RFC3339, cutoffISO)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for k, b := range m.buckets {
		if b.updatedAt.Before(cutoff) {
			delete(m.buckets, k)
			removed++
		}
	}
	return removed, nil
}

// AuditEvents returns a copy of everything recorded so far, for assertions.
func (m *Store) AuditEvents() []store.AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.AuditEvent(nil), m.audit...)
}

// ── identity (credentials and sessions) ───────────────────────────────────────

func (m *Store) SetUserPassword(_ context.Context, userID, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.creds[userID] = hash
	return nil
}

func (m *Store) GetUserPasswordHash(_ context.Context, userID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.creds[userID]
	return h, ok, nil
}

func (m *Store) DeleteUserPassword(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.creds, userID)
	return nil
}

func (m *Store) CountUsersWithPassword(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.creds), nil
}

func (m *Store) FindUserByLogin(_ context.Context, login string) (map[string]any, error) {
	want := strings.ToLower(login)
	rows := m.sortedByID("users", nil) // sorted so a duplicate login resolves deterministically
	for _, r := range rows {
		username, _ := r["username"].(string)
		email, _ := r["email"].(string)
		if strings.ToLower(username) == want || (email != "" && strings.ToLower(email) == want) {
			return r, nil
		}
	}
	return nil, nil
}

// ListTeamIDsForUser mirrors the containment query: membership lives in
// teams.member_ids, not on the user.
func (m *Store) ListTeamIDsForUser(_ context.Context, userID string) ([]string, error) {
	var out []string
	for _, team := range m.sortedByID("teams", nil) {
		members, _ := team["member_ids"].([]any)
		for _, member := range members {
			if member == userID {
				out = append(out, team["id"].(string))
				break
			}
		}
	}
	return out, nil
}

func (m *Store) CreateWebSession(_ context.Context, sess store.WebSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := sess
	if cp.CreatedAt.IsZero() {
		// The column has a default in SQL; the fake stamps it so ordering and
		// the "created_at" field of the sessions API are meaningful in tests.
		cp.CreatedAt = time.Now()
	}
	m.sessions[sess.TokenHash] = &cp
	return nil
}

// FindSessionUser mirrors the SQL join, including the two ways a session stops
// resolving: revoked, and expired.
func (m *Store) FindSessionUser(_ context.Context, tokenHash string) (map[string]any, string, error) {
	m.mu.Lock()
	sess, ok := m.sessions[tokenHash]
	revoked := m.revoked[tokenHash]
	m.mu.Unlock()
	if !ok || revoked || !sess.ExpiresAt.After(time.Now()) {
		return nil, "", nil
	}
	user := m.Row("users", sess.UserID)
	if user == nil {
		return nil, "", nil
	}
	return user, sess.ID, nil
}

// ListWebSessions mirrors the SQL: live sessions only, newest first.
func (m *Store) ListWebSessions(_ context.Context, userID string) ([]store.WebSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.WebSession
	for hash, sess := range m.sessions {
		if sess.UserID != userID || m.revoked[hash] || !sess.ExpiresAt.After(time.Now()) {
			continue
		}
		out = append(out, *sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Store) RevokeWebSessionByID(_ context.Context, sessionID, userID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for hash, sess := range m.sessions {
		if sess.ID == sessionID && sess.UserID == userID && !m.revoked[hash] {
			m.revoked[hash] = true
			return true, nil
		}
	}
	return false, nil
}

func (m *Store) RevokeWebSession(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[tokenHash] = true
	return nil
}

func (m *Store) RevokeUserSessions(_ context.Context, userID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for hash, sess := range m.sessions {
		if sess.UserID == userID && !m.revoked[hash] {
			m.revoked[hash] = true
			n++
		}
	}
	return n, nil
}

func (m *Store) DeleteExpiredWebSessions(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for hash, sess := range m.sessions {
		if sess.ExpiresAt.Before(time.Now().Add(-24 * time.Hour)) {
			delete(m.sessions, hash)
			delete(m.revoked, hash)
			n++
		}
	}
	return n, nil
}

// The retention deletes are modelled for real, like PruneAuditEvents above: the
// sweep's contract is "terminal rows older than the cutoff go, everything else
// stays", and a stub returning 0 would let a sweep that deletes live work pass.
func (m *Store) DeleteOldNotifications(_ context.Context, cutoffISO string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	terminal := map[string]bool{"delivered": true, "failed": true, "skipped": true}
	n := 0
	for id, item := range m.data["notifications"] {
		status, _ := item["status"].(string)
		updated, _ := item["updated_at"].(string)
		if terminal[status] && updated != "" && updated < cutoffISO {
			delete(m.data["notifications"], id)
			n++
		}
	}
	return n, nil
}

func (m *Store) DeleteOldDeliveryAttempts(_ context.Context, cutoffISO string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, item := range m.data["notification_delivery_attempts"] {
		created, _ := item["created_at"].(string)
		if created != "" && created < cutoffISO {
			delete(m.data["notification_delivery_attempts"], id)
			n++
		}
	}
	return n, nil
}

func (m *Store) DeleteOldWebSessions(_ context.Context, cutoffISO string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff, err := time.Parse(time.RFC3339, cutoffISO)
	if err != nil {
		return 0, err
	}
	n := 0
	for hash, sess := range m.sessions {
		if sess.CreatedAt.Before(cutoff) {
			delete(m.sessions, hash)
			delete(m.revoked, hash)
			n++
		}
	}
	return n, nil
}

// PseudonymiseAuditActor mirrors the real one: the events stay, their personal
// columns go.
func (m *Store) PseudonymiseAuditActor(_ context.Context, actorID, pseudonym string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i := range m.audit {
		if m.audit[i].ActorID != actorID {
			continue
		}
		if m.audit[i].ActorName == pseudonym && m.audit[i].RequestIP == "" {
			continue
		}
		m.audit[i].ActorName = pseudonym
		m.audit[i].RequestIP = ""
		n++
	}
	return n, nil
}

func (m *Store) DeleteUserWebSessions(_ context.Context, userID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for hash, sess := range m.sessions {
		if sess.UserID == userID {
			delete(m.sessions, hash)
			delete(m.revoked, hash)
			n++
		}
	}
	return n, nil
}

// ScrubUserNotificationTargets mirrors the real one: the rows stay, the address
// on them is replaced.
func (m *Store) ScrubUserNotificationTargets(_ context.Context, userID, replacement string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	scrubbed := map[string]bool{}
	for id, ntf := range m.data["notifications"] {
		if uid, _ := ntf["user_id"].(string); uid != userID {
			continue
		}
		scrubbed[id] = true
		if target, _ := ntf["target"].(string); target != replacement {
			ntf["target"] = replacement
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

func (m *Store) ListKafkaOutboxEvents(context.Context, int) ([]map[string]any, error) {
	return nil, nil
}
func (m *Store) CountKafkaOutboxByTopic(context.Context) (map[string]int, error) { return nil, nil }
func (m *Store) DeleteKafkaOutboxEvents(context.Context, []string) error         { return nil }
func (m *Store) DeleteOldChatopsMessages(context.Context, string) (int, error)   { return 0, nil }
func (m *Store) TryAdvisoryLock(context.Context, int64) (bool, func(), error) {
	return true, func() {}, nil
}
func (m *Store) NotifyWake(context.Context) error                { return nil }
func (m *Store) WaitForWake(context.Context, time.Duration) bool { return false }
func (m *Store) PoolStats() store.PoolStats                      { return store.PoolStats{} }
func (m *Store) Close()                                          {}

// Ping returns whatever SetPingErr installed. Readiness reports the database as
// a blocker on a failed ping, and that branch is only testable if the double can
// fail on demand.
func (m *Store) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pingErr
}

// SetPingErr makes Ping fail (or succeed again with nil).
func (m *Store) SetPingErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pingErr = err
}

// GetMetadata returns a copy of the deployment metadata document.
func (m *Store) GetMetadata(context.Context) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]any{}
	for k, v := range m.metadata {
		out[k] = v
	}
	return out, nil
}

// PatchMetadata merges patch into the document, deleting keys set to nil.
func (m *Store) PatchMetadata(_ context.Context, patch map[string]any) (map[string]any, error) {
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
