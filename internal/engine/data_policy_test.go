package engine

import (
	"context"
	"strings"
	"testing"
)

func policy(blocked, allowlist string) ChannelPolicy {
	return buildChannelPolicy(blocked, allowlist, false)
}

// TestNoAllowlistAllowsEverything pins the compatible default: an installation
// that has not stated a boundary is not silently given one.
func TestNoAllowlistAllowsEverything(t *testing.T) {
	p := policy("", "")
	for _, u := range []string{"https://hooks.example.com/x", "http://10.1.2.3/y", "nonsense"} {
		if ok, detail := p.DestinationAllowed(u); !ok {
			t.Errorf("DestinationAllowed(%q) = false (%s), want true with no allowlist", u, detail)
		}
	}
}

func TestEgressAllowlistMatching(t *testing.T) {
	p := policy("", ".example.ru,exact.example.com,10.20.0.0/16")
	cases := []struct {
		url  string
		want bool
		why  string
	}{
		{"https://hooks.example.ru/path", true, "leading dot matches a subdomain"},
		{"https://example.ru/path", true, "leading dot also matches the bare domain"},
		{"https://example.ru.evil.com/path", false, "suffix match must not be a substring match"},
		{"https://exact.example.com/x", true, "exact hostname"},
		{"https://other.example.com/x", false, "exact rule does not cover siblings"},
		{"https://10.20.30.40/x", true, "literal IP inside an allowed CIDR"},
		{"https://10.99.30.40/x", false, "literal IP outside every CIDR"},
		{"https://named.host/x", false, "a CIDR rule cannot authorise a name"},
		{"::::not a url", false, "unparseable destinations are refused, not waved through"},
	}
	for _, c := range cases {
		if ok, _ := p.DestinationAllowed(c.url); ok != c.want {
			t.Errorf("DestinationAllowed(%q) = %v, want %v — %s", c.url, ok, c.want, c.why)
		}
	}
}

// TestBlockedChannelIsTerminalSkip is the central promise of the policy: a
// refused channel produces a terminal skip with a stated reason, not a failure
// that will be retried and not a silent success.
func TestBlockedChannelIsTerminalSkip(t *testing.T) {
	p := policy("telegram,slack", "")
	outcome, refused := p.checkOutboundPolicy("telegram", "12345")
	if !refused {
		t.Fatal("telegram must be refused when blocked")
	}
	if outcome.Status != deliverySkipped {
		t.Errorf("status = %q, want %q — a retry cannot make a refused channel allowed", outcome.Status, deliverySkipped)
	}
	if outcome.ProviderStatus != skipChannelBlocked {
		t.Errorf("reason = %q, want %q", outcome.ProviderStatus, skipChannelBlocked)
	}
	if !strings.Contains(outcome.Err, "NXS_ANOMALY_BLOCKED_CHANNELS") {
		t.Errorf("detail %q should name the setting that carries the decision", outcome.Err)
	}
	if _, refused := p.checkOutboundPolicy("email", "a@b.c"); refused {
		t.Error("email is not blocked and must pass")
	}
}

func TestUnlistedDestinationIsTerminalSkip(t *testing.T) {
	p := policy("", ".example.ru")
	outcome, refused := p.checkOutboundPolicy("webhook", "https://hooks.slack.com/services/x")
	if !refused {
		t.Fatal("a destination outside the allowlist must be refused")
	}
	if outcome.ProviderStatus != skipDestinationNotAllowed {
		t.Errorf("reason = %q, want %q", outcome.ProviderStatus, skipDestinationNotAllowed)
	}
	if _, refused := p.checkOutboundPolicy("webhook", "https://hooks.example.ru/x"); refused {
		t.Error("an allowlisted destination must pass")
	}
	// Telegram's host is not something an operator types into a target field,
	// so the allowlist must not silently disable it — blocking Telegram is a
	// channel decision.
	if _, refused := p.checkOutboundPolicy("telegram", "12345"); refused {
		t.Error("a chat id is not a URL and must not be judged by the egress allowlist")
	}
}

// TestPolicyRefusesConfigurationNotJustDelivery covers the half that makes the
// policy usable: an operator saving a target the installation refuses is told
// so, instead of finding out the night nobody was paged.
func TestPolicyRefusesConfigurationNotJustDelivery(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	e.deliveryCfg.Channels = policy("telegram", "")

	_, err := e.CreateUser(context.Background(), map[string]any{
		"name":                 "Bob",
		"notification_targets": []any{map[string]any{"type": "telegram", "target": "555"}},
	})
	if err == nil {
		t.Fatal("creating a user with a blocked channel must be refused")
	}
	if !strings.Contains(err.Error(), "telegram") {
		t.Errorf("error %q should name the channel", err)
	}

	if _, err := e.CreateUser(context.Background(), map[string]any{
		"name":                 "Carol",
		"notification_targets": []any{map[string]any{"type": "email", "target": "carol@example.ru"}},
	}); err != nil {
		t.Fatalf("a permitted channel must still be accepted: %v", err)
	}
}

func TestPolicyRefusesUnlistedWebhookTargetAtConfigurationTime(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	e.deliveryCfg.Channels = policy("", ".example.ru")

	if _, err := e.CreateUser(context.Background(), map[string]any{
		"name":                 "Dave",
		"notification_targets": []any{map[string]any{"type": "webhook", "target": "https://evil.example.com/x"}},
	}); err == nil {
		t.Fatal("a webhook target outside the allowlist must be refused")
	}
	if _, err := e.CreateUser(context.Background(), map[string]any{
		"name":                 "Erin",
		"notification_targets": []any{map[string]any{"type": "webhook", "target": "https://hooks.example.ru/x"}},
	}); err != nil {
		t.Fatalf("an allowlisted webhook target must be accepted: %v", err)
	}
}
