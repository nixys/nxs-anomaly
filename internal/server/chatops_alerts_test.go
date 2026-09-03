package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// Typing a command into Telegram used to answer nothing: the webhook replied
// with a JSON body Telegram ignores unless it is a method call. These tests
// drive the real handler and assert on what Telegram would actually be told.

// alertsServer seeds exactly the groups a test asks for, so a count in an
// assertion is the count the test wrote and not one inherited from a fixture.
func alertsServer(t *testing.T, groups int) (*Server, *storetest.Store) {
	t.Helper()
	srv, st := newTestServer()
	srv.chatopsInbound = chatopsInboundConfig{telegramSecretToken: callbackSecret}
	st.Seed("chatops_channels", map[string]any{
		"id":               "chn-1",
		"platform":         "telegram",
		"name":             "-100500",
		"external_id":      "-100500",
		"commands_enabled": true,
	})
	st.Seed("integrations", map[string]any{"id": "int-1", "name": "prod"})
	for i := 0; i < groups; i++ {
		st.Seed("alert_groups", map[string]any{
			"id":               fmt.Sprintf("grp-%02d", i),
			"status":           "open",
			"title":            fmt.Sprintf("alert %d", i),
			"severity":         "warning",
			"integration_id":   "int-1",
			"last_received_at": fmt.Sprintf("2026-08-01T10:%02d:00Z", i),
			"logs":             []any{},
		})
	}
	return srv, st
}

// sendCommand posts the update Telegram sends when a person types a command.
func sendCommand(t *testing.T, srv *Server, senderID int64, text string) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"text": text,
			"from": map[string]any{"id": senderID, "username": "bob"},
			"chat": map[string]any{"id": -100500},
		},
	})
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/integrations/v1/chatops/telegram", strings.NewReader(string(body)))
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", callbackSecret)
	w := httptest.NewRecorder()
	srv.handleTelegramCommand(w, r)

	var reply map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v (body %q)", err, w.Body.String())
	}
	return reply
}

// tapPager posts a tap on a navigation button, including the message it sits on.
func tapPager(t *testing.T, srv *Server, senderID int64, data string, messageID int64) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"callback_query": map[string]any{
			"id":   "cbq-1",
			"data": data,
			"from": map[string]any{"id": senderID, "username": "bob"},
			"message": map[string]any{
				"message_id": messageID,
				"chat":       map[string]any{"id": -100500},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/integrations/v1/chatops/telegram", strings.NewReader(string(body)))
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", callbackSecret)
	w := httptest.NewRecorder()
	srv.handleTelegramCommand(w, r)

	var reply map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v (body %q)", err, w.Body.String())
	}
	return reply
}

func keyboardRows(t *testing.T, reply map[string]any) []any {
	t.Helper()
	markup, ok := reply["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("no keyboard in reply: %+v", reply)
	}
	rows, _ := markup["inline_keyboard"].([]any)
	return rows
}

func TestTelegramCommandAnswersWithAMethodCall(t *testing.T) {
	srv, st := alertsServer(t, 2)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	reply := sendCommand(t, srv, 4242, "/alerts")

	if reply["method"] != "sendMessage" {
		t.Fatalf("method = %v, want sendMessage; anything else and Telegram shows nothing", reply["method"])
	}
	if reply["chat_id"] != "-100500" {
		t.Errorf("chat_id = %v, want the chat the command came from", reply["chat_id"])
	}
	if text, _ := reply["text"].(string); !strings.Contains(text, "2") {
		t.Errorf("text = %q, want it to name the two open groups", text)
	}
}

func TestTelegramAlertsListsOneButtonPerGroup(t *testing.T) {
	srv, st := alertsServer(t, 3)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	rows := keyboardRows(t, sendCommand(t, srv, 4242, "/alerts"))

	if len(rows) != 3 {
		t.Fatalf("keyboard has %d row(s), want one per open group", len(rows))
	}
}

// Twelve groups do not fit on a phone; the list pages, and the tap redraws the
// message it sits on rather than posting another copy of the list.
func TestTelegramAlertsPagesInPlace(t *testing.T) {
	srv, st := alertsServer(t, 12)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	first := sendCommand(t, srv, 4242, "/alerts")
	if text, _ := first["text"].(string); !strings.Contains(text, "page 1 of 3") {
		t.Errorf("text = %q, want it to state the page", text)
	}

	second := tapPager(t, srv, 4242, "alerts:2", 777)
	if second["method"] != "editMessageText" {
		t.Fatalf("method = %v, want editMessageText so the list is redrawn in place", second["method"])
	}
	if second["message_id"] != float64(777) {
		t.Errorf("message_id = %v, want the message the button sits on", second["message_id"])
	}
	if text, _ := second["text"].(string); !strings.Contains(text, "page 2 of 3") {
		t.Errorf("text = %q, want page 2", text)
	}
}

// The listing is a read, and reads obey the same boundary as the actions: a
// responder whose teams do not reach the integration sees nothing.
func TestTelegramAlertsHidesGroupsOutsideTheActorsTeams(t *testing.T) {
	srv, st := alertsServer(t, 3)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))
	if err := st.UpsertItem(t.Context(), "integrations", map[string]any{
		"id": "int-1", "name": "prod", "team_id": "team-payments",
	}); err != nil {
		t.Fatalf("assign the integration to a team: %v", err)
	}
	if err := st.UpsertItem(t.Context(), "teams", map[string]any{
		"id": "team-infra", "name": "infra", "member_ids": []any{"usr-bob"},
	}); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	srv.cfg.TeamScoping = true

	reply := sendCommand(t, srv, 4242, "/alerts")

	if text, _ := reply["text"].(string); !strings.Contains(text, "No open alert groups") {
		t.Errorf("text = %q, want nothing listed from another team", text)
	}
	if _, present := reply["reply_markup"]; present {
		t.Errorf("keyboard offered for groups the responder may not touch: %+v", reply["reply_markup"])
	}
}
