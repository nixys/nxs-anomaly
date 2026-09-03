package engine

import (
	"strings"
	"testing"
)

// Slack and Mattermost buttons resolve through the same action table as the
// Telegram ones, so an action added on one platform cannot quietly mean
// something else on another.

func TestSlackPayloadCarriesButtonsForAnAlert(t *testing.T) {
	payload := SlackMessagePayload("disk full", "grp-1")

	// The plain text stays alongside the blocks: it is what a lock-screen
	// preview and any client that cannot render blocks will show.
	if payload["text"] != "disk full" {
		t.Errorf("text = %v, want the alert text kept", payload["text"])
	}
	blocks, ok := payload["blocks"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("blocks = %+v, want a section and an actions block", payload["blocks"])
	}
	actions, _ := blocks[1].(map[string]any)
	elements, _ := actions["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("got %d button(s), want acknowledge and resolve", len(elements))
	}
	for _, raw := range elements {
		button, _ := raw.(map[string]any)
		id, _ := button["action_id"].(string)
		value, _ := button["value"].(string)
		if _, ok := ChatActionCommand(id, value); !ok {
			t.Errorf("button %q/%q is not one the callback path accepts back", id, value)
		}
	}
}

func TestSlackPayloadWithoutAGroupHasNoButtons(t *testing.T) {
	payload := SlackMessagePayload("you are on call", "")

	if _, present := payload["blocks"]; present {
		t.Errorf("blocks attached without an alert group: %+v", payload["blocks"])
	}
}

func TestMattermostPayloadCarriesCallbackURLAndSecret(t *testing.T) {
	payload := MattermostMessagePayload("disk full", "grp-1", "https://alerts.example.com/", "s3cret")

	attachments, ok := payload["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("attachments = %+v, want one", payload["attachments"])
	}
	actions, _ := attachments[0].(map[string]any)["actions"].([]any)
	if len(actions) != 2 {
		t.Fatalf("got %d action(s), want acknowledge and resolve", len(actions))
	}
	first, _ := actions[0].(map[string]any)
	integration, _ := first["integration"].(map[string]any)
	url, _ := integration["url"].(string)
	if url != "https://alerts.example.com/integrations/v1/chatops/mattermost" {
		t.Errorf("callback url = %q; the trailing slash must not double up", url)
	}
	ctx, _ := integration["context"].(map[string]any)
	if ctx["token"] != "s3cret" {
		t.Errorf("context token = %v, want the action secret — it is the only credential the callback carries", ctx["token"])
	}
	if _, ok := ChatActionCommand(ctx["action"].(string), ctx["group_id"].(string)); !ok {
		t.Errorf("context %v does not resolve to a command", ctx)
	}
}

// A button posting to a URL nobody answers is worse than no button, so both the
// address and the secret have to be configured before one is rendered.
func TestMattermostPayloadOmitsButtonsWhenUnconfigured(t *testing.T) {
	cases := []struct {
		name      string
		publicURL string
		secret    string
	}{
		{"no public url", "", "s3cret"},
		{"no action secret", "https://alerts.example.com", ""},
		{"neither", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := MattermostMessagePayload("disk full", "grp-1", c.publicURL, c.secret)
			if _, present := payload["attachments"]; present {
				t.Errorf("buttons rendered with %s: %+v", c.name, payload["attachments"])
			}
			if payload["text"] != "disk full" {
				t.Errorf("text = %v, want the alert to go out regardless", payload["text"])
			}
		})
	}
}

func TestChatActionCommand(t *testing.T) {
	cases := []struct {
		action, groupID, want string
		ok                    bool
	}{
		{"ack", "grp-1", "ack grp-1", true},
		{"resolve", "grp-1", "resolve grp-1", true},
		{"delete", "grp-1", "", false},
		{"ack", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		got, ok := ChatActionCommand(c.action, c.groupID)
		if ok != c.ok || got != c.want {
			t.Errorf("ChatActionCommand(%q, %q) = (%q, %v), want (%q, %v)",
				c.action, c.groupID, got, ok, c.want, c.ok)
		}
	}
}

// Every platform must offer the same actions: a button added to one and not the
// others is a difference in what a responder can do depending on where they read
// the alert.
func TestEveryPlatformOffersTheSameActions(t *testing.T) {
	telegram := replyMarkupRow(t, telegramMessagePayload("-100500", "disk full", "grp-1"))
	slackBlocks := SlackMessagePayload("disk full", "grp-1")["blocks"].([]any)
	slack := slackBlocks[1].(map[string]any)["elements"].([]any)
	mattermost := MattermostMessagePayload("disk full", "grp-1", "https://x", "s")["attachments"].([]any)[0].(map[string]any)["actions"].([]any)

	if len(telegram) != len(slack) || len(slack) != len(mattermost) {
		t.Fatalf("button counts differ: telegram %d, slack %d, mattermost %d",
			len(telegram), len(slack), len(mattermost))
	}
	for i := range telegram {
		tg, _ := telegram[i].(map[string]any)["callback_data"].(string)
		tgAction, _, _ := strings.Cut(tg, ":")
		slackAction, _ := slack[i].(map[string]any)["action_id"].(string)
		mmAction, _ := mattermost[i].(map[string]any)["id"].(string)
		if tgAction != slackAction || slackAction != mmAction {
			t.Errorf("button %d is %q on telegram, %q on slack, %q on mattermost", i, tgAction, slackAction, mmAction)
		}
	}
}
