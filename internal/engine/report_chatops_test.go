package engine

import (
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
)

func reportFixture(channelTeam string) *memStore {
	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id":               "chn-1",
		"platform":         "telegram",
		"name":             "-100500",
		"external_id":      "-100500",
		"team_id":          nilIfEmpty(channelTeam),
		"commands_enabled": true,
	})
	return ms
}

func TestChatopsReportReturnsLatestVisibleReport(t *testing.T) {
	ms := reportFixture("")
	ms.seed("reports", map[string]any{
		"id": "rpt_team-a_20260901", "team_id": "team-a",
		"generated_at": "2026-09-01T00:00:00Z", "summary_text": "older team-a digest",
	})
	ms.seed("reports", map[string]any{
		"id": "rpt__all_20260908", "team_id": nil,
		"generated_at": "2026-09-08T00:00:00Z", "summary_text": "newer installation-wide digest",
	})
	eng := New(ms)

	got, err := postCommand(t, eng, scopedCtx(authz.RoleAdmin, "team-a"), "report")
	if err != nil {
		t.Fatalf("report command: %v", err)
	}
	msg, _ := got["response"].(map[string]any)
	text, _ := msg["text"].(string)
	if !strings.Contains(text, "older team-a digest") || strings.Contains(text, "newer installation-wide digest") {
		t.Fatalf("expected the more recent report, got: %q", text)
	}
}

func TestChatopsReportRefusesForeignTeam(t *testing.T) {
	ms := reportFixture("")
	ms.seed("reports", map[string]any{
		"id": "rpt_team-b_20260901", "team_id": "team-b",
		"generated_at": "2026-09-01T00:00:00Z", "summary_text": "team-b digest",
	})
	eng := New(ms)

	got, err := postCommand(t, eng, scopedCtx(authz.RoleAdmin, "team-a"), "report")
	if err != nil {
		t.Fatalf("report command: %v", err)
	}
	msg, _ := got["response"].(map[string]any)
	text, _ := msg["text"].(string)
	if strings.Contains(text, "team-b digest") {
		t.Fatalf("a team-a actor must not see team-b's report, got: %q", text)
	}
	if !strings.Contains(text, "No on-call quality report") {
		t.Fatalf("expected the no-report message, got: %q", text)
	}
}

func TestChatopsReportRespectsChannelTeamNarrowing(t *testing.T) {
	// The channel is bound to team-b; an admin who also belongs to team-a
	// must not see team-a's report through this channel (chatopsReportAccess
	// mirrors chatopsGroupAccess's channel-narrowing rule).
	ms := reportFixture("team-b")
	ms.seed("reports", map[string]any{
		"id": "rpt_team-a_20260901", "team_id": "team-a",
		"generated_at": "2026-09-01T00:00:00Z", "summary_text": "team-a digest",
	})
	eng := New(ms)

	got, err := postCommand(t, eng, scopedCtx(authz.RoleAdmin, "team-a", "team-b"), "report")
	if err != nil {
		t.Fatalf("report command: %v", err)
	}
	msg, _ := got["response"].(map[string]any)
	text, _ := msg["text"].(string)
	if strings.Contains(text, "team-a digest") {
		t.Fatalf("a channel bound to team-b must not surface team-a's report, got: %q", text)
	}
}

func TestChatopsReportNeverSendsGlobalDigestToTeamChannel(t *testing.T) {
	if chatopsReportAccess(map[string]any{"team_id": "team-a"}, authz.Actor{Role: authz.RoleAdmin}, "") {
		t.Fatal("global digest must not be broadcast to a team channel")
	}
}
