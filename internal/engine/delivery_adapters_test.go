package engine

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestGuardWebhookURL(t *testing.T) {
	ctx := context.Background()

	// Guard disabled → always allowed, even for private addresses.
	if err := guardWebhookURL(ctx, "http://127.0.0.1/hook", false); err != nil {
		t.Errorf("disabled guard should allow any URL, got: %v", err)
	}

	// Guard enabled → loopback blocked.
	for _, raw := range []string{
		"http://127.0.0.1/hook",
		"http://localhost/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/x",
		"http://192.168.1.10/x",
	} {
		if err := guardWebhookURL(ctx, raw, true); err == nil {
			t.Errorf("expected %q to be blocked", raw)
		}
	}

	// Guard enabled → non-http scheme blocked.
	if err := guardWebhookURL(ctx, "ftp://example.com/x", true); err == nil {
		t.Errorf("ftp scheme should be blocked")
	}

	// Guard enabled → empty host blocked.
	if err := guardWebhookURL(ctx, "http:///x", true); err == nil {
		t.Errorf("empty host should be blocked")
	}

	// Guard enabled → public host allowed (uses a documentation IP via literal).
	if err := guardWebhookURL(ctx, "http://93.184.216.34/x", true); err != nil {
		t.Errorf("public IP should be allowed, got: %v", err)
	}
}

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "192.168.0.1", "169.254.169.254", "::1", "0.0.0.0"}
	for _, s := range blocked {
		if !isBlockedIP(parseIP(t, s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	allowed := []string{"8.8.8.8", "93.184.216.34", "1.1.1.1"}
	for _, s := range allowed {
		if isBlockedIP(parseIP(t, s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad test IP %q", s)
	}
	return ip
}

// Ensure the SSRF error message is descriptive (used in delivery failure logs).
func TestGuardWebhookURLErrorMessage(t *testing.T) {
	err := guardWebhookURL(context.Background(), "http://10.0.0.1/x", true)
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("expected descriptive non-public error, got: %v", err)
	}
}
