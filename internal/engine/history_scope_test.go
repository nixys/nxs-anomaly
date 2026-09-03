package engine

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// GetHistory returns alert groups with their notifications and delivery
// attempts inlined, and a notification carries its target — somebody's Telegram
// id, phone number or address. It was the one read that ignored team scoping
// entirely; these tests pin that it no longer does.

func historyFixture(t *testing.T) (*Engine, *storetest.Store) {
	t.Helper()
	st := storetest.New()
	st.Seed("integrations", map[string]any{"id": "int-infra", "name": "infra", "team_id": "team-infra"})
	st.Seed("integrations", map[string]any{"id": "int-pay", "name": "payments", "team_id": "team-payments"})
	st.Seed("alert_groups",
		map[string]any{"id": "grp-infra", "status": "open", "integration_id": "int-infra", "logs": []any{}},
		map[string]any{"id": "grp-pay", "status": "open", "integration_id": "int-pay", "logs": []any{}},
	)
	// The payload the leak actually exposed: a colleague's contact details.
	st.Seed("notifications", map[string]any{
		"id": "ntf-pay", "alert_group_id": "grp-pay", "integration_id": "int-pay",
		"channel": "telegram", "target": "4343", "status": "delivered",
	})
	return New(st), st
}

func historyGroupIDs(t *testing.T, res map[string]any) []string {
	t.Helper()
	items, _ := res["items"].([]map[string]any)
	var ids []string
	for _, item := range items {
		group, _ := item["alert_group"].(map[string]any)
		if id, _ := group["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func scopedHistoryCtx(teams ...string) context.Context {
	return authz.NewContext(context.Background(), authz.Actor{
		ID: "usr-bob", Kind: authz.KindUser, Role: authz.RoleResponder,
		TeamIDs: teams, TeamScoped: true,
	})
}

func TestHistoryHidesOtherTeamsGroups(t *testing.T) {
	e, _ := historyFixture(t)

	res, err := e.GetHistory(scopedHistoryCtx("team-infra"), map[string]any{})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	ids := historyGroupIDs(t, res)
	if len(ids) != 1 || ids[0] != "grp-infra" {
		t.Fatalf("history returned %v, want only this team's group", ids)
	}
}

// An unscoped caller — an admin, an API key, the worker — keeps seeing
// everything, which is the contract the rest of the service already follows.
func TestHistoryUnscopedActorSeesEverything(t *testing.T) {
	e, _ := historyFixture(t)
	ctx := authz.NewContext(context.Background(), authz.Actor{
		ID: "key", Kind: authz.KindService, Role: authz.RoleAdmin,
	})

	res, err := e.GetHistory(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	if ids := historyGroupIDs(t, res); len(ids) != 2 {
		t.Errorf("history returned %v, want both groups", ids)
	}
}

// Asking for one integration outside your teams must yield nothing, not
// everything: the scope intersects the caller's filter rather than being
// replaced by it.
func TestHistoryFilterOutsideTeamsYieldsNothing(t *testing.T) {
	e, _ := historyFixture(t)

	res, err := e.GetHistory(scopedHistoryCtx("team-infra"), map[string]any{"integration": "int-pay"})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	if ids := historyGroupIDs(t, res); len(ids) != 0 {
		t.Errorf("history returned %v for another team's integration, want nothing", ids)
	}
}

// Belonging to no team means seeing the unassigned objects and nothing else;
// here there are none, so the answer is empty rather than unrestricted.
func TestHistoryActorWithoutTeamsSeesNothing(t *testing.T) {
	e, _ := historyFixture(t)

	res, err := e.GetHistory(scopedHistoryCtx(), map[string]any{})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	if ids := historyGroupIDs(t, res); len(ids) != 0 {
		t.Errorf("history returned %v to an actor in no team, want nothing", ids)
	}
}

// An integration with no team stays visible to everyone — that is what keeps
// switching scoping on a no-op where nobody assigned teams.
func TestHistoryUnassignedIntegrationStaysVisible(t *testing.T) {
	e, st := historyFixture(t)
	st.Seed("integrations", map[string]any{"id": "int-loose", "name": "loose"})
	st.Seed("alert_groups", map[string]any{
		"id": "grp-loose", "status": "open", "integration_id": "int-loose", "logs": []any{},
	})

	res, err := e.GetHistory(scopedHistoryCtx("team-infra"), map[string]any{})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	ids := historyGroupIDs(t, res)
	found := false
	for _, id := range ids {
		if id == "grp-loose" {
			found = true
		}
	}
	if !found {
		t.Errorf("history returned %v, want the unassigned group among them", ids)
	}
}
