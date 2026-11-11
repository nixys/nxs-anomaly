package engine

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Chat delivery: Telegram, Slack and Mattermost message payloads, their action
// buttons, and the parsing of what comes back when a responder presses one.

func sendTelegram(ctx context.Context, client *http.Client, chatID, text, token, groupID string, shift telegramShiftOptions, publicURL string) (status, errMsg, providerResp string) {
	if token == "" {
		return "failed", "NXS_ANOMALY_TELEGRAM_BOT_TOKEN is not set", ""
	}
	if chatID == "" {
		return "failed", "telegram target chat id is empty", ""
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	// Telegram API is a fixed public host — never apply the private-IP guard here.
	s, err := postWebhook(ctx, client, url, telegramMessageWithActions(chatID, text, groupID, shift, publicURL), false)
	if s == "delivered" {
		return "delivered", "", "telegram_sendMessage"
	}
	return "failed", err, ""
}

// chatGroupAction is one interactive action on an alert group: the ChatOps
// command word it runs and the label the responder sees.
type chatGroupAction struct {
	command string
	label   string
}

// chatGroupActions map an interactive action to the command it runs. A tap goes
// through the same command path as the typed word, so the team and role
// boundaries cannot differ between the two.
//
// The undo pair is here with the rest rather than in a table of its own: they
// are ordinary group commands, and what makes them undo is only where they are
// offered — on the message a completed action left behind.
var chatGroupActions = map[string]chatGroupAction{
	"ack":       {command: "ack", label: "Acknowledge"},
	"resolve":   {command: "resolve", label: "Resolve"},
	"unack":     {command: "unack", label: "Undo acknowledge"},
	"unresolve": {command: "unresolve", label: "Reopen"},
	"show":      {command: "show", label: "Details"},
}

// silenceOptions are the durations offered as buttons, in minutes.
//
// Silence is the answer neither verdict gives at three in the morning:
// acknowledging claims the incident is being worked, resolving claims it is
// over, and "I have seen it, it is noise, stop calling" is neither. Three
// durations, because a fourth wraps the row on a phone.
var silenceOptions = []int{60, 240, 480}

// silenceLabel names a duration the way a responder reads it, not the way it is
// stored.
func silenceLabel(minutes int) string {
	if minutes%60 == 0 {
		return fmt.Sprintf("Silence %dh", minutes/60)
	}
	return fmt.Sprintf("Silence %dm", minutes)
}

// bulkChatActions are the verbs `bulk` accepts. Kept next to the button builder
// so a keyboard cannot offer a verb the command rejects.
var bulkChatActions = map[string]bool{"ack": true, "silence": true, "resolve": true}

// telegramCallbackDataLimit is Telegram's hard cap on callback_data, in bytes.
const telegramCallbackDataLimit = 64

// telegramMessagePayload builds the sendMessage body, attaching the acknowledge
// and resolve buttons when the notification is about an alert group.
//
// Buttons are omitted rather than truncated when the callback payload would not
// fit: Telegram rejects the whole sendMessage when callback_data is over the
// limit, so an unusually long group id would cost the notification itself.
// Losing a shortcut is recoverable; losing the alert is not.
func telegramMessagePayload(chatID, text, groupID string) map[string]any {
	return telegramMessageWithActions(chatID, text, groupID, telegramShiftOptions{}, "")
}

// telegramShiftOptions are the extras a shift-handover notice carries: it has no
// alert group to act on, but it does have a schedule to answer questions about.
type telegramShiftOptions struct {
	offerCheckin bool
	scheduleID   string
}

// telegramMessageWithActions builds the sendMessage body and the keyboard that
// belongs on it: the alert keyboard for a group, the shift keyboard for a
// handover notice, and none at all for anything else.
func telegramMessageWithActions(chatID, text, groupID string, shift telegramShiftOptions, publicURL string) map[string]any {
	payload := map[string]any{
		"chat_id": chatID,
		// Telegram's sendMessage limit is 4096 UTF-16 code units, not bytes;
		// capping by rune keeps Cyrillic alerts from being cut to half their
		// allowance and, more importantly, from being cut mid-rune — the API
		// rejects invalid UTF-8.
		"text":                     utils.TruncateRunes(text, 4096),
		"disable_web_page_preview": true,
	}
	var rows []any
	if groupID == "" {
		rows = telegramShiftRows(shift)
	} else {
		rows = telegramGroupRows(groupID, publicURL)
	}
	if len(rows) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	return payload
}

// telegramShiftRows are the buttons under a shift-handover notice: confirm the
// shift, take it over, or ask who the schedule currently names.
//
// All three exist as typed commands already, and all three need an argument
// nobody remembers at 09:00 on a Monday — the schedule id. The notice knows it,
// so the button carries it.
func telegramShiftRows(shift telegramShiftOptions) []any {
	if !shift.offerCheckin {
		return nil
	}
	rows := []any{[]any{
		map[string]any{"text": "I am on duty", "callback_data": "duty:on"},
		map[string]any{"text": "Take this shift", "callback_data": "duty:take"},
	}}
	if data := "oncall:" + shift.scheduleID; shift.scheduleID != "" && len(data) <= telegramCallbackDataLimit {
		rows = append(rows, []any{map[string]any{"text": "Who is on call", "callback_data": data}})
	}
	return rows
}

// telegramGroupRows are the buttons under a live alert: the two verdicts, the
// silence durations, and a link out to the group's own page.
func telegramGroupRows(groupID, publicURL string) []any {
	var rows []any
	if row := telegramActionRow(groupID, "ack", "resolve"); len(row) > 0 {
		rows = append(rows, row)
	}
	var silence []any
	for _, minutes := range silenceOptions {
		data := fmt.Sprintf("silence:%d:%s", minutes, groupID)
		if len(data) > telegramCallbackDataLimit {
			continue
		}
		silence = append(silence, map[string]any{"text": silenceLabel(minutes), "callback_data": data})
	}
	if len(silence) > 0 {
		rows = append(rows, silence)
	}
	if btn := telegramGroupLinkButton(groupID, publicURL); btn != nil {
		rows = append(rows, []any{btn})
	}
	return rows
}

// telegramActionRow renders the named actions as one row, in the order given.
//
// Ordered explicitly by the caller: map iteration would shuffle the buttons
// between messages, and a responder taps by position under time pressure.
func telegramActionRow(groupID string, actions ...string) []any {
	var row []any
	for _, action := range actions {
		a, known := chatGroupActions[action]
		if !known {
			continue
		}
		data := action + ":" + groupID
		if len(data) > telegramCallbackDataLimit {
			continue
		}
		row = append(row, map[string]any{"text": a.label, "callback_data": data})
	}
	return row
}

// telegramGroupLinkButton opens the group's own page. A URL button is not a
// callback: the tap never reaches this service, which is why it works even when
// the bot is refusing everything else.
func telegramGroupLinkButton(groupID, publicURL string) map[string]any {
	url := AlertGroupURL(publicURL, groupID)
	if url == "" {
		return nil
	}
	return map[string]any{"text": "Open in nxs-anomaly", "url": url}
}

// AlertGroupURL is where a person reads the whole group: its timeline, its
// alerts and every action a keyboard has no room for.
//
// Empty when the deployment does not know its own public address, and the
// button is then omitted rather than pointed at a URL that would not resolve —
// the same rule the Mattermost buttons already follow.
func AlertGroupURL(publicURL, groupID string) string {
	if publicURL == "" || groupID == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + "/alert-groups/" + groupID
}

// SlackMessagePayload builds a Slack incoming-webhook body, attaching the
// acknowledge and resolve buttons when the notification is about an alert group.
//
// The plain text is kept alongside the blocks: it is what a notification preview
// and a client that cannot render blocks will show, and losing it would mean an
// alert that reads as empty on a phone lock screen.
func SlackMessagePayload(text, groupID, publicURL string) map[string]any {
	payload := map[string]any{"text": text}
	if groupID == "" {
		return payload
	}
	payload["blocks"] = []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		map[string]any{"type": "actions", "elements": slackActionElements(groupID, publicURL)},
	}
	return payload
}

// slackActionElements are the buttons on a Slack alert: the two verdicts, the
// silence durations, and — when this deployment knows its own address — a link
// out to the group's page. The link is a plain URL button, so tapping it never
// reaches this service.
func slackActionElements(groupID, publicURL string) []any {
	var elements []any
	for _, action := range []string{"ack", "resolve"} {
		elements = append(elements, map[string]any{
			"type":      "button",
			"text":      map[string]any{"type": "plain_text", "text": chatGroupActions[action].label},
			"action_id": action,
			"value":     groupID,
		})
	}
	for _, minutes := range silenceOptions {
		elements = append(elements, map[string]any{
			"type":      "button",
			"text":      map[string]any{"type": "plain_text", "text": silenceLabel(minutes)},
			"action_id": fmt.Sprintf("silence:%d", minutes),
			"value":     groupID,
		})
	}
	if url := AlertGroupURL(publicURL, groupID); url != "" {
		elements = append(elements, map[string]any{
			"type":      "button",
			"text":      map[string]any{"type": "plain_text", "text": "Open in nxs-anomaly"},
			"action_id": "open_ui",
			"url":       url,
		})
	}
	return elements
}

// SlackSettledPayload rewrites an alert message once one of its buttons has
// been used: the alert keeps its text, the verdict is appended, and the only
// button left is the one that takes the action back.
//
// The message used to be replaced by the verdict alone — "Acknowledged
// grp_a1b2…" — which threw away the incident the channel had been reading, and
// left the id, which nobody reads.
func SlackSettledPayload(originalText, verdict, undoAction, groupID string) map[string]any {
	text := settledText(originalText, verdict)
	payload := map[string]any{"replace_original": true, "text": text}
	blocks := []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
	}
	if a, known := chatGroupActions[undoAction]; known && groupID != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []any{
			map[string]any{
				"type":      "button",
				"text":      map[string]any{"type": "plain_text", "text": a.label},
				"action_id": undoAction,
				"value":     groupID,
			},
		}})
	}
	payload["blocks"] = blocks
	return payload
}

// settledText is the alert plus the verdict it reached. Falls back to the
// verdict alone when the platform did not hand back what the message said,
// which is the old behaviour and still better than an empty post.
func settledText(originalText, verdict string) string {
	if strings.TrimSpace(originalText) == "" {
		return "✓ " + verdict
	}
	return originalText + "\n\n✓ " + verdict
}

// MattermostMessagePayload builds a Mattermost incoming-webhook body.
//
// Mattermost buttons carry an absolute callback URL rather than an opaque token
// the platform hands back, so they can only be attached when this deployment
// knows its own public address and has an action secret to authenticate the
// callback with. Without either, the alert still goes out — plain. A button that
// posts to a URL nobody answers is worse than no button.
func MattermostMessagePayload(text, groupID, publicURL, actionSecret string) map[string]any {
	payload := map[string]any{"text": text}
	if groupID == "" || publicURL == "" || actionSecret == "" {
		return payload
	}
	var actions []any
	for _, action := range []string{"ack", "resolve"} {
		actions = append(actions, mattermostAction(action, chatGroupActions[action].label, groupID, publicURL, actionSecret))
	}
	for _, minutes := range silenceOptions {
		actions = append(actions,
			mattermostAction(fmt.Sprintf("silence:%d", minutes), silenceLabel(minutes), groupID, publicURL, actionSecret))
	}
	// The attachment carries the buttons and nothing else. It used to repeat the
	// alert text, which Mattermost renders in addition to the message rather
	// than instead of it — so every actionable post showed the incident twice.
	attachment := map[string]any{"actions": actions}
	if url := AlertGroupURL(publicURL, groupID); url != "" {
		// Mattermost has no URL button: a link in the attachment title is how a
		// post sends someone to a page without a callback.
		attachment["title"] = "Open in nxs-anomaly"
		attachment["title_link"] = url
	}
	payload["attachments"] = []any{attachment}
	return payload
}

// mattermostAction builds one interactive button. The secret rides in the
// context because Mattermost does not sign these callbacks: it is the only
// credential the request carries, and it is compared in constant time on
// arrival.
func mattermostAction(action, label, groupID, publicURL, actionSecret string) map[string]any {
	return map[string]any{
		"id":   action,
		"name": label,
		"integration": map[string]any{
			"url": strings.TrimRight(publicURL, "/") + "/integrations/v1/chatops/mattermost",
			"context": map[string]any{
				"action":   action,
				"group_id": groupID,
				"token":    actionSecret,
			},
		},
	}
}

// MattermostSettledUpdate rewrites a post once one of its buttons has been used.
//
// The message itself is deliberately absent from the update: Mattermost leaves
// out what an update does not name, so omitting it keeps the alert exactly as
// the channel has been reading it. Replacing it with the verdict — which is
// what this used to do — turned the incident into an identifier.
func MattermostSettledUpdate(verdict, undoAction, groupID, publicURL, actionSecret string) map[string]any {
	attachment := map[string]any{"text": "✓ " + verdict}
	if a, known := chatGroupActions[undoAction]; known && groupID != "" && publicURL != "" && actionSecret != "" {
		attachment["actions"] = []any{mattermostAction(undoAction, a.label, groupID, publicURL, actionSecret)}
	}
	return map[string]any{
		"update": map[string]any{
			"props": map[string]any{"attachments": []any{attachment}},
		},
	}
}

// ChatActionCommand turns an interactive action name and its target into the
// ChatOps command it stands for, so Slack and Mattermost buttons resolve through
// the same table as the Telegram ones.
func ChatActionCommand(action, groupID string) (string, bool) {
	if groupID == "" {
		return "", false
	}
	// An action name is either a plain verb ("ack") or a verb carrying its own
	// argument ("silence:60"). One encoding serves all three platforms: it fits
	// a Telegram callback, a Slack action_id and a Mattermost context alike.
	if verb, arg, carriesArg := strings.Cut(action, ":"); carriesArg {
		if verb != "silence" {
			return "", false
		}
		minutes, err := strconv.Atoi(arg)
		if err != nil || minutes <= 0 {
			return "", false
		}
		return fmt.Sprintf("silence %s %d", groupID, minutes), true
	}
	a, known := chatGroupActions[action]
	if !known {
		return "", false
	}
	return a.command + " " + groupID, true
}

// TelegramUndoRow is the single-button keyboard left on a settled alert, or nil
// when the action that settled it cannot be taken back.
func TelegramUndoRow(undoAction, groupID string) []any {
	if undoAction == "" || groupID == "" {
		return nil
	}
	row := telegramActionRow(groupID, undoAction)
	if len(row) == 0 {
		return nil
	}
	return row
}

// UndoActionFor names the action that takes a completed one back, or "" when
// there is nothing to undo. It is what puts a single button on a settled
// message: a mis-tapped Resolve from a phone was otherwise unfixable without
// opening the web UI.
func UndoActionFor(command string) string {
	switch {
	case strings.HasPrefix(command, "ack "):
		return "unack"
	case strings.HasPrefix(command, "resolve "):
		return "unresolve"
	}
	return ""
}

// ParseTelegramCallbackData turns a tapped button back into a ChatOps command,
// reporting false for anything this service did not put on a keyboard. It lives
// next to the code that builds the buttons so the two cannot drift.
func ParseTelegramCallbackData(data string) (string, bool) {
	verb, rest, found := strings.Cut(data, ":")
	if !found || rest == "" {
		return "", false
	}
	switch verb {
	case "silence":
		// silence:<minutes>:<group_id> — the duration is part of the button, so
		// the person picks it by tapping rather than by typing.
		minutes, groupID, ok := strings.Cut(rest, ":")
		if !ok {
			return "", false
		}
		return ChatActionCommand("silence:"+minutes, groupID)
	case "bulk":
		// bulk:<verb> or bulk:silence:<minutes> — the storm case, where acting
		// on one group at a time is the problem.
		what, arg, carriesArg := strings.Cut(rest, ":")
		if !bulkChatActions[what] {
			return "", false
		}
		if carriesArg {
			return "bulk " + what + " " + arg, true
		}
		return "bulk " + what, true
	case "alerts":
		return "alerts " + rest, true
	case "duty":
		if rest != "on" && rest != "off" && rest != "take" {
			return "", false
		}
		return "duty " + rest, true
	case "oncall":
		return "oncall " + rest, true
	default:
		return ChatActionCommand(verb, rest)
	}
}

// TelegramReplyPayload renders a command's answer as a Telegram message body,
// attaching a keyboard when the answer is something to act on.
//
// It takes the engine's own result map rather than a rendered string because the
// listing needs the groups themselves, not the sentence describing them.
func TelegramReplyPayload(chatID, text string, result map[string]any, publicURL string) map[string]any {
	return telegramReplyPayload(chatID, text, result, publicURL)
}

// telegramReplyPayload is the unexported body, so the engine's own callers and
// its tests reach the keyboards without going through the exported name.
func telegramReplyPayload(chatID, text string, result map[string]any, publicURL string) map[string]any {
	response, _ := result["response"].(map[string]any)
	if response == nil {
		return telegramMessagePayload(chatID, text, "")
	}
	// A card is about one group, so it carries that group's own keyboard.
	if card, ok := response["alert_group_card"].(map[string]any); ok {
		return telegramMessageWithActions(chatID, text, utils.StrVal(card, "id"),
			telegramShiftOptions{}, publicURL)
	}
	items, isListing := response["alerts_page"].([]map[string]any)
	if !isListing {
		// The greeting is the only other answer with something to press.
		if utils.BoolVal(response, "offer_start_keyboard", false) {
			payload := telegramMessagePayload(chatID, text, "")
			payload["reply_markup"] = map[string]any{"inline_keyboard": telegramStartRows()}
			return payload
		}
		return telegramMessagePayload(chatID, text, "")
	}
	payload := telegramMessagePayload(chatID, text, "")
	if rows := telegramListingRows(items, response); len(rows) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	return payload
}

// telegramStartRows are the buttons the bot offers on first contact: the three
// things a responder opens it for, none of which needs an argument.
func telegramStartRows() []any {
	return []any{
		[]any{map[string]any{"text": "My open alerts", "callback_data": "alerts:1"}},
		[]any{
			map[string]any{"text": "I am on duty", "callback_data": "duty:on"},
			map[string]any{"text": "I am off duty", "callback_data": "duty:off"},
		},
	}
}

// telegramListingRows draws one page of the alert listing.
//
// Each group gets a row of two: the label opens the group, the narrow button
// acknowledges it. They used to be one button whose label read "[critical] Disk
// full" and whose action was acknowledge — a label promising navigation and a
// tap performing a state change, with no confirmation and no way back.
func telegramListingRows(items []map[string]any, response map[string]any) []any {
	var rows []any
	for _, item := range items {
		id := utils.StrVal(item, "id")
		open := "show:" + id
		ack := "ack:" + id
		if len(open) > telegramCallbackDataLimit || len(ack) > telegramCallbackDataLimit {
			continue
		}
		rows = append(rows, []any{
			map[string]any{"text": alertButtonLabel(item), "callback_data": open},
			map[string]any{"text": "✓", "callback_data": ack},
		})
	}
	if len(rows) > 0 {
		// Offered on the listing rather than on an alert, because this is where
		// the storm is visible: forty groups is where acting one at a time
		// stops being possible.
		rows = append(rows, []any{
			map[string]any{"text": "Acknowledge all open", "callback_data": "bulk:ack"},
			map[string]any{"text": "Silence all 1h", "callback_data": "bulk:silence:60"},
		})
	}
	if nav := telegramPagerRow(utils.IntVal(response, "page"), utils.IntVal(response, "pages")); nav != nil {
		rows = append(rows, nav)
	}
	return rows
}

// alertButtonLabel names a group on its button: severity first, because that is
// what decides whether it is worth tapping, then as much title as fits.
func alertButtonLabel(item map[string]any) string {
	severity := strDefault(utils.StrVal(item, "severity"), "unknown")
	title := strDefault(utils.StrVal(item, "title"), "alert")
	return utils.TruncateRunes(fmt.Sprintf("[%s] %s", severity, title), 60)
}

// telegramPagerRow builds the previous/next row, or nil when everything fits on
// one page. Only reachable pages get a button: a dead "next" on the last page
// invites a tap that does nothing.
func telegramPagerRow(page, pages int) []any {
	if pages <= 1 {
		return nil
	}
	var row []any
	if page > 1 {
		row = append(row, map[string]any{
			"text": "‹ Previous", "callback_data": "alerts:" + strconv.Itoa(page-1),
		})
	}
	if page < pages {
		row = append(row, map[string]any{
			"text": "Next ›", "callback_data": "alerts:" + strconv.Itoa(page+1),
		})
	}
	return row
}
