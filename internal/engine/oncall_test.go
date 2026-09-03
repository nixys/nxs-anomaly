package engine

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// TestCurrentlyOnCall verifies the aggregate resolves who is on call through
// Schedule v2 (an override here), reports a deleted participant as a hole, and
// skips disabled schedules — none of which the manual on_duty flag would show.
func TestCurrentlyOnCall(t *testing.T) {
	ms := newMemStore()
	ms.seed("users", map[string]any{"id": "u1", "name": "Alice", "username": "alice"})
	now := utils.UTCNow()
	within := func(d time.Duration) string { return utils.ToISO(now.Add(d)) }

	ms.seed("schedules",
		map[string]any{
			"id": "s1", "name": "Primary", "enabled": true,
			"overrides": []any{map[string]any{
				"id": "o1", "user_id": "u1",
				"start_at": within(-time.Hour), "until": within(time.Hour),
			}},
		},
		// A schedule whose current slot names a user that no longer exists.
		map[string]any{
			"id": "s2", "name": "Ghost", "enabled": true,
			"overrides": []any{map[string]any{
				"id": "o2", "user_id": "u_ghost",
				"start_at": within(-time.Hour), "until": within(time.Hour),
			}},
		},
		// Disabled schedules never contribute.
		map[string]any{
			"id": "s3", "name": "Off", "enabled": false,
			"overrides": []any{map[string]any{
				"id": "o3", "user_id": "u1",
				"start_at": within(-time.Hour), "until": within(time.Hour),
			}},
		},
	)

	e, _ := cachedEngine(ms)
	res, err := e.CurrentlyOnCall(context.Background())
	if err != nil {
		t.Fatalf("CurrentlyOnCall: %v", err)
	}
	items, _ := res["items"].([]map[string]any)
	byUser := map[string]map[string]any{}
	for _, it := range items {
		byUser[utils.StrVal(it, "user_id")] = it
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 on-call entries (s1, s2), got %d: %v", len(items), items)
	}
	if a := byUser["u1"]; a == nil || utils.StrVal(a, "name") != "Alice" || a["exists"] != true {
		t.Fatalf("u1 entry wrong: %v", a)
	}
	if g := byUser["u_ghost"]; g == nil || g["exists"] != false || utils.StrVal(g, "name") != "u_ghost" {
		t.Fatalf("ghost entry should be reported as a hole: %v", g)
	}
}
