package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

// isUnchanged mirrors the skip condition in upsertCollection.
func isUnchanged(baseline map[string][]byte, id string, item map[string]any) bool {
	data, err := json.Marshal(item)
	if err != nil {
		return false
	}
	prev, ok := baseline[id]
	return ok && bytes.Equal(prev, data)
}

// testMapRecord is a minimal Record stub for store unit tests. Production
// wrappers live in the model package, which store cannot import (cyclic); a
// local stub plus per-test wrapper registration keeps the store unit tests
// self-contained.
type testMapRecord struct {
	collection string
	m          map[string]any
}

func (r testMapRecord) RecordID() string             { id, _ := r.m["id"].(string); return id }
func (r testMapRecord) MarshalData() ([]byte, error) { return json.Marshal(r.m) }
func (r testMapRecord) TypedValues() []any {
	// Reproduces the historical typedValues(collection, item) behavior the
	// snapshot/upsert tests rely on. Limited to the few collections used in
	// store unit tests; production paths use the model wrappers.
	switch r.collection {
	case "users":
		return []any{nullOrStr(r.m, "username"), nullOrStr(r.m, "email"), boolOrFalse(r.m, "on_duty"), nullOrStr(r.m, "priority")}
	case "notifications":
		return []any{nullOrStr(r.m, "alert_group_id"), nullOrStr(r.m, "user_id"), nullOrStr(r.m, "channel"), nullOrStr(r.m, "status"), nullOrStr(r.m, "idempotency_key"), anyOrNil(r.m, "retry_count"), nullOrStr(r.m, "next_retry_at"), nullOrStr(r.m, "last_error"), nullOrStr(r.m, "batch_id"), nullOrStr(r.m, "batch_key"), nullOrStr(r.m, "provider_status")}
	}
	return nil
}

func nullOrStr(item map[string]any, key string) any {
	v, ok := item[key]
	if !ok || v == nil || v == "" {
		return nil
	}
	return fmt.Sprintf("%v", v)
}

func boolOrFalse(item map[string]any, key string) any {
	v, _ := item[key].(bool)
	return v
}

func anyOrNil(item map[string]any, key string) any {
	v, ok := item[key]
	if !ok || v == nil || v == "" {
		return nil
	}
	return v
}

// init registers stub wrappers for the collections store unit tests upsert
// against. Production registrations happen from internal/model init(); store
// tests can't import model (cyclic), so they self-register stubs here. The
// stubs cover only the keys these tests assert on.
func init() {
	for _, col := range []string{"users", "notifications", "alert_groups", "teams", "schedules"} {
		c := col
		RegisterRecordWrapper(c, func(m map[string]any) Record { return testMapRecord{collection: c, m: m} })
	}
}

// recs wraps untyped map rows as Records for snapshotCollection.
func recs(items map[string]map[string]any) map[string]Record {
	out := make(map[string]Record, len(items))
	for id, m := range items {
		out[id] = testMapRecord{collection: "alert_groups", m: m}
	}
	return out
}

func TestSnapshotCollectionDetectsChanges(t *testing.T) {
	items := map[string]map[string]any{
		"a": {"id": "a", "status": "open", "alert_count": 1, "logs": []any{}},
		"b": {"id": "b", "status": "open", "labels": map[string]any{"env": "prod"}},
		"c": {"id": "c", "status": "resolved"},
	}
	baseline := snapshotCollection(recs(items))
	if len(baseline) != 3 {
		t.Fatalf("baseline size = %d, want 3", len(baseline))
	}

	// Top-level in-place mutation must be detected.
	items["a"]["status"] = "resolved"
	// Nested in-place mutation (the appendLog pattern) must be detected.
	items["b"]["labels"].(map[string]any)["env"] = "stage"
	// New item has no baseline and must always be written.
	items["d"] = map[string]any{"id": "d", "status": "open"}

	if isUnchanged(baseline, "a", items["a"]) {
		t.Error("top-level mutation of a not detected")
	}
	if isUnchanged(baseline, "b", items["b"]) {
		t.Error("nested mutation of b not detected")
	}
	if !isUnchanged(baseline, "c", items["c"]) {
		t.Error("untouched item c reported as changed")
	}
	if isUnchanged(baseline, "d", items["d"]) {
		t.Error("new item d must not match baseline")
	}
}

func TestSnapshotCollectionValueRewriteSameContent(t *testing.T) {
	// A mutator may overwrite a field with an equal value (possibly a different
	// numeric Go type after a JSON round-trip); that must NOT count as a change.
	items := map[string]map[string]any{
		"a": {"id": "a", "alert_count": float64(2), "severity": "high"},
	}
	baseline := snapshotCollection(recs(items))
	items["a"]["alert_count"] = int(2)
	items["a"]["severity"] = "high"
	if !isUnchanged(baseline, "a", items["a"]) {
		t.Error("rewrite with identical content reported as changed")
	}
}

func TestSnapshotCollectionEmpty(t *testing.T) {
	if snapshotCollection(nil) != nil {
		t.Error("nil collection must yield nil baseline")
	}
	if snapshotCollection(map[string]Record{}) != nil {
		t.Error("empty collection must yield nil baseline")
	}
}
