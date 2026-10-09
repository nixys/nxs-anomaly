package tests

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func chatopsAdmin(ctx context.Context) context.Context {
	return authz.NewContext(ctx, authz.Actor{ID: "usr-admin", Kind: "user", Role: authz.RoleAdmin})
}

// pagedChatopsGroup sets up a user bound to a ChatOps channel posting to
// chatURL, a chain that pages the user, and ingests one alert. It returns the
// group, the user and the chain.
func pagedChatopsGroup(t *testing.T, ctx context.Context, eng *engine.Engine, chatURL string, messageUpdate map[string]any) (groupID, userID, chainID string) {
	t.Helper()
	adminCtx := chatopsAdmin(ctx)
	user, err := eng.CreateUser(adminCtx, map[string]any{"name": "Chat User", "username": "chat-user"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	channel := map[string]any{
		"platform": "mattermost", "name": "#sre", "user_id": user["id"],
		"webhook_url": chatURL + "/hooks/1",
	}
	if messageUpdate != nil {
		channel["message_update"] = messageUpdate
	}
	if _, err := eng.CreateChatopsChannel(adminCtx, channel); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	chain, err := eng.CreateEscalationChain(adminCtx, map[string]any{
		"name":  "chatops-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{user["id"]}}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(adminCtx, map[string]any{
		"name": "chatops-integration",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	res, err := eng.IngestAlert(adminCtx, utils.StrVal(integ, "key"), map[string]any{
		"title": "disk full", "labels": map[string]any{"alertname": "disk"},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return utils.StrVal(res["group"].(map[string]any), "id"), utils.StrVal(user, "id"), utils.StrVal(chain, "id")
}

// chatopsRows returns the ChatOps notifications of a group.
func chatopsRows(t *testing.T, ctx context.Context, st store.PostgreSQLStore, groupID string) []model.Notification {
	t.Helper()
	rows, err := st.ListItemsIn(ctx, "notifications", "alert_group_id", []any{groupID})
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Notification
	for _, row := range rows {
		if n := model.WrapNotification(row); n.Channel() == "chatops" {
			out = append(out, n)
		}
	}
	return out
}

// The channel's message survives the member it was sent on behalf of.
func TestChatopsMessageSurvivesTheMemberInPostgres(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	groupID, userID, chainID := pagedChatopsGroup(t, ctx, eng, "https://chat.example.com", nil)
	if got := len(chatopsRows(t, ctx, st, groupID)); got != 1 {
		t.Fatalf("%d channel messages before the delete, want 1", got)
	}
	// The chain names the user; a person still on a chain cannot be deleted.
	if _, err := eng.UpdateEscalationChain(chatopsAdmin(ctx), chainID, map[string]any{"steps": []any{}}); err != nil {
		t.Fatalf("empty chain: %v", err)
	}
	if _, err := eng.DeleteEntity(chatopsAdmin(ctx), "users", userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if got := len(chatopsRows(t, ctx, st, groupID)); got != 1 {
		t.Errorf("%d channel messages after the member was deleted, want 1", got)
	}
}
