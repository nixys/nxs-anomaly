package engine

import (
	"context"
	"github.com/nixys/nxs-anomaly/internal/storetest"
	"strings"
	"testing"
)

func TestReferenceSearchAndValidation(t *testing.T) {
	st := storetest.New()
	st.Seed("users", map[string]any{"id": "one", "name": "Alice", "email": "special@example.org"}, map[string]any{"id": "two", "name": "Bob"})
	e := New(st)
	page, err := e.ListCollectionPage(context.Background(), "users", map[string]any{"q": " SPECIAL@ ", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	items := page["items"].([]map[string]any)
	if len(items) != 1 || items[0]["id"] != "one" {
		t.Fatalf("wrong search results: %#v", page)
	}
	for _, params := range []map[string]any{{"q": strings.Repeat("x", 257)}, {"has_steps": "true"}} {
		if _, err := e.ListCollectionPage(context.Background(), "users", params); err == nil {
			t.Fatalf("accepted invalid params: %v", params)
		}
	}
	st.Seed("escalation_chains", map[string]any{"id": "empty", "steps": []any{}}, map[string]any{"id": "ready", "steps": []any{map[string]any{"type": "wait"}}})
	page, err = e.ListCollectionPage(context.Background(), "escalation_chains", map[string]any{"has_steps": "true"})
	if err != nil {
		t.Fatal(err)
	}
	items = page["items"].([]map[string]any)
	if len(items) != 1 || items[0]["id"] != "ready" {
		t.Fatalf("wrong readiness results: %#v", page)
	}
}
func TestOverviewRespectsTeamScope(t *testing.T) {
	e, _ := historyFixture(t)
	overview, err := e.Overview(scopedHistoryCtx("team-infra"))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"open": "grp-infra", "integrations": "int-infra"} {
		page := overview[key].(map[string]any)
		items := page["items"].([]map[string]any)
		if len(items) != 1 || items[0]["id"] != want {
			t.Fatalf("%s leaks scope: %#v", key, page)
		}
	}
}
