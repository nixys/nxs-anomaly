package store

import (
	"bytes"
	"fmt"
	"testing"
)

// buildRecords returns n synthetic alert_group-shaped map records, the hot
// collection that snapshot-diff marshals every worker cycle. Uses the local
// testMapRecord stub (the production wrapper lives in model and would create
// an import cycle here).
func buildRecords(n int) map[string]Record {
	recs := make(map[string]Record, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("ag-%05d", i)
		recs[id] = testMapRecord{collection: "alert_groups", m: map[string]any{
			"id":               id,
			"integration_id":   "int-1",
			"route_id":         "route-1",
			"dedupe_key":       fmt.Sprintf("dk-%d", i),
			"status":           "open",
			"severity":         "critical",
			"alert_count":      float64(i % 50),
			"next_run_at":      "2026-06-14T10:00:00+00:00",
			"last_received_at": "2026-06-14T09:59:00+00:00",
			"title":            "CPU saturation on node",
			"labels":           map[string]any{"team": "infra", "env": "prod", "host": id},
			"logs": []any{
				map[string]any{"kind": "created", "ts": "2026-06-14T09:00:00+00:00"},
				map[string]any{"kind": "escalation_resumed", "ts": "2026-06-14T09:30:00+00:00"},
			},
		}}
	}
	return recs
}

// BenchmarkSnapshotCollection measures the pre-mutator marshal pass.
func BenchmarkSnapshotCollection(b *testing.B) {
	recs := buildRecords(500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = snapshotCollection(recs)
	}
}

// BenchmarkSnapshotDiffUnchanged measures the full no-DB cost of the save path
// for an unchanged collection: snapshot before the mutator, then re-marshal and
// compare every row at save time (the second marshal upsertCollection performs).
// This is the doubled-marshal hot path UpdateCollectionsFiltered runs per cycle.
func BenchmarkSnapshotDiffUnchanged(b *testing.B) {
	recs := buildRecords(500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		baseline := snapshotCollection(recs)
		skipped := 0
		for id, rec := range recs {
			data, _ := rec.MarshalData()
			if prev, ok := baseline[id]; ok && bytes.Equal(prev, data) {
				skipped++
			}
		}
		if skipped != len(recs) {
			b.Fatalf("expected all %d rows unchanged, skipped %d", len(recs), skipped)
		}
	}
}

// BenchmarkSaveWriteAll measures the save path UpdateCollectionsWriteAll takes:
// no baseline snapshot, every row marshaled exactly once (the bytes that go to
// the upsert). Compared against BenchmarkSnapshotDiffUnchanged it isolates the
// marshal pass removed for hot paths that rewrite every loaded row
// (delivery/retry finalize every claimed notification), where the baseline diff
// can never spare a write and the second marshal is pure overhead.
func BenchmarkSaveWriteAll(b *testing.B) {
	recs := buildRecords(500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, rec := range recs {
			if _, err := rec.MarshalData(); err != nil {
				b.Fatal(err)
			}
		}
	}
}
