package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
)

// Until now an inbound ChatOps command could act on any alert group whose id it
// named: the handler took the group by id and never asked who owned it. A chat
// bound to one team could therefore acknowledge another team's incident, and
// the whole surface authenticated as a single service principal, so the audit
// trail said "telegram" rather than naming a person.
//
// These tests pin the two boundaries that now apply — the actor's teams and the
// channel's team — plus the role check that becomes load-bearing once a real
// user, and not a fixed responder principal, stands behind the command.

// chatopsFixture seeds one channel, one integration and one open group, wiring
// the group to the integration so the ownership lookup has something to find.
func chatopsFixture(channelTeam, integrationTeam string) *memStore {
	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id":               "chn-1",
		"platform":         "telegram",
		"name":             "-100500",
		"external_id":      "-100500",
		"team_id":          nilIfEmpty(channelTeam),
		"commands_enabled": true,
	})
	ms.seed("integrations", map[string]any{
		"id":      "int-1",
		"name":    "prod",
		"team_id": nilIfEmpty(integrationTeam),
	})
	ms.seed("alert_groups", map[string]any{
		"id":             "grp-1",
		"status":         "open",
		"title":          "disk full",
		"integration_id": "int-1",
		"logs":           []any{},
	})
	return ms
}

// scopedCtx is a person limited to teamIDs, as the session and ChatOps paths
// both build them.
func scopedCtx(role authz.Role, teamIDs ...string) context.Context {
	return authz.NewContext(context.Background(), authz.Actor{
		ID:          "usr-bob",
		Kind:        authz.KindUser,
		DisplayName: "bob",
		Role:        role,
		TeamIDs:     teamIDs,
		TeamScoped:  true,
	})
}

func postCommand(t *testing.T, e *Engine, ctx context.Context, command string) (map[string]any, error) {
	t.Helper()
	return e.PostChatopsCommand(ctx, map[string]any{
		"channel_id": "chn-1",
		"command":    command,
		"actor":      "bob",
	})
}

func TestChatopsAckRefusesGroupOutsideActorTeams(t *testing.T) {
	ms := chatopsFixture("", "team-payments")
	e := crudEngine(ms)

	_, err := postCommand(t, e, scopedCtx(authz.RoleResponder, "team-infra"), "ack grp-1")
	if err == nil {
		t.Fatal("ack succeeded across a team boundary; it must be refused")
	}
	if !isForbidden(err) {
		t.Fatalf("err = %v, want a forbidden error", err)
	}
	if status := groupStatus(t, ms, "grp-1"); status != "open" {
		t.Errorf("group status = %q, want it left open", status)
	}
}

// A chat bound to a team is a context, not just a filter: belonging to the
// group's team elsewhere does not make it actionable from another team's chat.
func TestChatopsAckRefusesGroupOutsideChannelTeam(t *testing.T) {
	ms := chatopsFixture("team-infra", "team-payments")
	e := crudEngine(ms)

	_, err := postCommand(t, e, scopedCtx(authz.RoleResponder, "team-infra", "team-payments"), "ack grp-1")
	if err == nil {
		t.Fatal("ack succeeded from a chat bound to another team; it must be refused")
	}
	if !isForbidden(err) {
		t.Fatalf("err = %v, want a forbidden error", err)
	}
}

func TestChatopsAckAllowsGroupInsideBothBoundaries(t *testing.T) {
	ms := chatopsFixture("team-infra", "team-infra")
	e := crudEngine(ms)

	if _, err := postCommand(t, e, scopedCtx(authz.RoleResponder, "team-infra"), "ack grp-1"); err != nil {
		t.Fatalf("ack inside both boundaries must succeed, got %v", err)
	}
	if status := groupStatus(t, ms, "grp-1"); status != "acknowledged" {
		t.Errorf("group status = %q, want acknowledged", status)
	}
}

// Scoping must stay a no-op where nobody assigned teams, otherwise turning it
// on would break every existing installation.
func TestChatopsAckAllowsUnassignedIntegration(t *testing.T) {
	ms := chatopsFixture("team-infra", "")
	e := crudEngine(ms)

	if _, err := postCommand(t, e, scopedCtx(authz.RoleResponder, "team-infra"), "ack grp-1"); err != nil {
		t.Fatalf("an unassigned integration must stay reachable, got %v", err)
	}
}

// The inbound path used to authenticate as a fixed responder principal, so no
// role was ever consulted. With a real person behind the command, theirs is.
func TestChatopsAckRefusesViewerRole(t *testing.T) {
	ms := chatopsFixture("team-infra", "team-infra")
	e := crudEngine(ms)

	_, err := postCommand(t, e, scopedCtx(authz.RoleViewer, "team-infra"), "ack grp-1")
	if err == nil {
		t.Fatal("a viewer acknowledged an alert; the role must be enforced")
	}
	if !isForbidden(err) {
		t.Fatalf("err = %v, want a forbidden error", err)
	}
}

func TestChatopsResolveRefusesGroupOutsideActorTeams(t *testing.T) {
	ms := chatopsFixture("", "team-payments")
	e := crudEngine(ms)

	_, err := postCommand(t, e, scopedCtx(authz.RoleResponder, "team-infra"), "resolve grp-1")
	if err == nil {
		t.Fatal("resolve succeeded across a team boundary; it must be refused")
	}
	if status := groupStatus(t, ms, "grp-1"); status != "open" {
		t.Errorf("group status = %q, want it left open", status)
	}
}

// status listed every unresolved group in the deployment. It is a read, so the
// leak was quieter than the acknowledge one and no less real.
func TestChatopsStatusHidesGroupsOutsideBoundaries(t *testing.T) {
	ms := chatopsFixture("", "team-payments")
	e := crudEngine(ms)

	result, err := postCommand(t, e, scopedCtx(authz.RoleResponder, "team-infra"), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	response, _ := result["response"].(map[string]any)
	groups, _ := response["open_alert_groups"].([]map[string]any)
	if len(groups) != 0 {
		t.Errorf("status listed %d group(s) from another team, want none", len(groups))
	}
}

func TestChatopsStatusShowsOwnTeamGroups(t *testing.T) {
	ms := chatopsFixture("team-infra", "team-infra")
	e := crudEngine(ms)

	result, err := postCommand(t, e, scopedCtx(authz.RoleResponder, "team-infra"), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	response, _ := result["response"].(map[string]any)
	groups, _ := response["open_alert_groups"].([]map[string]any)
	if len(groups) != 1 {
		t.Fatalf("status listed %d group(s), want the one in this team", len(groups))
	}
}

// A group with no integration, or one whose integration is gone, carries no
// team — so no team boundary is crossed and it stays reachable. This mirrors
// authorizeItem: the same group must be actionable from the chat and from the
// REST API, or a responder's options depend on where they read the alert.
//
// The stricter reading — treat an unknown owner as forbidden — was tried and
// reverted; it hid groups that were never attached to an integration at all,
// which is an ordinary state, not a dangling reference.
func TestChatopsAckAllowsGroupWithNothingToScopeBy(t *testing.T) {
	ms := chatopsFixture("team-infra", "team-infra")
	ms.seed("alert_groups", map[string]any{
		"id": "grp-orphan", "status": "open", "title": "orphan",
		"integration_id": "int-gone", "logs": []any{},
	})
	ms.seed("alert_groups", map[string]any{
		"id": "grp-loose", "status": "open", "title": "no integration at all",
		"logs": []any{},
	})
	e := crudEngine(ms)

	for _, id := range []string{"grp-orphan", "grp-loose"} {
		if _, err := postCommand(t, e, scopedCtx(authz.RoleResponder, "team-infra"), "ack "+id); err != nil {
			t.Errorf("ack %s: %v — a group carrying no team must stay reachable", id, err)
		}
	}
}

// An actor that is not team-scoped — an API key, an admin — keeps reaching
// everything, which is the contract the rest of the service already follows.
func TestChatopsAckAllowsUnscopedActor(t *testing.T) {
	ms := chatopsFixture("", "team-payments")
	e := crudEngine(ms)
	ctx := authz.NewContext(context.Background(), authz.Actor{
		ID:          "chatops:telegram",
		Kind:        authz.KindService,
		DisplayName: "telegram (signed webhook)",
		Role:        authz.RoleResponder,
	})

	if _, err := postCommand(t, e, ctx, "ack grp-1"); err != nil {
		t.Fatalf("an unscoped service principal must keep working, got %v", err)
	}
}

func groupStatus(t *testing.T, ms *memStore, id string) string {
	t.Helper()
	rows := ms.data["alert_groups"]
	row, ok := rows[id]
	if !ok {
		t.Fatalf("alert group %s missing from the store", id)
	}
	status, _ := row["status"].(string)
	return status
}

// status answered "how many" by returning every open group whole, logs
// included, and the reply is stored with the chat message: 10 MB per call and
// 1.7 MB of database on a stand with 7,000 open groups. It now carries the
// exact count and a short, newest-first list of briefs.
func TestChatopsStatusReplyIsBounded(t *testing.T) {
	ms := chatopsFixture("", "")
	bigLogs := make([]any, 20)
	for i := range bigLogs {
		bigLogs[i] = map[string]any{"id": fmt.Sprintf("log-%d", i), "message": strings.Repeat("x", 200)}
	}
	for i := 0; i < statusListLimit+10; i++ {
		ms.seed("alert_groups", map[string]any{
			"id": fmt.Sprintf("grp-many-%03d", i), "status": "open", "title": fmt.Sprintf("group %d", i),
			"severity": "critical", "integration_id": "int-1", "logs": bigLogs,
			"last_received_at": fmt.Sprintf("2026-09-26T10:%02d:00Z", i%60),
		})
	}
	e := crudEngine(ms)

	result, err := postCommand(t, e, scopedCtx(authz.RoleResponder), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	response, _ := result["response"].(map[string]any)
	total := statusListLimit + 11 // the fixture's own group and the seeded ones
	if response["open_count"] != total || response["text"] != fmt.Sprintf("Open alert groups: %d", total) {
		t.Errorf("count = %v, text = %v, want %d", response["open_count"], response["text"], total)
	}
	groups, _ := response["open_alert_groups"].([]map[string]any)
	if len(groups) != statusListLimit || response["truncated"] != true {
		t.Errorf("listed %d, truncated %v; want %d and true", len(groups), response["truncated"], statusListLimit)
	}
	for _, g := range groups {
		if len(g) != 4 || g["logs"] != nil {
			t.Fatalf("a listed group is not a brief: %v", g)
		}
	}
	// And the stored chat message is the same small reply.
	stored := ms.row("chatops_messages", result["id"].(string))
	if stored == nil {
		t.Fatal("the chat message was not stored")
	}
	if b, _ := json.Marshal(stored); strings.Contains(string(b), "log-0") {
		t.Fatal("the stored chat message still carries group logs")
	}
}
