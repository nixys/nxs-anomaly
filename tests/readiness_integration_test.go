package tests

import (
	"context"
	"sync"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// readiness_integration_test.go exercises the deployment-metadata row against a
// real PostgreSQL. The unit tests model that row with a Go map, which cannot
// catch what actually goes wrong here: the single-row upsert conflicting on the
// wrong column, the jsonb round-trip losing a key, or the read-modify-write
// dropping a concurrent writer's field.

func TestMetadataRoundTripAndMerge(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()

	// The row does not exist on a fresh database, which must read as empty
	// rather than as an error — readiness calls this before anything writes it.
	before, err := st.GetMetadata(ctx)
	if err != nil {
		t.Fatalf("GetMetadata on a fresh database: %v", err)
	}
	if before == nil {
		t.Fatal("GetMetadata returned a nil map; readiness would panic indexing it")
	}

	if _, err := st.PatchMetadata(ctx, map[string]any{"last_backup_at": "2026-07-27T00:00:00Z"}); err != nil {
		t.Fatalf("PatchMetadata (insert): %v", err)
	}
	// A second patch of a different key must not clobber the first: the whole
	// point of merging rather than replacing the document.
	if _, err := st.PatchMetadata(ctx, map[string]any{"worker_heartbeat_at": "2026-07-27T00:01:00Z"}); err != nil {
		t.Fatalf("PatchMetadata (update): %v", err)
	}

	got, err := st.GetMetadata(ctx)
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if utils.StrVal(got, "last_backup_at") != "2026-07-27T00:00:00Z" {
		t.Errorf("last_backup_at = %v, want it to survive the second patch", got["last_backup_at"])
	}
	if utils.StrVal(got, "worker_heartbeat_at") != "2026-07-27T00:01:00Z" {
		t.Errorf("worker_heartbeat_at = %v, want it written", got["worker_heartbeat_at"])
	}

	// Nested values must round-trip through jsonb: the readiness
	// acknowledgement is an object, not a scalar.
	ack := map[string]any{"actor": "alice", "reason": "pilot", "fingerprint": "abc123"}
	if _, err := st.PatchMetadata(ctx, map[string]any{"readiness_ack": ack}); err != nil {
		t.Fatalf("PatchMetadata (nested): %v", err)
	}
	got, err = st.GetMetadata(ctx)
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	stored, ok := got["readiness_ack"].(map[string]any)
	if !ok {
		t.Fatalf("readiness_ack came back as %T, want an object", got["readiness_ack"])
	}
	if stored["fingerprint"] != "abc123" {
		t.Errorf("fingerprint = %v, want it preserved", stored["fingerprint"])
	}

	// A nil value deletes its key.
	if _, err := st.PatchMetadata(ctx, map[string]any{"readiness_ack": nil}); err != nil {
		t.Fatalf("PatchMetadata (delete): %v", err)
	}
	got, _ = st.GetMetadata(ctx)
	if _, present := got["readiness_ack"]; present {
		t.Error("readiness_ack survived a nil patch, so an acknowledgement could never be cleared")
	}
}

// TestMetadataConcurrentPatchesBothSurvive is the reason PatchMetadata locks the
// row instead of doing a plain read-modify-write: the worker heartbeat and a
// backup report are written by different processes and must not lose each other.
func TestMetadataConcurrentPatchesBothSurvive(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()

	if _, err := st.PatchMetadata(ctx, map[string]any{"seed": "1"}); err != nil {
		t.Fatalf("seed patch: %v", err)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "writer_" + string(rune('a'+n))
			if _, err := st.PatchMetadata(ctx, map[string]any{key: n}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent PatchMetadata: %v", err)
	}

	got, err := st.GetMetadata(ctx)
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	for i := 0; i < writers; i++ {
		key := "writer_" + string(rune('a'+i))
		if _, present := got[key]; !present {
			t.Errorf("%s is missing: a concurrent patch was lost", key)
		}
	}
	if utils.StrVal(got, "seed") != "1" {
		t.Error("the seed key was clobbered by the concurrent writers")
	}
}

// TestReadinessAgainstPostgres runs the whole report against a real database, so
// the checks that read reference collections are exercised through real SQL
// rather than through the in-memory double.
func TestReadinessAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)
	// clearStore truncates the collection tables but not the single metadata
	// row, which is not a collection. Another test in this package (or an
	// earlier worker cycle) may have left a fresh heartbeat and backup there,
	// which would make this installation look ready.
	if _, err := st.PatchMetadata(ctx, map[string]any{
		"last_backup_at": nil, "worker_heartbeat_at": nil, "readiness_ack": nil,
	}); err != nil {
		t.Fatalf("reset metadata: %v", err)
	}

	report, err := eng.Readiness(ctx)
	if err != nil {
		t.Fatalf("Readiness: %v", err)
	}
	// An empty installation is not ready: no backup has been reported and no
	// worker has ever run.
	if report["ready"] != false {
		t.Errorf("ready = %v on an empty installation, want false", report["ready"])
	}
	if n, _ := report["blockers"].(int); n == 0 {
		t.Error("blockers = 0 on an empty installation, want the backup and worker checks to fire")
	}
	if utils.StrVal(report, "blocker_fingerprint") == "" {
		t.Error("blocker_fingerprint is empty while blockers exist")
	}

	// The database check must pass: this store is demonstrably reachable.
	for _, raw := range report["checks"].([]any) {
		c := raw.(map[string]any)
		if utils.StrVal(c, "key") == "database" && utils.StrVal(c, "severity") != "ok" {
			t.Errorf("database check = %s against a live PostgreSQL: %s",
				utils.StrVal(c, "severity"), utils.StrVal(c, "detail"))
		}
	}

	// Reporting a backup must move that check, proving the write and the read
	// agree through real jsonb.
	if _, err := eng.ReportBackup(ctx, map[string]any{"kind": "integration-test"}); err != nil {
		t.Fatalf("ReportBackup: %v", err)
	}
	report, err = eng.Readiness(ctx)
	if err != nil {
		t.Fatalf("Readiness after backup report: %v", err)
	}
	for _, raw := range report["checks"].([]any) {
		c := raw.(map[string]any)
		if utils.StrVal(c, "key") == "backup" && utils.StrVal(c, "severity") != "ok" {
			t.Errorf("backup check = %s after a report: %s",
				utils.StrVal(c, "severity"), utils.StrVal(c, "detail"))
		}
	}
}
