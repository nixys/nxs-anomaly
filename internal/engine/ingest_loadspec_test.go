package engine

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// recordingStore captures the LoadSpecs an operation asks for, then delegates.
type recordingStore struct {
	store.PostgreSQLStore
	loads [][]store.LoadSpec
}

func (r *recordingStore) UpdateCollectionsFiltered(ctx context.Context, loads []store.LoadSpec, save []string, mutator func(*store.State) (any, error), lockKey int64) (any, error) {
	r.loads = append(r.loads, loads)
	return r.PostgreSQLStore.UpdateCollectionsFiltered(ctx, loads, save, mutator, lockKey)
}

func specFor(loads []store.LoadSpec, collection string) (store.LoadSpec, bool) {
	for _, l := range loads {
		if l.Collection == collection {
			return l, true
		}
	}
	return store.LoadSpec{}, false
}

// TestIngestLoadsOnlyTheEnvelopesDedupeKeys is the regression behind narrowing
// the ingest LoadSpec.
//
// Ingest used to load every unresolved group of the integration so the
// in-mutator re-scan could find one by dedupe key. That made a single ingest
// cost O(open groups) — measured 16.6ms at 50 open groups against 148.1ms at
// 800, so a burst of n alerts cost O(n²), and it degraded exactly during an
// incident, when open groups are many and alerts arrive fastest.
//
// The assertion is on the filter rather than on elapsed time: the filter is
// what regressed, and a timing assertion on this would be flaky. The paired
// behavioural half — that deduplication still finds the group — is covered
// against real PostgreSQL by the ingest tests in tests/.
func TestIngestLoadsOnlyTheEnvelopesDedupeKeys(t *testing.T) {
	base := storetest.New()
	rec := &recordingStore{PostgreSQLStore: base}
	eng := New(rec)
	ctx := context.Background()

	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "loadspec",
		"routes": []any{map[string]any{
			"name": "all", "match_type": "all", "is_default": true,
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	key := integ["key"].(string)

	rec.loads = nil
	if _, err := eng.IngestAlert(ctx, key, map[string]any{
		"title": "one", "dedupe_key": "dk-1",
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(rec.loads) == 0 {
		t.Fatal("ingest issued no filtered load at all")
	}
	last := rec.loads[len(rec.loads)-1]

	groups, ok := specFor(last, "alert_groups")
	if !ok {
		t.Fatal("ingest did not load alert_groups")
	}
	keys, ok := groups.Filters["dedupe_key"].([]any)
	if !ok {
		t.Fatalf("alert_groups is loaded without a dedupe_key filter (%#v) — ingest is scanning the "+
			"integration's whole open backlog again", groups.Filters)
	}
	if len(keys) != 1 || keys[0] != "dk-1" {
		t.Errorf("dedupe_key filter = %#v, want exactly the envelope's one key", keys)
	}

	batches, ok := specFor(last, "notification_batches")
	if !ok {
		t.Fatal("ingest did not load notification_batches")
	}
	if _, ok := batches.Filters["batch_key"].([]any); !ok {
		t.Errorf("notification_batches is loaded without a batch_key filter (%#v) — the open-batch scan "+
			"grows with the integration's backlog", batches.Filters)
	}
}

// TestIngestLoadSpecCoversEveryKeyInTheEnvelope: an Alertmanager envelope carries
// many alerts at once, and the filter has to name every distinct dedupe key in
// it or the re-scan silently misses a group and opens a duplicate.
func TestIngestLoadSpecCoversEveryKeyInTheEnvelope(t *testing.T) {
	base := storetest.New()
	rec := &recordingStore{PostgreSQLStore: base}
	eng := New(rec)
	ctx := context.Background()

	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "loadspec-envelope",
		"routes": []any{map[string]any{
			"name": "all", "match_type": "all", "is_default": true,
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	key := integ["key"].(string)

	rec.loads = nil
	if _, err := eng.IngestAlertmanager(ctx, key, map[string]any{
		"alerts": []any{
			map[string]any{"status": "firing", "labels": map[string]any{"alertname": "a"}, "fingerprint": "fp-a"},
			map[string]any{"status": "firing", "labels": map[string]any{"alertname": "b"}, "fingerprint": "fp-b"},
			map[string]any{"status": "firing", "labels": map[string]any{"alertname": "c"}, "fingerprint": "fp-c"},
		},
	}); err != nil {
		t.Fatalf("ingest envelope: %v", err)
	}
	last := rec.loads[len(rec.loads)-1]
	groups, _ := specFor(last, "alert_groups")
	keys, ok := groups.Filters["dedupe_key"].([]any)
	if !ok {
		t.Fatalf("no dedupe_key filter: %#v", groups.Filters)
	}
	if len(keys) != 3 {
		t.Errorf("filter names %d dedupe keys for a 3-alert envelope: %#v", len(keys), keys)
	}
}
