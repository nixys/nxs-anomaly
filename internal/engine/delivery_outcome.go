package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// deliveryOutcome is what one provider call produced.
//
// It exists because "delivered / failed" was too coarse to be honest. A
// notification for a channel this deployment has no transport for was reported
// as delivered, which is the worst possible answer: an operator reading the
// timeline concluded that someone had been told. The third outcome — skipped —
// says nothing was sent and no retry will help.
//
// The struct also carries what a delivery diagnosis actually needs: the
// provider's own status string, its response code where the protocol has one,
// and a bounded, redacted excerpt of the response.
type deliveryOutcome struct {
	// Status is one of "delivered", "failed" or "skipped".
	Status string
	// ProviderStatus names the transport that answered ("telegram_sendMessage")
	// or, for a skip, why there was none ("not_configured").
	ProviderStatus string
	// Code is the provider's numeric response code (HTTP status for the
	// webhook-shaped adapters), or 0 where the protocol has none.
	Code int
	// Response is a bounded, redacted excerpt of what the provider said.
	Response string
	// Err is the failure detail, empty on success.
	Err string
	// RetryAfter is how long the provider asked to be left alone, when it said
	// so (HTTP 429). Zero means it did not, and the configured backoff applies.
	//
	// Honouring it matters most exactly when it arrives: a provider returns 429
	// during a burst, which is when this service is generating the most traffic,
	// and retrying on the usual schedule turns a rate limit into a queue of
	// requests that are all refused again.
	RetryAfter time.Duration
}

// Delivery statuses an adapter may report.
const (
	deliveryDelivered = "delivered"
	deliveryFailed    = "failed"
	deliverySkipped   = "skipped"
)

// skipNotConfigured is the only skip reason so far: this deployment has no
// transport for the channel. It is stored on the notification and shown in the
// UI, so it is a constant rather than free text.
const skipNotConfigured = "not_configured"

func delivered(providerStatus string, code int, response string) deliveryOutcome {
	return deliveryOutcome{
		Status: deliveryDelivered, ProviderStatus: providerStatus,
		Code: code, Response: redactProviderResponse(response),
	}
}

func failed(providerStatus, errMsg string, code int, response string) deliveryOutcome {
	return deliveryOutcome{
		Status: deliveryFailed, ProviderStatus: providerStatus, Err: errMsg,
		Code: code, Response: redactProviderResponse(response),
	}
}

// skipped reports that no transport exists for this channel on this
// deployment. detail explains what would have to be configured.
func skipped(reason, detail string) deliveryOutcome {
	return deliveryOutcome{Status: deliverySkipped, ProviderStatus: reason, Err: detail}
}

// maxProviderResponse bounds what is stored per delivery attempt. Provider
// bodies can be arbitrarily long (an HTML error page), and the attempts table
// is written on every retry of every notification.
const maxProviderResponse = 512

// redactProviderResponse trims a provider response to something safe to store
// and show: bounded in length, stripped of control characters, and with
// anything that looks like a credential masked.
//
// Provider bodies routinely echo the request, and the request may carry a bot
// token or a signed URL. The attempts table is readable by anyone who can read
// delivery diagnostics, so redaction happens here, at the point of capture,
// rather than being left to every reader.
func redactProviderResponse(s string) string {
	if s == "" {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = redactSecrets(s)
	if t := utils.TruncateRunes(s, maxProviderResponse); t != s {
		s = t + "…"
	}
	return strings.TrimSpace(s)
}

// secretPatterns mask credentials that providers echo back.
//
// Two shapes cover what is actually seen in provider responses: a
// key/value pair (JSON or query string) whose key looks like a credential, and
// a Telegram bot token, which is part of the request URL and therefore appears
// verbatim in Telegram's own error bodies.
var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)\b(token|password|passwd|secret|api[_-]?key|authorization)("?\s*[:=]\s*"?)([^"&\s,};']+)`), "$1$2***"},
	{regexp.MustCompile(`(?i)\bbot\d+:[A-Za-z0-9_\-]+`), "bot***"},
}

// redactSecrets masks anything credential-shaped in a captured response.
func redactSecrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// httpOutcome turns an HTTP call result into an outcome, so every
// webhook-shaped adapter reports codes and bodies the same way.
func httpOutcome(providerStatus string, code int, body, errMsg, header string) deliveryOutcome {
	if errMsg != "" {
		return failed(providerStatus, errMsg, code, body)
	}
	if code >= 200 && code < 300 {
		return delivered(providerStatus, code, body)
	}
	res := failed(providerStatus, fmt.Sprintf("HTTP %d", code), code, body)
	if code == 429 {
		res.RetryAfter = parseRetryAfter(header, body)
	}
	return res
}

// maxRetryAfter caps what a provider can make this service wait. A cooperating
// provider asks for seconds; anything beyond an hour is either a broken header
// or a provider that will not be available on any schedule this queue can keep,
// and both are better handled by the ordinary backoff and the retry limit.
const maxRetryAfter = time.Hour

// parseRetryAfter reads how long a provider asked to be left alone.
//
// Two sources, because the two that matter here disagree: the HTTP standard puts
// seconds (or a date) in Retry-After, while Telegram answers 429 with
// {"parameters":{"retry_after":N}} in the body and does not always send the
// header. The header wins when both are present — it is the one a proxy in
// between would also honour.
func parseRetryAfter(header, body string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
		return capRetryAfter(time.Duration(secs) * time.Second)
	}
	if when, err := time.Parse(time.RFC1123, strings.TrimSpace(header)); err == nil {
		if d := time.Until(when); d > 0 {
			return capRetryAfter(d)
		}
	}
	var telegram struct {
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(body), &telegram); err == nil && telegram.Parameters.RetryAfter > 0 {
		return capRetryAfter(time.Duration(telegram.Parameters.RetryAfter) * time.Second)
	}
	return 0
}

func capRetryAfter(d time.Duration) time.Duration {
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}
