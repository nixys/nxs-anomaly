package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Deleting an object paging depends on used to succeed and break the chain
// silently: the alert that needed it found "missing_chain" or "unknown users".

func referencedStore() *memStore {
	ms := newMemStore()
	ms.seed("users", map[string]any{"id": "u1", "username": "alice", "name": "Alice"})
	ms.seed("teams", map[string]any{"id": "t1", "name": "Ops"})
	ms.seed("schedules", map[string]any{"id": "s1", "name": "Primary"})
	ms.seed("escalation_chains", map[string]any{"id": "c1", "name": "Critical", "steps": []any{
		map[string]any{"kind": "NOTIFY_SCHEDULE", "schedule_id": "s1"},
		map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{"u1"}},
		map[string]any{"kind": "NOTIFY_TEAM", "team_id": "t1"},
	}})
	ms.seed("integrations", map[string]any{"id": "i1", "name": "Prometheus", "routes": []any{
		map[string]any{"name": "default", "is_default": true, "match_type": "all", "escalation_chain_id": "c1"},
	}})
	return ms
}

func TestDeletingWhatAChainPagesIsRefused(t *testing.T) {
	for _, tc := range []struct{ collection, id, want string }{
		{"users", "u1", `escalation chain "Critical" step 2 (NOTIFY_USER)`},
		{"teams", "t1", `escalation chain "Critical" step 3 (NOTIFY_TEAM)`},
		{"schedules", "s1", `escalation chain "Critical" step 1 (NOTIFY_SCHEDULE)`},
		{"escalation_chains", "c1", `integration "Prometheus" route "default"`},
	} {
		e := crudEngine(referencedStore())
		_, err := e.DeleteEntity(context.Background(), tc.collection, tc.id)
		if !errors.Is(err, ErrConflict) {
			t.Errorf("delete %s %s = %v, want a conflict", tc.collection, tc.id, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("delete %s: %q does not name %s", tc.collection, err.Error(), tc.want)
		}
	}
}

func TestADeletedIntegrationDoesNotHoldItsChain(t *testing.T) {
	ms := referencedStore()
	ms.data["integrations"]["i1"]["deleted_at"] = "2026-01-01T00:00:00Z"
	e := crudEngine(ms)
	if _, err := e.DeleteEntity(context.Background(), "escalation_chains", "c1"); err != nil {
		t.Errorf("delete chain used only by a deleted integration = %v, want success", err)
	}
}
