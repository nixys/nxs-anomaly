package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// A button on Slack or Mattermost has to reach the same verdict as the same
// button on Telegram and as the typed command. These tests drive the real
// handlers and assert on the stored alert group, not on the reply.

const (
	slackSecret      = "slack-signing-secret"
	mattermostSecret = "mattermost-action-secret"
)

func interactiveServer(t *testing.T, platform string) (*Server, *storetest.Store) {
	t.Helper()
	srv, st := newTestServer()
	srv.chatopsInbound = chatopsInboundConfig{
		slackSigningSecret:     slackSecret,
		mattermostActionSecret: mattermostSecret,
	}
	st.Seed("chatops_channels", map[string]any{
		"id": "chn-1", "platform": platform, "external_id": "C123", "commands_enabled": true,
	})
	st.Seed("integrations", map[string]any{"id": "int-1", "name": "prod"})
	st.Seed("alert_groups", map[string]any{
		"id": "grp-1", "status": "open", "title": "disk full",
		"integration_id": "int-1", "logs": []any{},
	})
	return srv, st
}

// tapSlackButton posts the interactive payload Slack sends, signed as Slack
// signs it.
func tapSlackButton(t *testing.T, srv *Server, actionID, value string) map[string]any {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type":    "block_actions",
		"user":    map[string]any{"id": "U1", "username": "bob"},
		"channel": map[string]any{"id": "C123"},
		"message": map[string]any{"text": "[critical] disk full"},
		"actions": []any{map[string]any{"action_id": actionID, "value": value}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	body := "payload=" + url.QueryEscape(string(payload))
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(slackSecret))
	// Writing to an hmac.Hash never returns an error; the discard mirrors the
	// production signer.
	_, _ = fmt.Fprintf(mac, "v0:%s:%s", ts, body)

	r := httptest.NewRequest(http.MethodPost, "/integrations/v1/chatops/slack/interactive", strings.NewReader(body))
	r.Header.Set("X-Slack-Request-Timestamp", ts)
	r.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	srv.handleSlackInteractive(w, r)

	var reply map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v (body %q, status %d)", err, w.Body.String(), w.Code)
	}
	return reply
}

func tapMattermostButton(t *testing.T, srv *Server, action, groupID, token string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"user_name":  "bob",
		"channel_id": "C123",
		"post_id":    "P1",
		"context":    map[string]any{"action": action, "group_id": groupID, "token": token},
	})
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/integrations/v1/chatops/mattermost", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.handleMattermostAction(w, r)

	var reply map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	return w, reply
}

func TestSlackButtonAcknowledges(t *testing.T) {
	srv, st := interactiveServer(t, "slack")

	reply := tapSlackButton(t, srv, "ack", "grp-1")

	if got := st.Row("alert_groups", "grp-1")["status"]; got != "acknowledged" {
		t.Fatalf("group status = %v, want acknowledged", got)
	}
	// The message is replaced so it stops offering a button for work already done.
	if reply["replace_original"] != true {
		t.Errorf("replace_original = %v, want the message replaced", reply["replace_original"])
	}
	if text, _ := reply["text"].(string); !strings.Contains(text, "Acknowledged") {
		t.Errorf("text = %q, want the verdict", text)
	}
}

func TestSlackButtonRequiresAValidSignature(t *testing.T) {
	srv, st := interactiveServer(t, "slack")

	r := httptest.NewRequest(http.MethodPost, "/integrations/v1/chatops/slack/interactive",
		strings.NewReader("payload=%7B%7D"))
	r.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	r.Header.Set("X-Slack-Signature", "v0=deadbeef")
	w := httptest.NewRecorder()
	srv.handleSlackInteractive(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched", got)
	}
}

// A refusal leaves the message alone: the next person to look should still be
// able to act on it.
func TestSlackButtonKeepsTheMessageWhenRefused(t *testing.T) {
	srv, st := interactiveServer(t, "slack")

	reply := tapSlackButton(t, srv, "ack", "grp-missing")

	if reply["replace_original"] != false {
		t.Errorf("replace_original = %v, want the message kept after a refusal", reply["replace_original"])
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched", got)
	}
}

// The message used to be replaced by the verdict alone — "Acknowledged grp-1" —
// which threw away the incident the channel had been reading and left the id,
// which nobody reads.
func TestSlackSettledMessageKeepsTheAlertAndOffersTheUndo(t *testing.T) {
	srv, _ := interactiveServer(t, "slack")

	reply := tapSlackButton(t, srv, "ack", "grp-1")

	if reply["replace_original"] != true {
		t.Fatalf("replace_original = %v; an acknowledged alert must stop offering Acknowledge", reply["replace_original"])
	}
	text, _ := reply["text"].(string)
	if !strings.Contains(text, "disk full") {
		t.Errorf("text = %q, want the alert kept", text)
	}
	if !strings.Contains(text, "Acknowledged") {
		t.Errorf("text = %q, want the verdict appended", text)
	}
	blocks, _ := reply["blocks"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %+v, want the text and the undo", blocks)
	}
	elements, _ := blocks[1].(map[string]any)["elements"].([]any)
	if len(elements) != 1 {
		t.Fatalf("got %d button(s) on a settled alert, want just the undo", len(elements))
	}
	if got := elements[0].(map[string]any)["action_id"]; got != "unack" {
		t.Errorf("undo action = %v, want unack", got)
	}
}

// A silence has nothing to take back — it lapses on its own — so the settled
// message carries no button at all.
func TestSlackSettledSilenceOffersNoUndo(t *testing.T) {
	srv, _ := interactiveServer(t, "slack")

	reply := tapSlackButton(t, srv, "silence:60", "grp-1")

	blocks, _ := reply["blocks"].([]any)
	if len(blocks) != 1 {
		t.Errorf("blocks = %+v, want only the text", blocks)
	}
}

// A Slack tap used to run as the platform service principal, so the group's log
// named a bot as the actor.
func TestSlackButtonIsAttributedToTheLinkedUser(t *testing.T) {
	srv, st := interactiveServer(t, "slack")
	if err := st.UpsertItem(t.Context(), "users", map[string]any{
		"id": "usr-bob", "username": "bob",
		"slack_id": "U1", "role": string(authz.RoleResponder),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	tapSlackButton(t, srv, "ack", "grp-1")

	by, _ := st.Row("alert_groups", "grp-1")["acknowledged_by"].(map[string]any)
	if by == nil || by["id"] != "usr-bob" {
		t.Errorf("acknowledged_by = %v, want the person who tapped", by)
	}
}

func TestSlackButtonRejectsAnUnknownAction(t *testing.T) {
	srv, st := interactiveServer(t, "slack")

	tapSlackButton(t, srv, "delete", "grp-1")

	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want an unrecognised action to change nothing", got)
	}
}

func TestMattermostButtonResolves(t *testing.T) {
	// The undo button is a callback like any other Mattermost button, so it
	// needs the address this deployment answers on. Set before the engine is
	// built, which is where the delivery configuration is read.
	t.Setenv("NXS_ANOMALY_PUBLIC_URL", "https://alerts.example.com")
	srv, st := interactiveServer(t, "mattermost")

	w, reply := tapMattermostButton(t, srv, "resolve", "grp-1", mattermostSecret)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "resolved" {
		t.Fatalf("group status = %v, want resolved", got)
	}
	update, ok := reply["update"].(map[string]any)
	if !ok {
		t.Fatalf("no update in reply: %+v", reply)
	}
	// The post keeps its message: an update that does not name one leaves it
	// alone. Replacing it with the verdict — which is what this used to do —
	// threw away the incident the channel had been reading and left the id.
	if _, replaced := update["message"]; replaced {
		t.Errorf("update names a message (%v); the alert text must be left as it stands", update["message"])
	}
	props, _ := update["props"].(map[string]any)
	attachments, _ := props["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("attachments = %+v, want exactly the verdict", attachments)
	}
	attachment, _ := attachments[0].(map[string]any)
	if text, _ := attachment["text"].(string); !strings.Contains(text, "Resolved") {
		t.Errorf("attachment text = %q, want the verdict", text)
	}
	// The verdict's own attachment carries the way back, and nothing else.
	actions, _ := attachment["actions"].([]any)
	if len(actions) != 1 {
		t.Fatalf("attachment has %d action(s), want just the undo", len(actions))
	}
	if got := actions[0].(map[string]any)["id"]; got != "unresolve" {
		t.Errorf("undo action = %v, want unresolve", got)
	}
}

// Mattermost does not sign its callbacks, so the token in the context is the
// only thing standing between this endpoint and anyone who learned the URL.
func TestMattermostButtonRequiresTheActionToken(t *testing.T) {
	srv, st := interactiveServer(t, "mattermost")

	w, _ := tapMattermostButton(t, srv, "ack", "grp-1", "wrong")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched", got)
	}
}

func TestMattermostButtonIsRefusedWhenUnconfigured(t *testing.T) {
	srv, st := interactiveServer(t, "mattermost")
	srv.chatopsInbound.mattermostActionSecret = ""

	w, _ := tapMattermostButton(t, srv, "ack", "grp-1", "anything")

	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 while no secret is configured", w.Code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched", got)
	}
}

// A refused tap tells only the person who made it and leaves the post — and its
// buttons — as they were.
func TestMattermostButtonKeepsThePostWhenRefused(t *testing.T) {
	srv, st := interactiveServer(t, "mattermost")
	st.Seed("users", map[string]any{"id": "usr-vera", "username": "vera", "role": string(authz.RoleViewer)})

	_, reply := tapMattermostButton(t, srv, "ack", "grp-missing", mattermostSecret)

	if _, present := reply["update"]; present {
		t.Errorf("the post was updated after a refusal: %+v", reply["update"])
	}
	if reply["ephemeral_text"] == nil {
		t.Error("the person who tapped was told nothing")
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched", got)
	}
}
