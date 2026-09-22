package engine

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The pre-flight guard (guardWebhookURL) inspects the URL a caller supplied and
// nothing else. That left two ways for anyone who can set a webhook URL — an
// editor, via a TRIGGER_WEBHOOK step or a notification target — to reach an
// address the guard exists to keep them away from:
//
//	1. answer with a redirect to it, since the guard never sees redirect targets;
//	2. answer DNS with a public address for the check and a private one for the
//	   dial, since those were two independent lookups.
//
// Both are closed in the transport, so these tests drive a real client against
// real listeners rather than asserting on the URL string.
//
// A note on the addresses. Every address a test can bind is loopback, so the
// real isBlockedIP would reject the stand-in for the *public* first hop and the
// redirect would never be reached — an earlier draft of this file passed for
// exactly that wrong reason. The tests below therefore inject a policy that
// designates 127.0.0.1 public and 127.0.0.2 "the metadata endpoint". What is
// under test is whether the blocklist is consulted on every hop and at the
// address actually dialled; the contents of the real blocklist are asserted
// separately in TestIsBlockedIPCoversPrivateRanges.

const (
	publicHost  = "127.0.0.1" // stands in for an ordinary, permitted webhook host
	privateHost = "127.0.0.2" // stands in for 169.254.169.254 and friends
)

// blockOnly returns a policy rejecting exactly one address.
func blockOnly(addr string) func(net.IP) bool {
	target := net.ParseIP(addr)
	return func(ip net.IP) bool { return ip.Equal(target) }
}

// serveOn starts a test server bound to the given loopback address.
func serveOn(t *testing.T, addr string, h http.Handler) *httptest.Server {
	t.Helper()
	l, err := net.Listen("tcp", addr+":0")
	if err != nil {
		t.Skipf("cannot bind %s on this machine: %v", addr, err)
	}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestDeliveryTransportRefusesRedirectToBlockedAddress(t *testing.T) {
	reached := false
	secret := serveOn(t, privateHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte("iam-credentials"))
	}))

	// The host named in the webhook URL is permitted. That is the whole point:
	// the pre-flight check has no reason to object to it, and did not.
	hop := serveOn(t, publicHost, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, secret.URL, http.StatusFound)
	}))

	client := newDeliveryHTTPClientWithPolicy(5*time.Second, ipPolicy(blockOnly(privateHost)), ChannelPolicy{})
	resp, err := client.Get(hop.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect into the blocked address was followed; the guard is bypassed")
	}
	if reached {
		t.Error("the blocked target was actually contacted")
	}
	if !strings.Contains(err.Error(), "non-public address") {
		t.Errorf("expected the refusal to name the blocked address, got %v", err)
	}
}

func TestDeliveryTransportFollowsRedirectBetweenPermittedHosts(t *testing.T) {
	// Supplying CheckRedirect replaces net/http's default policy, so the ordinary
	// case has to be re-proven: a redirect between permitted targets must still
	// be followed, or every provider that answers 302 would break.
	final := serveOn(t, publicHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	hop := serveOn(t, publicHost, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))

	client := newDeliveryHTTPClientWithPolicy(5*time.Second, ipPolicy(blockOnly(privateHost)), ChannelPolicy{})
	resp, err := client.Get(hop.URL)
	if err != nil {
		t.Fatalf("ordinary redirect was not followed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("redirect did not reach the final handler: got status %d", resp.StatusCode)
	}
}

func TestDeliveryTransportRefusesBlockedAddressOnFirstHop(t *testing.T) {
	secret := serveOn(t, privateHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	client := newDeliveryHTTPClientWithPolicy(5*time.Second, ipPolicy(blockOnly(privateHost)), ChannelPolicy{})
	resp, err := client.Get(secret.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("direct request to a blocked address succeeded")
	}
}

// TestGuardedDialContextRefusesBlockedAnswer covers the TOCTOU half: the policy
// is consulted against the address that is about to be dialled, inside the dial
// itself, so there is no window between checking and connecting.
func TestGuardedDialContextRefusesBlockedAnswer(t *testing.T) {
	dial := guardedDialContext(&net.Dialer{Timeout: time.Second}, ipPolicy(isBlockedIP))
	// localhost is the one name guaranteed to resolve to a blocked address on
	// every machine that runs this suite.
	if _, err := dial(context.Background(), "tcp", "localhost:80"); err == nil {
		t.Fatal("dialled a blocked address")
	} else if !strings.Contains(err.Error(), "non-public address") {
		t.Errorf("expected a blocked-address error, got %v", err)
	}
}

func TestGuardedDialContextIsTransparentWithoutPolicy(t *testing.T) {
	// The guard is opt-in; with no policy, delivery to a local target has to keep
	// working. Local webhooks in development depend on this, and the default
	// profile leaves the guard off.
	target := serveOn(t, publicHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	client := newDeliveryHTTPClientWithPolicy(5*time.Second, nil, ChannelPolicy{})
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("unguarded delivery to a local target failed: %v", err)
	}
	_ = resp.Body.Close()
}

func TestIsBlockedIPCoversPrivateRanges(t *testing.T) {
	// The policy's contents, asserted independently of the transport.
	blocked := []string{
		"127.0.0.1",       // loopback
		"169.254.169.254", // cloud metadata (link-local)
		"10.1.2.3",        // RFC1918
		"172.16.0.1",      // RFC1918
		"192.168.1.1",     // RFC1918
		"0.0.0.0",         // unspecified
		"::1",             // IPv6 loopback
		"fd00::1",         // IPv6 unique-local
		"fe80::1",         // IPv6 link-local
	}
	for _, s := range blocked {
		if !isBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"93.184.216.34", "8.8.8.8", "2606:2800:220:1:248:1893:25c8:1946"} {
		if isBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestDeliveryConfigInstallsGuardedClientInProductionProfile(t *testing.T) {
	// The wiring, not the mechanism: a config built with the guard on must hand
	// out a client whose transport enforces it. Without this the guard could be
	// correct and simply not installed — which is what the bug was.
	t.Setenv("NXS_ANOMALY_PROFILE", "production")
	cfg := DeliveryConfigFromEnv()
	if !cfg.BlockPrivateWebhooks {
		t.Fatal("production profile did not enable BlockPrivateWebhooks")
	}
	target := serveOn(t, publicHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	resp, err := cfg.HTTPClient.Get(target.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the client from the production config reached a loopback address")
	}
	if !strings.Contains(err.Error(), "non-public address") {
		t.Errorf("refused, but not by the SSRF guard: %v", err)
	}
}

func TestDeliveryClientRejectsNonHTTPRedirectScheme(t *testing.T) {
	hop := serveOn(t, publicHost, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	client := newDeliveryHTTPClientWithPolicy(5*time.Second, ipPolicy(blockOnly(privateHost)), ChannelPolicy{})
	resp, err := client.Get(hop.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("followed a redirect to a non-HTTP scheme")
	}
}
