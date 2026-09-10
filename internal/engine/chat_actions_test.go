package engine

import (
	"strings"
	"testing"
)

// Slack and Mattermost buttons resolve through the same action table as the
// Telegram ones, so an action added on one platform cannot quietly mean
// something else on another.

func TestSlackPayloadCarriesButtonsForAnAlert(t *testing.T) {
	payload := SlackMessagePayload("disk full", "grp-1", "")

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
	if len(elements) != 2+len(silenceOptions) {
		t.Fatalf("got %d button(s), want the verdicts and the silence durations", len(elements))
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
	payload := SlackMessagePayload("you are on call", "", "")

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
	if len(actions) != 2+len(silenceOptions) {
		t.Fatalf("got %d action(s), want the verdicts and the silence durations", len(actions))
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
	const publicURL = "https://alerts.example.com"

	var telegram []string
	for _, button := range flattenButtons(t,
		telegramMessageWithActions("-100500", "disk full", "grp-1", telegramShiftOptions{}, publicURL)) {
		// The link is not an action on any platform: Slack renders it as a URL
		// button, Mattermost as the attachment title, Telegram as a URL button.
		// Compared separately below, because only its presence is the invariant.
		if _, isLink := button["url"]; isLink {
			continue
		}
		telegram = append(telegram, telegramActionID(button["callback_data"].(string)))
	}

	slackBlocks := SlackMessagePayload("disk full", "grp-1", publicURL)["blocks"].([]any)
	var slack []string
	for _, raw := range slackBlocks[1].(map[string]any)["elements"].([]any) {
		element := raw.(map[string]any)
		if _, isLink := element["url"]; isLink {
			continue
		}
		slack = append(slack, element["action_id"].(string))
	}

	attachment := MattermostMessagePayload("disk full", "grp-1", publicURL, "s")["attachments"].([]any)[0].(map[string]any)
	var mattermost []string
	for _, raw := range attachment["actions"].([]any) {
		mattermost = append(mattermost, raw.(map[string]any)["id"].(string))
	}

	if strings.Join(telegram, "|") != strings.Join(slack, "|") {
		t.Errorf("telegram offers %v, slack offers %v", telegram, slack)
	}
	if strings.Join(slack, "|") != strings.Join(mattermost, "|") {
		t.Errorf("slack offers %v, mattermost offers %v", slack, mattermost)
	}
	if attachment["title_link"] != publicURL+"/alert-groups/grp-1" {
		t.Errorf("mattermost link = %v, want the group's page", attachment["title_link"])
	}
}

// telegramActionID reads a callback back into the action name the other two
// platforms carry directly, so the three can be compared at all: "ack:grp-1" is
// the action "ack", and "silence:60:grp-1" is the action "silence:60".
func telegramActionID(data string) string {
	verb, rest, _ := strings.Cut(data, ":")
	if verb != "silence" {
		return verb
	}
	minutes, _, _ := strings.Cut(rest, ":")
	return verb + ":" + minutes
}
