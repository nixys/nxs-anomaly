package store

import (
	"strings"
	"testing"
)

func TestBuildWhereEquality(t *testing.T) {
	where, args, err := buildWhere("alert_groups", map[string]any{"status": "open"})
	if err != nil {
		t.Fatalf("buildWhere failed: %v", err)
	}
	if where != "status=$1" {
		t.Errorf("where = %q", where)
	}
	if len(args) != 1 || args[0] != "open" {
		t.Errorf("args = %#v", args)
	}
}

func TestBuildWhereDeterministicOrder(t *testing.T) {
	filters := map[string]any{"status": "open", "severity": "high", "integration_id": "int1"}
	where, args, err := buildWhere("alert_groups", filters)
	if err != nil {
		t.Fatalf("buildWhere failed: %v", err)
	}
	// Keys are sorted: integration_id, severity, status.
	if where != "integration_id=$1 AND severity=$2 AND status=$3" {
		t.Errorf("where = %q", where)
	}
	if len(args) != 3 || args[0] != "int1" || args[1] != "high" || args[2] != "open" {
		t.Errorf("args = %#v", args)
	}
}

func TestBuildWhereInClause(t *testing.T) {
	where, args, err := buildWhere("alert_groups", map[string]any{"status": []any{"open", "acknowledged"}})
	if err != nil {
		t.Fatalf("buildWhere failed: %v", err)
	}
	if where != "status IN ($1,$2)" {
		t.Errorf("where = %q", where)
	}
	if len(args) != 2 {
		t.Errorf("args = %#v", args)
	}
}

func TestBuildWhereEmptyInClauseMatchesNothing(t *testing.T) {
	where, args, err := buildWhere("alert_groups", map[string]any{"status": []any{}})
	if err != nil {
		t.Fatalf("buildWhere failed: %v", err)
	}
	if where != "FALSE" {
		t.Errorf("where = %q", where)
	}
	if len(args) != 0 {
		t.Errorf("args = %#v", args)
	}
}

func TestBuildWhereNotEqual(t *testing.T) {
	where, args, err := buildWhere("alert_groups", map[string]any{"status": NotEqualFilter{Value: "resolved"}})
	if err != nil {
		t.Fatalf("buildWhere failed: %v", err)
	}
	if where != "status!=$1" {
		t.Errorf("where = %q", where)
	}
	if len(args) != 1 || args[0] != "resolved" {
		t.Errorf("args = %#v", args)
	}
}

func TestBuildWhereRejectsUnknownColumn(t *testing.T) {
	// Filter keys are whitelisted against id + TypedColumns; anything else must
	// be rejected, never interpolated into SQL.
	if _, _, err := buildWhere("alert_groups", map[string]any{"data->>'x'": "1"}); err == nil {
		t.Fatal("unknown filter column must be rejected")
	}
	if _, _, err := buildWhere("teams", map[string]any{"status": "open"}); err == nil {
		t.Fatal("column not typed for this collection must be rejected")
	}
}

func TestBuildWhereEmptyFilters(t *testing.T) {
	where, args, err := buildWhere("alert_groups", nil)
	if err != nil || where != "" || args != nil {
		t.Fatalf("empty filters: where=%q args=%#v err=%v", where, args, err)
	}
}

func TestBuildUpsertQueryErrors(t *testing.T) {
	if _, _, err := buildUpsertQuery("nope", map[string]any{"id": "x"}); err == nil {
		t.Fatal("unknown collection must fail")
	}
	if _, _, err := buildUpsertQuery("users", map[string]any{"username": "no-id"}); err == nil {
		t.Fatal("item without id must fail")
	}
}

func TestBuildUpsertQueryTypedColumns(t *testing.T) {
	q, args, err := buildUpsertQuery("users", map[string]any{"id": "u1", "username": "alice", "on_duty": true})
	if err != nil {
		t.Fatalf("buildUpsertQuery failed: %v", err)
	}
	if !strings.Contains(q, "ON CONFLICT(id) DO UPDATE") {
		t.Errorf("query missing upsert clause: %s", q)
	}
	// id, data + typed columns (username, email, on_duty, priority).
	if want := 2 + len(TypedColumns["users"]); len(args) != want {
		t.Errorf("args len = %d, want %d", len(args), want)
	}
}

func TestBuildUpsertQueryNotificationsIdempotencyGuard(t *testing.T) {
	q, _, err := buildUpsertQuery("notifications", map[string]any{"id": "n1", "idempotency_key": "k1"})
	if err != nil {
		t.Fatalf("buildUpsertQuery failed: %v", err)
	}
	if !strings.Contains(q, "WHERE NOT EXISTS") || !strings.Contains(q, "idempotency_key") {
		t.Errorf("notifications upsert must guard on idempotency_key: %s", q)
	}
}
