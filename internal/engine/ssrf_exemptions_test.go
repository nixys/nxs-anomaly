package engine

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Exceptions to the SSRF guard: an internal receiver (a chat gateway in the
// same cluster) is private by definition, and switching the whole guard off to
// reach it — which the production profile refuses — was the only way.

func TestSSRFExemptionsAllow(t *testing.T) {
	x := parseSSRFExemptions("gw.chat.svc.cluster.local, .corp.example, 10.20.0.0/16, 172.16.5.9, fd00:1::/64")
	cases := []struct {
		host, ip string
		want     bool
	}{
		{"gw.chat.svc.cluster.local", "10.96.14.7", true},
		{"GW.Chat.svc.cluster.local.", "10.96.14.7", true}, // case and trailing dot
		{"other.chat.svc.cluster.local", "10.96.14.7", false},
		{"jira.corp.example", "192.168.1.10", true},
		{"corp.example", "192.168.1.10", true},
		{"evilcorp.example", "192.168.1.10", false}, // suffix is a label boundary
		{"anything", "10.20.3.4", true},             // CIDR judges the address
		{"anything", "10.21.3.4", false},
		{"172.16.5.9", "172.16.5.9", true}, // a bare IP is a /32
		{"anything", "fd00:1::5", true},
		// Never excepted, whatever the list says: these are what an SSRF is after.
		{"gw.chat.svc.cluster.local", "127.0.0.1", false},
		{"gw.chat.svc.cluster.local", "169.254.169.254", false},
		{"gw.chat.svc.cluster.local", "::1", false},
		{"gw.chat.svc.cluster.local", "fe80::1", false},
		{"gw.chat.svc.cluster.local", "0.0.0.0", false},
	}
	for _, c := range cases {
		if got := x.allows(c.host, net.ParseIP(c.ip)); got != c.want {
			t.Errorf("allows(%q, %s) = %v, want %v", c.host, c.ip, got, c.want)
		}
	}

	wide := parseSSRFExemptions("0.0.0.0/0, 127.0.0.0/8, 169.254.0.0/16")
	for _, ip := range []string{"127.0.0.1", "169.254.169.254"} {
		if wide.allows("x", net.ParseIP(ip)) {
			t.Errorf("%s excepted by a CIDR that covers it; loopback and link-local must stay refused", ip)
		}
	}
	if !wide.allows("x", net.ParseIP("10.0.0.1")) {
		t.Error("0.0.0.0/0 did not except a private address")
	}

	var none *ssrfExemptions
	if parseSSRFExemptions("  ") != nil || none.allows("x", net.ParseIP("10.0.0.1")) {
		t.Error("an empty list must except nothing")
	}
	if bad := parseSSRFExemptions("10.0.0.0/33"); bad.allows("10.0.0.0/33", net.ParseIP("10.0.0.1")) {
		t.Error("an invalid CIDR was taken as something")
	}
}

// The pre-flight has to honour the exceptions exactly as the dialler does, or
// an excepted receiver is skipped before the dial is ever tried. IP literals
// resolve without DNS, so private addresses can be judged here.
func TestPreflightHonoursExemptions(t *testing.T) {
	ctx := context.Background()
	x := parseSSRFExemptions("10.20.0.0/16")
	for _, mode := range []ssrfMode{modeDirect, modeProxied} {
		guard := ssrfGuard{mode: mode, exempt: x}
		if err := checkWebhookURL(ctx, "https://10.20.1.1/hook", guard); err != nil {
			t.Errorf("mode %d: excepted address refused: %v", mode, err)
		}
		if err := checkWebhookURL(ctx, "https://10.21.1.1/hook", guard); err == nil {
			t.Errorf("mode %d: address outside the exceptions accepted", mode)
		}
		if err := checkWebhookURL(ctx, "http://169.254.169.254/latest/meta-data/", ssrfGuard{mode: mode, exempt: parseSSRFExemptions("0.0.0.0/0")}); err == nil {
			t.Errorf("mode %d: metadata endpoint accepted under a catch-all exception", mode)
		}
	}
	if err := checkWebhookURL(ctx, "https://10.20.1.1/hook", guardDirect); err == nil {
		t.Error("without exceptions a private address must still be refused")
	}
}

// The dialler must hand the policy the name the connection is for, not only
// the address: a host exception is judged on the name. localhost is the one
// name that resolves everywhere, so the test policy blocks every address and
// excepts that name.
func TestGuardedDialPassesHostToPolicy(t *testing.T) {
	target := serveOn(t, publicHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	var asked []string
	policy := func(host string, _ net.IP) bool {
		asked = append(asked, host)
		return host != "localhost"
	}
	client := newDeliveryHTTPClientWithPolicy(5*time.Second, policy, ChannelPolicy{})

	resp, err := client.Get("http://localhost:" + port + "/")
	if err != nil {
		t.Fatalf("excepted host refused: %v (policy asked about %v)", err, asked)
	}
	_ = resp.Body.Close()

	if _, err := client.Get("http://" + publicHost + ":" + port + "/"); err == nil {
		t.Error("an address outside the exception was dialled")
	}
}

func TestDeliveryConfigReadsExemptions(t *testing.T) {
	t.Setenv("NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS", "true")
	t.Setenv(ssrfExemptionsEnv, "gw.chat.svc, 10.20.0.0/16")
	cfg := DeliveryConfigFromEnv()
	if cfg.SSRFExemptions.String() != "gw.chat.svc, 10.20.0.0/16" {
		t.Fatalf("exemptions = %q", cfg.SSRFExemptions.String())
	}
	if g := cfg.ssrfGuardFor("webhook"); g.exempt != cfg.SSRFExemptions {
		t.Error("the webhook pre-flight does not carry the exceptions")
	}
	if err := checkWebhookURL(context.Background(), "https://10.20.1.1/x", cfg.ssrfGuardFor("webhook")); err != nil {
		t.Errorf("excepted address refused by the configured guard: %v", err)
	}

	// The dial-time half, through the client delivery actually uses. Nothing
	// listens on these addresses; what matters is whether the guard refused the
	// dial or the dial was attempted.
	dialErr := func(u string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := cfg.clientFor("webhook").Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return ""
		}
		return err.Error()
	}
	if e := dialErr("http://10.20.1.1:9/"); strings.Contains(e, "non-public address") {
		t.Errorf("the delivery client refused an excepted address: %s", e)
	}
	if e := dialErr("http://10.21.1.1:9/"); !strings.Contains(e, "non-public address") {
		t.Errorf("the delivery client dialled an address outside the exceptions: %q", e)
	}
}
