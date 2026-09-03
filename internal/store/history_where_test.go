package store

import (
	"strings"
	"testing"
)

// History is the widest read in the service — alert groups with their
// notifications and delivery attempts inlined, and a notification carries
// somebody's Telegram id or phone number. These tests pin the SQL that bounds
// it, because the wiring above can only be as correct as this clause.

func TestBuildHistoryWhereScopesByIntegrationList(t *testing.T) {
	where, args := buildHistoryWhere(map[string]any{
		"integration_ids": []any{"int-1", "int-2"},
	})

	if !strings.Contains(where, "integration_id IN (") {
		t.Fatalf("where = %q, want an IN list", where)
	}
	if len(args) != 2 || args[0] != "int-1" || args[1] != "int-2" {
		t.Errorf("args = %v, want both integrations bound as parameters", args)
	}
}

// An actor whose teams reach no integration must see nothing. Rendering no
// clause at all would hand them the whole deployment — the failure this scoping
// exists to prevent, arrived at by omission.
func TestBuildHistoryWhereEmptyScopeMatchesNothing(t *testing.T) {
	where, args := buildHistoryWhere(map[string]any{
		"integration_ids": []any{},
	})

	if !strings.Contains(where, "FALSE") {
		t.Errorf("where = %q, want a clause that matches nothing", where)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want none", args)
	}
}

// The scope is a conjunction with whatever the caller asked for, not a
// replacement of it.
func TestBuildHistoryWhereCombinesScopeWithFilters(t *testing.T) {
	where, args := buildHistoryWhere(map[string]any{
		"integration_ids": []any{"int-1"},
		"severity":        "critical",
	})

	if !strings.Contains(where, "integration_id IN (") || !strings.Contains(where, "severity=") {
		t.Errorf("where = %q, want both the scope and the filter", where)
	}
	if len(args) != 2 {
		t.Errorf("args = %v, want the integration and the severity", args)
	}
}

// No scope key at all is the unscoped caller — an admin, an API key, the worker.
func TestBuildHistoryWhereWithoutScopeIsUnrestricted(t *testing.T) {
	where, _ := buildHistoryWhere(map[string]any{})

	if strings.Contains(where, "integration_id") || strings.Contains(where, "FALSE") {
		t.Errorf("where = %q, want no restriction for an unscoped caller", where)
	}
}
