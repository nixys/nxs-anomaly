package engine

import (
	"strconv"
	"strings"
	"testing"
)

// A responder woken at night should acknowledge with one tap, not by typing an
// alert group id into a chat. These tests pin the button payload, because two
// of its properties are load-bearing: Telegram rejects the whole sendMessage
// when callback_data is over its limit, and the tap has to name the group
// unambiguously for the command path to find it again.

func replyMarkupRows(t *testing.T, payload map[string]any) []any {
	t.Helper()
	markup, ok := payload["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("no reply_markup in payload: %+v", payload)
	}
	keyboard, ok := markup["inline_keyboard"].([]any)
	if !ok || len(keyboard) == 0 {
		t.Fatalf("inline_keyboard = %+v, want at least one row", markup["inline_keyboard"])
	}
	return keyboard
}

// replyMarkupRow is the single-row case, which several messages still are.
func replyMarkupRow(t *testing.T, payload map[string]any) []any {
	t.Helper()
	rows := replyMarkupRows(t, payload)
	if len(rows) != 1 {
		t.Fatalf("inline_keyboard = %+v, want exactly one row", rows)
	}
	row, _ := rows[0].([]any)
	return row
}

func flattenButtons(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range replyMarkupRows(t, payload) {
		for _, item := range raw.([]any) {
			button, _ := item.(map[string]any)
			out = append(out, button)
		}
	}
	return out
}

func TestTelegramMessagePayloadCarriesActionButtons(t *testing.T) {
	payload := telegramMessagePayload("-100500", "disk full", "grp_a1b2c3d4e5f6")

	rows := replyMarkupRows(t, payload)
	if len(rows) != 2 {
		t.Fatalf("got %d row(s), want the verdicts and the silence durations", len(rows))
	}
	verdicts, _ := rows[0].([]any)
	want := []struct{ label, data string }{
		{"Acknowledge", "ack:grp_a1b2c3d4e5f6"},
		{"Resolve", "resolve:grp_a1b2c3d4e5f6"},
	}
	if len(verdicts) != len(want) {
		t.Fatalf("got %d verdict button(s), want acknowledge and resolve", len(verdicts))
	}
	for i, w := range want {
		button, _ := verdicts[i].(map[string]any)
		if button["text"] != w.label {
			t.Errorf("button %d label = %v, want %q", i, button["text"], w.label)
		}
		if button["callback_data"] != w.data {
			t.Errorf("button %d callback_data = %v, want %q", i, button["callback_data"], w.data)
		}
	}
}

// Silence is the answer neither verdict gives: acknowledging claims the incident
// is being worked and resolving claims it is over, and at three in the morning
// the true answer is often neither.
func TestTelegramAlertOffersSilenceDurations(t *testing.T) {
	payload := telegramMessagePayload("-100500", "disk full", "grp_a1b2c3d4e5f6")

	silence, _ := replyMarkupRows(t, payload)[1].([]any)
	if len(silence) != len(silenceOptions) {
		t.Fatalf("got %d silence button(s), want %d", len(silence), len(silenceOptions))
	}
	for i, minutes := range silenceOptions {
		button, _ := silence[i].(map[string]any)
		command, ok := ParseTelegramCallbackData(button["callback_data"].(string))
		if !ok {
			t.Fatalf("silence button %d renders callback_data the callback path rejects: %v", i, button)
		}
		if want := "silence grp_a1b2c3d4e5f6 " + strconv.Itoa(minutes); command != want {
			t.Errorf("silence button %d maps to %q, want %q", i, command, want)
		}
	}
}

// The link is a URL button, so tapping it never reaches this service — which is
// why it is the one button that still works when the bot is refusing everything
// else. It is omitted when the deployment does not know its own address, rather
// than pointed at a URL that would not resolve.
func TestTelegramAlertLinksToTheGroupWhenTheAddressIsKnown(t *testing.T) {
	with := telegramMessageWithActions("-100500", "disk full", "grp-1",
		telegramShiftOptions{}, "https://alerts.example.com/")
	var link map[string]any
	for _, button := range flattenButtons(t, with) {
		if _, isLink := button["url"]; isLink {
			link = button
		}
	}
	if link == nil {
		t.Fatal("no link button on an alert with a public URL configured")
	}
	if link["url"] != "https://alerts.example.com/alert-groups/grp-1" {
		t.Errorf("link = %v; the trailing slash must not double up", link["url"])
	}

	without := telegramMessagePayload("-100500", "disk full", "grp-1")
	for _, button := range flattenButtons(t, without) {
		if _, isLink := button["url"]; isLink {
			t.Errorf("link button rendered without a public URL: %+v", button)
		}
	}
}

// Not every Telegram notification is about an alert group — a shift handover has
// nothing to acknowledge — and a keyboard there would offer an action that
// cannot run.
func TestTelegramMessagePayloadWithoutGroupHasNoButtons(t *testing.T) {
	payload := telegramMessagePayload("-100500", "you are on duty from 09:00", "")

	if _, present := payload["reply_markup"]; present {
		t.Errorf("reply_markup present without an alert group: %+v", payload["reply_markup"])
	}
}

// The failure this guards against is not a missing button, it is a missing
// alert: Telegram refuses the whole sendMessage when callback_data is too long,
// so the notification would fail rather than arrive without its shortcut.
func TestTelegramMessagePayloadDropsOversizedCallbackData(t *testing.T) {
	longID := strings.Repeat("x", telegramCallbackDataLimit)
	payload := telegramMessagePayload("-100500", "disk full", longID)

	if _, present := payload["reply_markup"]; present {
		t.Errorf("oversized callback_data was still attached: %+v", payload["reply_markup"])
	}
	if payload["text"] != "disk full" {
		t.Errorf("text = %v, want the alert to be sent regardless", payload["text"])
	}
}

// The guard above cannot fire on a real group: the id is a fixed 16 bytes, and
// the longest button this service renders spends 27 of the 64 available.
func TestRealCallbackDataFitsWithRoomToSpare(t *testing.T) {
	longest := "silence:" + strconv.Itoa(silenceOptions[len(silenceOptions)-1]) + ":grp_a1b2c3d4e5f6"
	if len(longest) > telegramCallbackDataLimit/2 {
		t.Errorf("longest callback_data is %d bytes of %d; the budget assumption no longer holds",
			len(longest), telegramCallbackDataLimit)
	}
}

func TestTelegramMessagePayloadTruncatesByRune(t *testing.T) {
	payload := telegramMessagePayload("-100500", strings.Repeat("я", 5000), "grp-1")

	text, _ := payload["text"].(string)
	if runes := len([]rune(text)); runes != 4096 {
		t.Errorf("text = %d runes, want it capped at 4096", runes)
	}
	if !strings.HasSuffix(text, "я") {
		t.Error("text was cut mid-rune; Telegram rejects invalid UTF-8")
	}
}

// A shift notice has no alert group, so it gets the buttons that fit it:
// confirming the shift, taking it over, and asking who the schedule names.
// All three exist as typed commands, and all three need the schedule id nobody
// remembers at 09:00 on a Monday — so the notice carries it.
func TestShiftNoticeOffersShiftButtons(t *testing.T) {
	payload := telegramMessageWithActions("-100500", "You are on call: primary", "",
		telegramShiftOptions{offerCheckin: true, scheduleID: "sch_1"}, "")

	var commands []string
	for _, button := range flattenButtons(t, payload) {
		command, ok := ParseTelegramCallbackData(button["callback_data"].(string))
		if !ok {
			t.Fatalf("shift button renders callback_data the callback path rejects: %+v", button)
		}
		commands = append(commands, command)
	}
	want := []string{"duty on", "duty take", "oncall sch_1"}
	if strings.Join(commands, "|") != strings.Join(want, "|") {
		t.Errorf("shift buttons map to %v, want %v", commands, want)
	}
}

// A notice with no schedule id still confirms and takes over; only the question
// that needs the id is dropped.
func TestShiftNoticeWithoutAScheduleDropsOnlyTheOncallButton(t *testing.T) {
	payload := telegramMessageWithActions("-100500", "You are on call", "",
		telegramShiftOptions{offerCheckin: true}, "")

	row := replyMarkupRow(t, payload)
	if len(row) != 2 {
		t.Fatalf("got %d button(s), want the check-in and the takeover", len(row))
	}
}

// An ordinary alert notification must not grow a check-in button, and a shift
// notice must not grow acknowledge buttons for an alert that does not exist.
func TestCheckinButtonIsOnlyOnShiftNotices(t *testing.T) {
	alert := telegramMessageWithActions("-100500", "disk full", "grp-1", telegramShiftOptions{}, "")
	for _, button := range flattenButtons(t, alert) {
		if button["callback_data"] == "duty:on" {
			t.Error("an alert notification offered a check-in button")
		}
	}
	plain := telegramMessageWithActions("-100500", "plain", "", telegramShiftOptions{}, "")
	if _, present := plain["reply_markup"]; present {
		t.Error("a message that is neither an alert nor a shift notice grew a keyboard")
	}
}

func TestParseTelegramCallbackData(t *testing.T) {
	cases := []struct {
		name   string
		data   string
		want   string
		wantOK bool
	}{
		{"acknowledge", "ack:grp-1", "ack grp-1", true},
		{"resolve", "resolve:grp-1", "resolve grp-1", true},
		{"undo acknowledge", "unack:grp-1", "unack grp-1", true},
		{"reopen", "unresolve:grp-1", "unresolve grp-1", true},
		{"open the card", "show:grp-1", "show grp-1", true},
		{"silence with a duration", "silence:60:grp-1", "silence grp-1 60", true},
		{"silence without a duration", "silence:grp-1", "", false},
		{"silence with a nonsense duration", "silence:soon:grp-1", "", false},
		{"bulk acknowledge", "bulk:ack", "bulk ack", true},
		{"bulk silence with a duration", "bulk:silence:60", "bulk silence 60", true},
		{"bulk of an unknown verb", "bulk:delete", "", false},
		{"take the shift", "duty:take", "duty take", true},
		{"unknown duty argument", "duty:maybe", "", false},
		{"who is on call", "oncall:sch_1", "oncall sch_1", true},
		{"group id containing a colon", "ack:grp:1", "ack grp:1", true},
		{"unknown action", "delete:grp-1", "", false},
		{"no separator", "ackgrp-1", "", false},
		{"no group", "ack:", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseTelegramCallbackData(c.data)
			if ok != c.wantOK || got != c.want {
				t.Errorf("ParseTelegramCallbackData(%q) = (%q, %v), want (%q, %v)", c.data, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// Every button this service renders must be one the callback path accepts back;
// a label added on one side and not the other would produce a dead button.
func TestEveryRenderedButtonParsesBack(t *testing.T) {
	payloads := map[string]map[string]any{
		"alert": telegramMessageWithActions("-100500", "disk full", "grp-1",
			telegramShiftOptions{}, "https://alerts.example.com"),
		"shift": telegramMessageWithActions("-100500", "on call", "",
			telegramShiftOptions{offerCheckin: true, scheduleID: "sch_1"}, ""),
		"listing": telegramReplyPayload("-100500", "open", map[string]any{
			"response": alertsPage(listGroups(2), 1),
		}, ""),
		"greeting": telegramReplyPayload("-100500", "hello", map[string]any{
			"response": map[string]any{"offer_start_keyboard": true},
		}, ""),
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			for _, button := range flattenButtons(t, payload) {
				// A URL button is not a callback and has nothing to parse back.
				if _, isLink := button["url"]; isLink {
					continue
				}
				data, _ := button["callback_data"].(string)
				if _, ok := ParseTelegramCallbackData(data); !ok {
					t.Errorf("button %q renders callback_data %q that the callback path rejects", button["text"], data)
				}
			}
		})
	}
}

// The undo button is the whole answer to a mis-tapped Resolve from a phone: the
// only way back used to be the web UI, which is exactly what somebody paged at
// three in the morning cannot conveniently open.
func TestUndoActionForOnlyOffersRealReversals(t *testing.T) {
	cases := map[string]string{
		"ack grp-1":         "unack",
		"resolve grp-1":     "unresolve",
		"silence grp-1 60":  "",
		"unack grp-1":       "",
		"show grp-1":        "",
		"alerts 2":          "",
		"bulk ack":          "",
		"acknowledgements!": "",
	}
	for command, want := range cases {
		if got := UndoActionFor(command); got != want {
			t.Errorf("UndoActionFor(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestTelegramUndoRowIsEmptyWithoutSomethingToUndo(t *testing.T) {
	if row := TelegramUndoRow("", "grp-1"); row != nil {
		t.Errorf("undo row rendered for nothing to undo: %+v", row)
	}
	if row := TelegramUndoRow("unack", ""); row != nil {
		t.Errorf("undo row rendered without a group: %+v", row)
	}
	row := TelegramUndoRow("unresolve", "grp-1")
	if len(row) != 1 {
		t.Fatalf("undo row = %+v, want exactly the one button", row)
	}
	if got := row[0].(map[string]any)["callback_data"]; got != "unresolve:grp-1" {
		t.Errorf("undo callback_data = %v, want unresolve:grp-1", got)
	}
}
