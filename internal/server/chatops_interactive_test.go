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

func TestSlackButtonRejectsAnUnknownAction(t *testing.T) {
	srv, st := interactiveServer(t, "slack")

	tapSlackButton(t, srv, "delete", "grp-1")

	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want an unrecognised action to change nothing", got)
	}
}

func TestMattermostButtonResolves(t *testing.T) {
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
	props, _ := update["props"].(map[string]any)
	attachments, _ := props["attachments"].([]any)
	if len(attachments) != 0 {
		t.Errorf("attachments = %+v, want them emptied so the buttons go", attachments)
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
