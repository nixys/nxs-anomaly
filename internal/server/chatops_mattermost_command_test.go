package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// The Mattermost bot was one-way: it could show two buttons and take a tap, and
// there was no endpoint to type anything at. Every command the engine supports
// was reachable from Telegram and Slack and from nowhere else.

const mattermostCommandToken = "mm-command-token"

func mattermostCommandServer(t *testing.T) (*Server, *storetest.Store) {
	t.Helper()
	srv, st := newTestServer()
	srv.chatopsInbound = chatopsInboundConfig{
		mattermostActionSecret: mattermostSecret,
		mattermostCommandToken: mattermostCommandToken,
	}
	st.Seed("chatops_channels", map[string]any{
		"id": "chn-1", "platform": "mattermost", "external_id": "C123", "commands_enabled": true,
	})
	st.Seed("integrations", map[string]any{"id": "int-1", "name": "prod"})
	st.Seed("alert_groups", map[string]any{
		"id": "grp-1", "status": "open", "title": "disk full",
		"severity": "critical", "integration_id": "int-1", "logs": []any{},
	})
	return srv, st
}

func sendMattermostCommand(t *testing.T, srv *Server, token, userID, text string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	form := url.Values{
		"token":      {token},
		"channel_id": {"C123"},
		"user_id":    {userID},
		"user_name":  {"bob"},
		"command":    {"/nxs"},
		"text":       {text},
	}
	r := httptest.NewRequest(http.MethodPost, "/integrations/v1/chatops/mattermost/command",
		strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.handleMattermostCommand(w, r)

	var reply map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v (body %q)", err, w.Body.String())
	}
	return w, reply
}

func TestMattermostCommandAcknowledges(t *testing.T) {
	srv, st := mattermostCommandServer(t)

	w, reply := sendMattermostCommand(t, srv, mattermostCommandToken, "mmu-1", "ack grp-1")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "acknowledged" {
		t.Fatalf("group status = %v, want acknowledged", got)
	}
	// Ephemeral: the answer belongs to whoever typed it, not to the channel.
	if reply["response_type"] != "ephemeral" {
		t.Errorf("response_type = %v, want ephemeral", reply["response_type"])
	}
	if text, _ := reply["text"].(string); !strings.Contains(text, "Acknowledged") {
		t.Errorf("text = %q, want the verdict", text)
	}
}

// Mattermost does not sign slash commands: the token it generated for the
// command is the only credential the request carries.
func TestMattermostCommandRequiresItsToken(t *testing.T) {
	srv, st := mattermostCommandServer(t)

	w, _ := sendMattermostCommand(t, srv, "wrong", "mmu-1", "ack grp-1")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched", got)
	}
}

func TestMattermostCommandIsRefusedWhenUnconfigured(t *testing.T) {
	srv, _ := mattermostCommandServer(t)
	srv.chatopsInbound.mattermostCommandToken = ""

	w, _ := sendMattermostCommand(t, srv, "", "mmu-1", "ack grp-1")

	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 naming the missing configuration", w.Code)
	}
}

// A refusal is answered with 200 in the platform's own shape, for the same
// reason the button path already does: a non-2xx is a delivery a chat platform
// retries, and re-delivering a refused command runs it again.
func TestMattermostCommandAnswersARefusalWithoutFailing(t *testing.T) {
	srv, _ := mattermostCommandServer(t)

	w, reply := sendMattermostCommand(t, srv, mattermostCommandToken, "mmu-1", "nonsense")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 so the command is not redelivered", w.Code)
	}
	if text, _ := reply["text"].(string); text == "" {
		t.Error("refusal carries no text; the person sees nothing")
	}
}

// Identity was a Telegram-only question: a Mattermost tap ran as the platform
// service principal, so the group's log said a bot had acknowledged.
func TestMattermostCommandIsAttributedToTheLinkedUser(t *testing.T) {
	srv, st := mattermostCommandServer(t)
	if err := st.UpsertItem(t.Context(), "users", map[string]any{
		"id": "usr-bob", "username": "bob",
		"mattermost_id": "mmu-1", "role": string(authz.RoleResponder),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	sendMattermostCommand(t, srv, mattermostCommandToken, "mmu-1", "ack grp-1")

	by, _ := st.Row("alert_groups", "grp-1")["acknowledged_by"].(map[string]any)
	if by == nil {
		t.Fatal("no attribution on the group; the acknowledgement is unattributable")
	}
	if by["id"] != "usr-bob" {
		t.Errorf("acknowledged_by = %v, want the person who typed it", by)
	}
	if by["kind"] != string(authz.KindUser) {
		t.Errorf("actor kind = %v, want a user rather than the bot", by["kind"])
	}
}

// A person whose role was cleared must not fall back to the service principal:
// that would grant rights they no longer hold.
func TestMattermostCommandRefusesAUserWithNoRole(t *testing.T) {
	srv, st := mattermostCommandServer(t)
	if err := st.UpsertItem(t.Context(), "users", map[string]any{
		"id": "usr-gone", "username": "gone", "mattermost_id": "mmu-1", "role": "",
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	sendMattermostCommand(t, srv, mattermostCommandToken, "mmu-1", "ack grp-1")

	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched", got)
	}
}
