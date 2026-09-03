package engine

import (
	"strings"
	"testing"
)

// A responder woken at night should acknowledge with one tap, not by typing an
// alert group id into a chat. These tests pin the button payload, because two
// of its properties are load-bearing: Telegram rejects the whole sendMessage
// when callback_data is over its limit, and the tap has to name the group
// unambiguously for the command path to find it again.

func replyMarkupRow(t *testing.T, payload map[string]any) []any {
	t.Helper()
	markup, ok := payload["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("no reply_markup in payload: %+v", payload)
	}
	keyboard, ok := markup["inline_keyboard"].([]any)
	if !ok || len(keyboard) != 1 {
		t.Fatalf("inline_keyboard = %+v, want exactly one row", markup["inline_keyboard"])
	}
	row, _ := keyboard[0].([]any)
	return row
}

func TestTelegramMessagePayloadCarriesActionButtons(t *testing.T) {
	payload := telegramMessagePayload("-100500", "disk full", "grp_a1b2c3d4e5f6")

	row := replyMarkupRow(t, payload)
	if len(row) != 2 {
		t.Fatalf("got %d button(s), want acknowledge and resolve", len(row))
	}
	want := []struct{ label, data string }{
		{"Acknowledge", "ack:grp_a1b2c3d4e5f6"},
		{"Resolve", "resolve:grp_a1b2c3d4e5f6"},
	}
	for i, w := range want {
		button, _ := row[i].(map[string]any)
		if button["text"] != w.label {
			t.Errorf("button %d label = %v, want %q", i, button["text"], w.label)
		}
		if button["callback_data"] != w.data {
			t.Errorf("button %d callback_data = %v, want %q", i, button["callback_data"], w.data)
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

// A shift notice has no alert group, so it gets the one button that fits it:
// confirming the shift. Asking somebody to remember a command instead is a step
// nobody takes at 09:00 on a Monday.
func TestShiftNoticeOffersACheckinButton(t *testing.T) {
	payload := telegramMessageWithActions("-100500", "You are on call: primary", "", true)

	row := replyMarkupRow(t, payload)
	if len(row) != 1 {
		t.Fatalf("got %d button(s), want just the check-in", len(row))
	}
	button := row[0].(map[string]any)
	if button["callback_data"] != "duty:on" {
		t.Errorf("callback_data = %v, want duty:on", button["callback_data"])
	}
	command, ok := ParseTelegramCallbackData("duty:on")
	if !ok || command != "duty on" {
		t.Errorf("the check-in button maps to (%q, %v), want (\"duty on\", true)", command, ok)
	}
}

// An ordinary alert notification must not grow a check-in button, and a shift
// notice must not grow acknowledge buttons for an alert that does not exist.
func TestCheckinButtonIsOnlyOnShiftNotices(t *testing.T) {
	alert := telegramMessageWithActions("-100500", "disk full", "grp-1", false)
	for _, raw := range replyMarkupRow(t, alert) {
		if raw.(map[string]any)["callback_data"] == "duty:on" {
			t.Error("an alert notification offered a check-in button")
		}
	}
	if _, present := telegramMessageWithActions("-100500", "plain", "", false)["reply_markup"]; present {
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
	row := replyMarkupRow(t, telegramMessagePayload("-100500", "disk full", "grp-1"))
	for _, item := range row {
		button, _ := item.(map[string]any)
		data, _ := button["callback_data"].(string)
		if _, ok := ParseTelegramCallbackData(data); !ok {
			t.Errorf("button %q renders callback_data %q that the callback path rejects", button["text"], data)
		}
	}
}
