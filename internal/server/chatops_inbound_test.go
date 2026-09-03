package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// The inbound ChatOps endpoints are the half of the audit item that is a
// security property: a command claiming to come from Slack must be provably
// from Slack. These tests pin the verification itself; the routing to the
// engine is covered by the API tests.

func slackHeaders(secret, body string, ts time.Time) http.Header {
	h := http.Header{}
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "v0:%s:%s", tsStr, body)
	h.Set("X-Slack-Request-Timestamp", tsStr)
	h.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	return h
}

func TestVerifySlackSignatureAcceptsGenuineRequest(t *testing.T) {
	const secret = "s3cret"
	body := "command=%2Fack&text=grp_1&channel_id=C123"
	now := time.Now()
	if err := verifySlackSignature(slackHeaders(secret, body, now), []byte(body), secret, now); err != nil {
		t.Fatalf("genuine request rejected: %v", err)
	}
}

func TestVerifySlackSignatureRejections(t *testing.T) {
	const secret = "s3cret"
	body := "command=%2Fack&text=grp_1"
	now := time.Now()

	cases := []struct {
		name    string
		headers http.Header
		body    string
		secret  string
	}{
		{"wrong secret", slackHeaders("other-secret", body, now), body, secret},
		{"tampered body", slackHeaders(secret, body, now), body + "&text=evil", secret},
		{"stale timestamp", slackHeaders(secret, body, now.Add(-10*time.Minute)), body, secret},
		{"future timestamp", slackHeaders(secret, body, now.Add(10*time.Minute)), body, secret},
		{"missing signature", func() http.Header {
			h := slackHeaders(secret, body, now)
			h.Del("X-Slack-Signature")
			return h
		}(), body, secret},
		{"missing timestamp", func() http.Header {
			h := slackHeaders(secret, body, now)
			h.Del("X-Slack-Request-Timestamp")
			return h
		}(), body, secret},
		{"malformed timestamp", func() http.Header {
			h := slackHeaders(secret, body, now)
			h.Set("X-Slack-Request-Timestamp", "not-a-number")
			return h
		}(), body, secret},
	}
	for _, tc := range cases {
		if err := verifySlackSignature(tc.headers, []byte(tc.body), tc.secret, now); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// A replayed request is a genuine one sent again later: the freshness bound is
// what stops it, so it is asserted directly rather than implied.
func TestVerifySlackSignatureReplayWindow(t *testing.T) {
	const secret = "s3cret"
	body := "command=%2Fresolve&text=grp_1"
	signedAt := time.Now()
	headers := slackHeaders(secret, body, signedAt)

	if err := verifySlackSignature(headers, []byte(body), secret, signedAt.Add(4*time.Minute)); err != nil {
		t.Errorf("request inside the window rejected: %v", err)
	}
	if err := verifySlackSignature(headers, []byte(body), secret, signedAt.Add(6*time.Minute)); err == nil {
		t.Error("replay outside the window accepted")
	}
}

func TestSubtleCompare(t *testing.T) {
	if subtleCompare("abc", "abc") != 1 {
		t.Error("equal strings must compare equal")
	}
	if subtleCompare("abc", "abd") == 1 {
		t.Error("different strings must not compare equal")
	}
	if subtleCompare("abc", "abcd") == 1 {
		t.Error("different lengths must not compare equal")
	}
	if subtleCompare("", "") != 1 {
		t.Error("empty strings compare equal")
	}
}

func TestChatopsInboundConfigFromEnv(t *testing.T) {
	t.Setenv("NXS_ANOMALY_SLACK_SIGNING_SECRET", "  slack-secret  ")
	t.Setenv("NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET", "tg-secret")
	cfg := chatopsInboundConfigFromEnv()
	if cfg.slackSigningSecret != "slack-secret" {
		t.Errorf("slack secret = %q (whitespace must be trimmed)", cfg.slackSigningSecret)
	}
	if cfg.telegramSecretToken != "tg-secret" {
		t.Errorf("telegram secret = %q", cfg.telegramSecretToken)
	}
}

func TestChatopsReplyShape(t *testing.T) {
	engineResult := map[string]any{
		"id":         "chatmsg_1",
		"channel_id": "chat_1",
		"response":   map[string]any{"text": "Acknowledged grp_1", "alert_group_id": "grp_1"},
	}

	// Slack renders a slash-command answer from a top-level "text"; returning
	// the raw engine object left the responder with nothing on screen.
	slack := chatopsReply("slack", "C123", engineResult)
	if slack["text"] != "Acknowledged grp_1" {
		t.Errorf("slack text = %v", slack["text"])
	}
	if slack["response_type"] != "ephemeral" {
		t.Errorf("slack response_type = %v, want ephemeral", slack["response_type"])
	}

	// Telegram ignores a webhook response that is not a method call, so the
	// answer has to be the call itself — otherwise a typed command replies
	// nothing at all, which is what it used to do.
	telegram := chatopsReply("telegram", "-100500", engineResult)
	if telegram["method"] != "sendMessage" {
		t.Errorf("telegram method = %v, want sendMessage", telegram["method"])
	}
	if telegram["chat_id"] != "-100500" || telegram["text"] != "Acknowledged grp_1" {
		t.Errorf("telegram reply = %#v", telegram)
	}

	// Without a chat to answer in there is no method call to make, and the body
	// stays readable for whoever is testing the endpoint by hand.
	noChat := chatopsReply("telegram", "", engineResult)
	if noChat["ok"] != true || noChat["text"] != "Acknowledged grp_1" {
		t.Errorf("telegram reply without a chat = %#v", noChat)
	}

	// A result without a text still yields something a human can read.
	fallback := chatopsReply("slack", "C123", map[string]any{"id": "chatmsg_2"})
	if fallback["text"] == "" {
		t.Error("empty reply text")
	}
}
