package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buttonChannel delivers one ChatOps notification to a channel of the given
// platform and returns the body that was posted.
func buttonChannel(t *testing.T, platform string, interactive bool, cfg DeliveryConfig, payload map[string]any) map[string]any {
	t.Helper()
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted = decodeJSONBody(r)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	ms := newMemStore()
	ms.seed("chatops_channels", map[string]any{
		"id": "chat_1", "name": "#sre", "platform": platform, "webhook_url": srv.URL, "interactive": interactive,
	})
	e := honestyEngine(ms, cfg)
	res := e.deliverNotificationViaAdapter(context.Background(), notificationFor("chatops", "chat_1", payload))
	if res.Status != deliveryDelivered {
		t.Fatalf("status = %q (%s)", res.Status, res.Err)
	}
	return posted
}

func alertPayload() map[string]any {
	return map[string]any{"alert_group_id": "grp_1", "title": "Disk full", "severity": "critical"}
}

func TestInteractiveSlackChannelGetsTheButtons(t *testing.T) {
	body := buttonChannel(t, "slack", true, DeliveryConfig{PublicURL: "https://anomaly.example.com"}, alertPayload())
	blocks, _ := body["blocks"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %v, want the text and the actions", body["blocks"])
	}
	actions, _ := blocks[1].(map[string]any)["elements"].([]any)
	var ids []string
	for _, a := range actions {
		ids = append(ids, a.(map[string]any)["action_id"].(string))
	}
	if got := strings.Join(ids, ","); got != "ack,resolve,silence:60,silence:240,silence:480,open_ui" {
		t.Errorf("buttons = %s", got)
	}
	// The link is a button here, so the text does not repeat it.
	if text, _ := body["text"].(string); strings.Contains(text, "https://anomaly.example.com") {
		t.Errorf("text repeats the link the button carries: %q", text)
	}
}

func TestInteractiveMattermostChannelGetsTheButtons(t *testing.T) {
	cfg := DeliveryConfig{PublicURL: "https://anomaly.example.com", MattermostActionSecret: "s3cret"}
	body := buttonChannel(t, "mattermost", true, cfg, alertPayload())
	attachments, _ := body["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("attachments = %v", body["attachments"])
	}
	att := attachments[0].(map[string]any)
	if att["title_link"] != "https://anomaly.example.com/alert-groups/grp_1" {
		t.Errorf("title_link = %v", att["title_link"])
	}
	if actions, _ := att["actions"].([]any); len(actions) != 5 {
		t.Errorf("%d actions, want acknowledge, resolve and three silences", len(actions))
	}

	// Without the action secret Mattermost gets the text alone, and the text
	// then carries the link.
	body = buttonChannel(t, "mattermost", true, DeliveryConfig{PublicURL: "https://anomaly.example.com"}, alertPayload())
	if _, has := body["attachments"]; has {
		t.Errorf("buttons without an action secret: %v", body["attachments"])
	}
	if text, _ := body["text"].(string); !strings.HasSuffix(text, "/alert-groups/grp_1") {
		t.Errorf("text = %q, want the link line", text)
	}
}

// A channel that has not asked for buttons is posted exactly what it was
// posted before, and a status message never carries them.
func TestButtonsOnlyWhereAskedAndOnlyUnderTheAlert(t *testing.T) {
	cfg := DeliveryConfig{PublicURL: "https://anomaly.example.com"}
	if body := buttonChannel(t, "slack", false, cfg, alertPayload()); len(body) != 1 || body["text"] == nil {
		t.Errorf("a channel without interactive got %v, want the text alone", body)
	}
	status := alertPayload()
	status["chatops_event"] = chatopsEventAcknowledged
	status["chatops_channel_id"] = "chat_1"
	if body := buttonChannel(t, "slack", true, cfg, status); len(body) != 1 {
		t.Errorf("a status message got %v, want the text alone", body)
	}
	if body := buttonChannel(t, "generic", true, cfg, alertPayload()); len(body) != 1 {
		t.Errorf("a platform without a button format got %v, want the text alone", body)
	}
}
