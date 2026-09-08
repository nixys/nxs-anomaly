package store

import (
	"fmt"
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
	filters := map[string]any{"status": "open", "severity": "critical", "integration_id": "int1"}
	where, args, err := buildWhere("alert_groups", filters)
	if err != nil {
		t.Fatalf("buildWhere failed: %v", err)
	}
	// Keys are sorted: integration_id, severity, status. severity expands to its
	// family, so it occupies as many placeholders as the level has spellings.
	family := SeverityFamily("critical")
	wantSeverity := "severity IN ($2"
	for i := range family[1:] {
		wantSeverity += fmt.Sprintf(",$%d", i+3)
	}
	wantSeverity += ")"
	want := "integration_id=$1 AND " + wantSeverity + fmt.Sprintf(" AND status=$%d", len(family)+2)
	if where != want {
		t.Errorf("where = %q, want %q", where, want)
	}
	if len(args) != len(family)+2 || args[0] != "int1" || args[len(args)-1] != "open" {
		t.Errorf("args = %#v", args)
	}
}

// The filter names a level, and the level is what the responder means. An alert
// a source labelled "P1" is critical; before this it was invisible to every
// filter the UI could offer.
func TestBuildWhereSeverityMatchesTheWholeLevel(t *testing.T) {
	where, args, err := buildWhere("alert_groups", map[string]any{"severity": "critical"})
	if err != nil {
		t.Fatalf("buildWhere failed: %v", err)
	}
	if !strings.HasPrefix(where, "severity IN (") {
		t.Errorf("where = %q, want an IN over the whole level", where)
	}
	var sawP1, sawCritical bool
	for _, arg := range args {
		switch arg {
		case "p1":
			sawP1 = true
		case "critical":
			sawCritical = true
		}
	}
	if !sawCritical || !sawP1 {
		t.Errorf("args = %#v, want the level's own name and its aliases", args)
	}
}

// A spelling nothing here models still filters, and filters exactly. Expanding
// it to some guessed neighbourhood would answer a different question than the
// one asked.
func TestBuildWhereUnknownSeverityMatchesItselfOnly(t *testing.T) {
	_, args, err := buildWhere("alert_groups", map[string]any{"severity": "wobbly"})
	if err != nil {
		t.Fatalf("buildWhere failed: %v", err)
	}
	if len(args) != 1 || args[0] != "wobbly" {
		t.Errorf("args = %#v, want the word as given", args)
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
