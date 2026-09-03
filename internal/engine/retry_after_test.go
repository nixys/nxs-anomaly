package engine

import (
	"testing"
	"time"
)

// A provider that answers 429 named a time. Retrying before it is a request
// guaranteed to be refused, and it arrives precisely during the burst that
// caused the limit — so ignoring it turns a rate limit into a queue of refusals.

func TestHTTPOutcomeReadsRetryAfterHeader(t *testing.T) {
	res := httpOutcome("http_post", 429, "", "", "30")

	if res.Status != deliveryFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %s, want 30s", res.RetryAfter)
	}
}

// Telegram answers 429 with the wait inside the body and does not always send
// the header, so the body is read when the header says nothing.
func TestHTTPOutcomeReadsTelegramRetryAfterBody(t *testing.T) {
	body := `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":12}}`

	res := httpOutcome("http_post", 429, body, "", "")

	if res.RetryAfter != 12*time.Second {
		t.Errorf("RetryAfter = %s, want 12s from the body", res.RetryAfter)
	}
}

// The header is what a proxy in between would also honour, so it wins.
func TestRetryAfterHeaderWinsOverBody(t *testing.T) {
	body := `{"parameters":{"retry_after":12}}`

	if got := parseRetryAfter("30", body); got != 30*time.Second {
		t.Errorf("RetryAfter = %s, want the header's 30s", got)
	}
}

func TestRetryAfterIsCapped(t *testing.T) {
	if got := parseRetryAfter("99999", ""); got != maxRetryAfter {
		t.Errorf("RetryAfter = %s, want it capped at %s", got, maxRetryAfter)
	}
}

func TestRetryAfterIgnoresNonsense(t *testing.T) {
	for _, header := range []string{"", "soon", "-5", "0"} {
		if got := parseRetryAfter(header, "not json"); got != 0 {
			t.Errorf("parseRetryAfter(%q) = %s, want 0 so the configured backoff applies", header, got)
		}
	}
}

// Anything other than 429 keeps the configured backoff: a 500 with a stray
// Retry-After is not a rate limit, and treating it as one would let a broken
// provider dictate this service's schedule.
func TestRetryAfterOnlyAppliesTo429(t *testing.T) {
	res := httpOutcome("http_post", 503, "", "", "30")

	if res.RetryAfter != 0 {
		t.Errorf("RetryAfter = %s on a 503, want the ordinary backoff", res.RetryAfter)
	}
}
