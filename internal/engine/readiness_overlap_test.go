package engine

import (
	"context"
	"strings"
	"testing"
)

// The Terraform example's "Weekly Rotation": two weekly shifts a week apart.
// Both recur every week, so from the second week both people are on call at
// once. Coverage was perfect and readiness said nothing.
func TestReadinessWarnsAboutEveryoneOnCallAtOnce(t *testing.T) {
	ms := newMemStore()
	ms.seed("users", map[string]any{"id": "alice", "name": "Alice"})
	ms.seed("users", map[string]any{"id": "bob", "name": "Bob"})
	ms.seed("schedules", map[string]any{"id": "s1", "name": "Ops Weekly Rotation", "enabled": true, "shifts": []any{
		map[string]any{"user_id": "alice", "start_at": "2026-06-01T09:00:00+03:00", "end_at": "2026-06-08T09:00:00+03:00", "recurrence": "weekly"},
		map[string]any{"user_id": "bob", "start_at": "2026-06-08T09:00:00+03:00", "end_at": "2026-06-15T09:00:00+03:00", "recurrence": "weekly"},
	}})
	c := crudEngine(ms).checkScheduleCoverage(context.Background())
	if c.Severity != ReadinessWarning {
		t.Fatalf("severity = %v, want a warning", c.Severity)
	}
	if len(c.Items) != 1 || !strings.Contains(c.Items[0], "more than one person on call") {
		t.Errorf("items = %v, want the overlapping schedule named", c.Items)
	}
}
