package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// policyStub counts webhook deliveries per URL path so a test can tell which
// policy step fired.
type policyStub struct {
	mu   sync.Mutex
	hits map[string]int
	srv  *httptest.Server
}

func newPolicyStub() *policyStub {
	s := &policyStub{hits: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	return s
}

func (s *policyStub) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// forceRunDue sets every active policy run's next_step_at into the past, so the
// next worker cycle advances it without the test waiting real minutes.
func forceRunsDue(t *testing.T, ctx context.Context, st interface {
	ListCollection(context.Context, string) ([]map[string]any, error)
	UpsertItem(context.Context, string, map[string]any) error
}) {
	t.Helper()
	runs, err := st.ListCollection(ctx, "notification_policy_runs")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	past := utils.ToISO(utils.UTCNow().Add(-time.Hour))
	for _, r := range runs {
		if utils.StrVal(r, "status") != "active" {
			continue
		}
		r["next_step_at"] = past
		if err := st.UpsertItem(ctx, "notification_policy_runs", r); err != nil {
			t.Fatalf("upsert run: %v", err)
		}
	}
}

// TestNotificationPolicyFallback verifies the notify/wait/fallback state machine:
// step 1 fires on the page, step 2 only after the wait, and the run is pruned
// when it completes.
func TestNotificationPolicyFallback(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	stub := newPolicyStub()
	defer stub.srv.Close()

	chain, _ := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "pol-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{}}},
	})
	user, err := eng.CreateUser(ctx, map[string]any{
		"name": "Pol User", "username": "pol-user",
		"notification_policies": map[string]any{
			"default": []any{
				map[string]any{"channel": "webhook", "target": stub.srv.URL + "/primary", "wait_minutes": 5},
				map[string]any{"channel": "webhook", "target": stub.srv.URL + "/fallback", "wait_minutes": 0},
			},
		},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	integ, _ := eng.CreateIntegration(ctx, map[string]any{
		"name": "pol-integration",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if _, err := eng.UpdateEscalationChain(ctx, utils.StrVal(chain, "id"), map[string]any{
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{user["id"]}}},
	}); err != nil {
		t.Fatalf("update chain: %v", err)
	}
	if _, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{"title": "policy fallback"}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// Primary step fires; fallback is scheduled, not sent.
	runCyclesUntil(t, ctx, eng, 5*time.Second, func() bool { return stub.count("/primary") >= 1 })
	if stub.count("/fallback") != 0 {
		t.Fatalf("fallback fired before its wait elapsed: %d", stub.count("/fallback"))
	}

	// Simulate the wait elapsing; the next cycle must deliver the fallback.
	forceRunsDue(t, ctx, st)
	runCyclesUntil(t, ctx, eng, 5*time.Second, func() bool { return stub.count("/fallback") >= 1 })
	if stub.count("/primary") != 1 || stub.count("/fallback") != 1 {
		t.Fatalf("expected primary=1 fallback=1, got primary=%d fallback=%d", stub.count("/primary"), stub.count("/fallback"))
	}

	// The completed run is pruned.
	runs, _ := st.ListCollection(ctx, "notification_policy_runs")
	if len(runs) != 0 {
		t.Fatalf("expected completed run to be pruned, got %d", len(runs))
	}
}

// TestNotificationPolicyStopsOnAck verifies the fallback stops once the group is
// acknowledged: the primary step fired, but the fallback never does.
func TestNotificationPolicyStopsOnAck(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	stub := newPolicyStub()
	defer stub.srv.Close()

	chain, _ := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "ack-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{}}},
	})
	user, _ := eng.CreateUser(ctx, map[string]any{
		"name": "Ack User", "username": "ack-user",
		"notification_policies": map[string]any{
			"default": []any{
				map[string]any{"channel": "webhook", "target": stub.srv.URL + "/primary", "wait_minutes": 5},
				map[string]any{"channel": "webhook", "target": stub.srv.URL + "/fallback", "wait_minutes": 0},
			},
		},
	})
	integ, _ := eng.CreateIntegration(ctx, map[string]any{
		"name": "ack-integration",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if _, err := eng.UpdateEscalationChain(ctx, utils.StrVal(chain, "id"), map[string]any{
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{user["id"]}}},
	}); err != nil {
		t.Fatalf("update chain: %v", err)
	}
	res, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{"title": "policy ack"})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	runCyclesUntil(t, ctx, eng, 5*time.Second, func() bool { return stub.count("/primary") >= 1 })

	// Acknowledge the group, then let the fallback come due.
	group, _ := res["group"].(map[string]any)
	groupID := utils.StrVal(group, "id")
	if groupID == "" {
		t.Fatalf("could not determine alert group id from ingest result: %v", res)
	}
	if _, err := eng.AcknowledgeGroup(ctx, groupID); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	forceRunsDue(t, ctx, st)
	// A few cycles to let the advance run.
	for i := 0; i < 3; i++ {
		if _, err := eng.RunWorkerCycle(ctx); err != nil {
			t.Fatalf("cycle: %v", err)
		}
	}
	if stub.count("/fallback") != 0 {
		t.Fatalf("fallback must not fire after acknowledgement, got %d", stub.count("/fallback"))
	}
	runs, _ := st.ListCollection(ctx, "notification_policy_runs")
	if len(runs) != 0 {
		t.Fatalf("acknowledged run should be pruned, got %d", len(runs))
	}
}

// TestMigrateUserTargetsToDefaultPolicy verifies legacy targets are backfilled
// into a default policy, idempotently.
func TestMigrateUserTargetsToDefaultPolicy(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	if _, err := eng.CreateUser(ctx, map[string]any{
		"name": "Legacy", "username": "legacy",
		"notification_targets": []any{
			map[string]any{"type": "telegram", "target": "123"},
			map[string]any{"type": "email", "target": "l@x"},
		},
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	n, err := eng.MigrateUserTargetsToDefaultPolicy(ctx)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 user migrated, got %d", n)
	}
	// Idempotent: a second run migrates nobody.
	if n2, _ := eng.MigrateUserTargetsToDefaultPolicy(ctx); n2 != 0 {
		t.Fatalf("second migrate should be a no-op, got %d", n2)
	}
	users, _ := st.ListCollection(ctx, "users")
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	pol, ok := users[0]["notification_policies"].(map[string]any)
	if !ok {
		t.Fatalf("user has no notification_policies after migration: %v", users[0])
	}
	steps, _ := pol["default"].([]any)
	if len(steps) != 2 {
		t.Fatalf("expected 2 migrated steps, got %v", pol["default"])
	}
}

// runCyclesUntil runs worker cycles until cond is true or the deadline passes.
func runCyclesUntil(t *testing.T, ctx context.Context, eng interface {
	RunWorkerCycle(context.Context) (map[string]any, error)
}, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testDeadline(within))
	for time.Now().Before(deadline) {
		if _, err := eng.RunWorkerCycle(ctx); err != nil {
			t.Fatalf("worker cycle: %v", err)
		}
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not met before deadline")
	}
}
