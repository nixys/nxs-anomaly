package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// TestMultiWorkerNoDoubleDelivery is the BETA-020 multi-replica guarantee: two
// worker engines sharing one database, running cycles concurrently, must deliver
// each notification exactly once. The claim path (ClaimDeliverableNotifications,
// FOR UPDATE SKIP LOCKED with a per-engine worker id) is what makes this safe;
// this test exercises it under real contention rather than trusting the SQL by
// inspection.
//
// Each user's webhook target points at a distinct path on one counting stub, so
// a double delivery shows up as a path hit twice — an unmistakable, localized
// failure rather than an aggregate mismatch.
func TestMultiWorkerNoDoubleDelivery(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	const nUsers = 16

	var mu sync.Mutex
	hits := map[string]int{}
	// A deliberately slow stub widens the window in which both workers hold
	// live claims at once, so the SKIP LOCKED contention is actually tested
	// rather than serialized away by a fast round-trip.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "mw-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{}}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	userIDs := make([]any, 0, nUsers)
	for i := 0; i < nUsers; i++ {
		u, err := eng.CreateUser(ctx, map[string]any{
			"name":     fmt.Sprintf("MW User %d", i),
			"username": fmt.Sprintf("mw-user-%d", i),
			"notification_targets": []any{
				map[string]any{"type": "webhook", "target": fmt.Sprintf("%s/u/%d", stub.URL, i)},
			},
		})
		if err != nil {
			t.Fatalf("create user %d: %v", i, err)
		}
		userIDs = append(userIDs, u["id"])
	}
	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "mw-integration",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	if _, err := eng.UpdateEscalationChain(ctx, utils.StrVal(chain, "id"), map[string]any{
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": userIDs}},
	}); err != nil {
		t.Fatalf("update chain: %v", err)
	}

	if _, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{
		"title": "multi-worker test",
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// Two independent engines over the same store — distinct worker ids, as two
	// pod replicas would have. eng drove setup; add a second worker.
	eng2 := engine.New(st)

	deadline := time.Now().Add(testDeadline(15 * time.Second))
	var wg sync.WaitGroup
	for _, w := range []*engine.Engine{eng, eng2} {
		wg.Add(1)
		go func(e *engine.Engine) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if _, err := e.RunWorkerCycle(ctx); err != nil {
					t.Errorf("worker cycle: %v", err)
					return
				}
				mu.Lock()
				done := len(hits) >= nUsers
				mu.Unlock()
				if done {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}(w)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(hits) != nUsers {
		t.Fatalf("expected %d distinct targets delivered, got %d: %v", nUsers, len(hits), hits)
	}
	total := 0
	for path, n := range hits {
		if n != 1 {
			t.Errorf("target %s delivered %d times, want exactly 1 (double delivery across replicas)", path, n)
		}
		total += n
	}
	if total != nUsers {
		t.Fatalf("total deliveries = %d, want %d (exactly once each)", total, nUsers)
	}

	// The store should agree: every notification ended 'delivered', none stuck
	// in a transient claim ('delivering'/'retrying') or duplicated.
	delivered, err := st.CountCollection(ctx, "notifications", map[string]any{"status": "delivered"})
	if err != nil {
		t.Fatalf("count delivered: %v", err)
	}
	if delivered != nUsers {
		t.Fatalf("delivered notifications in store = %d, want %d", delivered, nUsers)
	}
}
