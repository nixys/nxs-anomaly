package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// Tapping an inline button has to reach the same verdict as typing the command
// it stands for. A shortcut that skipped the identity, team or role checks would
// be a second, weaker way in — so these tests drive the real webhook handler and
// assert on the stored alert group, not on the reply.

const callbackSecret = "telegram-webhook-secret"

func callbackServer(t *testing.T) (*Server, *storetest.Store) {
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
	st.Seed("alert_groups", map[string]any{
		"id":             "grp-1",
		"status":         "open",
		"title":          "disk full",
		"integration_id": "int-1",
		"logs":           []any{},
	})
	return srv, st
}

// tapButton posts the update Telegram sends when an inline button is pressed.
// The message the button sits on is included, as Telegram always includes it.
func tapButton(t *testing.T, srv *Server, senderID int64, data string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"callback_query": map[string]any{
			"id":   "cbq-1",
			"data": data,
			"from": map[string]any{"id": senderID, "username": "bob"},
			"message": map[string]any{
				"message_id": 555,
				"text":       "disk full",
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
	return w
}

func TestTelegramCallbackAcknowledgesAsTheSender(t *testing.T) {
	srv, st := callbackServer(t)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	if code := tapButton(t, srv, 4242, "ack:grp-1").Code; code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "acknowledged" {
		t.Fatalf("group status = %v, want acknowledged", got)
	}
	// The point of resolving the sender: the trail names the engineer, not the bot.
	by, _ := st.Row("alert_groups", "grp-1")["acknowledged_by"].(map[string]any)
	if by["id"] != "usr-bob" {
		t.Errorf("acknowledged_by = %+v, want the person who tapped", by)
	}
}

func TestTelegramCallbackResolves(t *testing.T) {
	srv, st := callbackServer(t)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	tapButton(t, srv, 4242, "resolve:grp-1")

	if got := st.Row("alert_groups", "grp-1")["status"]; got != "resolved" {
		t.Errorf("group status = %v, want resolved", got)
	}
}

// The button is a shortcut for the command, so it inherits the command's role
// check rather than bypassing it.
func TestTelegramCallbackRefusesViewer(t *testing.T) {
	srv, st := callbackServer(t)
	seedTelegramUser(t, st, "usr-vera", "4343", string(authz.RoleViewer))

	tapButton(t, srv, 4343, "ack:grp-1")

	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("a viewer's tap changed the group to %v; the role must be enforced", got)
	}
}

// Telegram redelivers updates it could not deliver. A refusal is a final answer,
// not a transient failure: answering non-2xx would have the tap replayed, and a
// replayed tap re-runs the command.
func TestTelegramCallbackAlwaysAnswersOK(t *testing.T) {
	srv, st := callbackServer(t)
	seedTelegramUser(t, st, "usr-vera", "4343", string(authz.RoleViewer))

	for _, c := range []struct {
		name string
		data string
	}{
		{"refused by role", "ack:grp-1"},
		{"unknown group", "ack:grp-missing"},
		{"button this service never rendered", "delete:grp-1"},
		{"malformed data", "nonsense"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code := tapButton(t, srv, 4343, c.data).Code; code != http.StatusOK {
				t.Errorf("status = %d, want 200 so Telegram does not replay the tap", code)
			}
		})
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched by refused taps", got)
	}
}

// A tap from an account nobody claims still works: a shared duty chat is a
// legitimate setup, and the signed webhook already proved Telegram sent it.
func TestTelegramCallbackFromUnknownSenderUsesServicePrincipal(t *testing.T) {
	srv, st := callbackServer(t)

	tapButton(t, srv, 9999, "ack:grp-1")

	if got := st.Row("alert_groups", "grp-1")["status"]; got != "acknowledged" {
		t.Fatalf("group status = %v, want acknowledged", got)
	}
	by, _ := st.Row("alert_groups", "grp-1")["acknowledged_by"].(map[string]any)
	if by["id"] != "chatops:telegram" {
		t.Errorf("acknowledged_by = %+v, want the platform service principal", by)
	}
}

// The toast that confirms an acknowledge is gone within seconds; what stays on
// screen is the message. Leaving "Acknowledge" on it implies the work is still
// waiting, so the message is rewritten and the buttons dropped.
func TestTelegramCallbackSettlesTheAlertMessage(t *testing.T) {
	srv, st := callbackServer(t)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	var reply map[string]any
	if err := json.Unmarshal(tapButton(t, srv, 4242, "ack:grp-1").Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}

	if reply["method"] != "editMessageText" {
		t.Fatalf("method = %v, want editMessageText", reply["method"])
	}
	if reply["message_id"] != float64(555) {
		t.Errorf("message_id = %v, want the message the button sits on", reply["message_id"])
	}
	text, _ := reply["text"].(string)
	if !strings.Contains(text, "disk full") {
		t.Errorf("text = %q, want the original alert kept", text)
	}
	if !strings.Contains(text, "Acknowledged") {
		t.Errorf("text = %q, want the verdict appended", text)
	}
	markup, ok := reply["reply_markup"].(map[string]any)
	if !ok {
		t.Fatal("no reply_markup: the old buttons would stay on the message")
	}
	if rows, _ := markup["inline_keyboard"].([]any); len(rows) != 0 {
		t.Errorf("keyboard still has %d row(s), want it emptied", len(rows))
	}
}

// A refused tap changed nothing, so the message must keep its buttons: the next
// person to look at it should still be able to act.
func TestTelegramCallbackLeavesTheMessageAloneWhenRefused(t *testing.T) {
	srv, st := callbackServer(t)
	seedTelegramUser(t, st, "usr-vera", "4343", string(authz.RoleViewer))

	var reply map[string]any
	if err := json.Unmarshal(tapButton(t, srv, 4343, "ack:grp-1").Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}

	if reply["method"] != nil {
		t.Errorf("method = %v, want the message left untouched after a refusal", reply["method"])
	}
}

// The secret token is the credential for this endpoint; a tap without it must
// not reach the command path at all.
func TestTelegramCallbackRequiresSecretToken(t *testing.T) {
	srv, st := callbackServer(t)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	body := `{"callback_query":{"id":"cbq-1","data":"ack:grp-1","from":{"id":4242},"message":{"chat":{"id":-100500}}}}`
	r := httptest.NewRequest(http.MethodPost, "/integrations/v1/chatops/telegram", strings.NewReader(body))
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong")
	w := httptest.NewRecorder()
	srv.handleTelegramCommand(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched", got)
	}
}

// tapButtonInChat is tapButton with the chat the button sits in spelled out, so
// a private conversation with the bot can be distinguished from the configured
// group chat.
func tapButtonInChat(t *testing.T, srv *Server, chatID, senderID int64, data string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"callback_query": map[string]any{
			"id":   "cbq-1",
			"data": data,
			"from": map[string]any{"id": senderID, "username": "bob"},
			"message": map[string]any{
				"message_id": 555,
				"text":       "disk full",
				"chat":       map[string]any{"id": chatID},
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
	return w
}

// Personal notifications are delivered to a person's own chat with the bot, and
// they carry Acknowledge and Resolve. That chat is nobody's ChatOps channel, so
// every one of those taps used to come back "no telegram chatops channel is
// bound to 4242" and the buttons did nothing at all — the symptom being that
// the alert stayed open while the person was sure they had acknowledged it.
func TestTelegramCallbackInPrivateChatActsAsTheSender(t *testing.T) {
	srv, st := callbackServer(t)
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	if code := tapButtonInChat(t, srv, 4242, 4242, "ack:grp-1").Code; code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "acknowledged" {
		t.Fatalf("group status = %v, want acknowledged", got)
	}
	by, _ := st.Row("alert_groups", "grp-1")["acknowledged_by"].(map[string]any)
	if by["id"] != "usr-bob" {
		t.Errorf("acknowledged_by = %+v, want the person who tapped", by)
	}
}

// The unbound path is only open to somebody this deployment recognises. An
// unclaimed account falls back to the platform service principal, and letting
// that run commands from any chat would hand the right to acknowledge alerts to
// whoever adds the bot to a chat.
func TestTelegramCallbackInUnknownChatFromUnknownSenderIsRefused(t *testing.T) {
	srv, st := callbackServer(t)

	if code := tapButtonInChat(t, srv, 777, 777, "ack:grp-1").Code; code != http.StatusOK {
		t.Fatalf("status = %d, want 200 so Telegram does not replay the tap", code)
	}
	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("group status = %v, want it untouched", got)
	}
}

// The role check is the same on the unbound path: it is the person's own role
// that decides, not the fact that they reached the bot directly.
func TestTelegramCallbackInPrivateChatStillEnforcesRole(t *testing.T) {
	srv, st := callbackServer(t)
	seedTelegramUser(t, st, "usr-vera", "4343", string(authz.RoleViewer))

	tapButtonInChat(t, srv, 4343, 4343, "ack:grp-1")

	if got := st.Row("alert_groups", "grp-1")["status"]; got != "open" {
		t.Errorf("a viewer's tap in a private chat changed the group to %v", got)
	}
}
