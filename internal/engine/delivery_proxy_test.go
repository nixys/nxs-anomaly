package engine

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder collects what a stand-in proxy was asked for. Accesses come from the
// proxy's own goroutines, so they are locked.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, s)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func TestBuildProxySettingsResolvesPerChannel(t *testing.T) {
	s := buildProxySettings(
		"http://egress.internal:3128",
		map[string]string{
			"telegram": "socks5://user:pw@tunnel.internal",
			"webhook":  "direct",
			"email":    "",
		},
		"receiver.internal, .corp.example",
	)

	tg := s.byChannel["telegram"]
	if tg == nil {
		t.Fatal("telegram has no proxy")
	}
	if tg.addr != "tunnel.internal:1080" {
		t.Errorf("socks5 default port not applied: %s", tg.addr)
	}
	if !tg.socks() {
		t.Error("socks5 endpoint not recognised as SOCKS")
	}
	if got := s.byChannel["email"]; got == nil || got.addr != "egress.internal:3128" {
		t.Errorf("channel without an override did not inherit the default: %+v", got)
	}
	if _, proxied := s.byChannel["webhook"]; proxied {
		t.Error(`"direct" did not opt the channel out of the default proxy`)
	}
	if len(s.errs) != 0 {
		t.Errorf("unexpected parse errors: %v", s.errs)
	}
}

func TestBuildProxySettingsRejectsUnusableURL(t *testing.T) {
	for name, raw := range map[string]string{
		"unsupported scheme": "ftp://proxy.internal:21",
		"no host":            "http://",
	} {
		s := buildProxySettings(raw, nil, "")
		if len(s.byChannel) != 0 {
			t.Errorf("%s: proxy was accepted", name)
		}
		if s.errs["telegram"] == nil {
			t.Errorf("%s: no error recorded, the channel would silently go direct", name)
		}
	}
}

func TestProxyBypassFollowsNoProxySyntax(t *testing.T) {
	s := buildProxySettings("http://p.internal:3128", nil, "receiver.internal, .corp.example")
	cases := map[string]bool{
		"receiver.internal":     true,
		"api.corp.example":      true,
		"corp.example":          true,
		"deep.api.corp.example": true,
		"api.telegram.org":      false,
		"notcorp.example":       false,
		"xreceiver.internal":    false,
	}
	for host, want := range cases {
		if got := s.bypass(host); got != want {
			t.Errorf("bypass(%q) = %v, want %v", host, got, want)
		}
	}
	if !buildProxySettings("http://p.internal:3128", nil, "*").bypass("api.telegram.org") {
		t.Error(`"*" did not bypass everything`)
	}
}

// recordingProxy is an HTTP proxy that answers every forwarded request itself
// and remembers what it was asked for.
func recordingProxy(t *testing.T, seen *recorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.add(r.Host + r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProxiedChannelSendsThroughTheProxy(t *testing.T) {
	var seen recorder
	proxySrv := recordingProxy(t, &seen)

	s := buildProxySettings("", map[string]string{"telegram": proxySrv.URL}, "")
	clients := newDeliveryClients(5*time.Second, ipPolicy(isBlockedIP), ChannelPolicy{}, s)
	client := clients["telegram"]
	if client == nil {
		t.Fatal("no client built for the proxied channel")
	}

	// api.telegram.org is never resolved or dialled here: the whole request goes
	// to the proxy, which is the point of the feature.
	resp, err := client.Get("http://api.telegram.org/bot123/sendMessage")
	if err != nil {
		t.Fatalf("proxied request failed: %v", err)
	}
	_ = resp.Body.Close()
	if len(seen.list()) != 1 || seen.list()[0] != "api.telegram.org/bot123/sendMessage" {
		t.Fatalf("proxy did not receive the request: %v", seen.list())
	}
}

// The proxy an operator names is almost always inside the perimeter — an egress
// sidecar on 10.x, or here a loopback test server. The SSRF dial guard must not
// refuse it, or every proxied deployment would be unable to page anyone while
// the production profile is on.
func TestProxyAddressIsExemptFromTheSSRFDialGuard(t *testing.T) {
	var seen recorder
	proxySrv := recordingProxy(t, &seen)

	s := buildProxySettings(proxySrv.URL, nil, "")
	clients := newDeliveryClients(5*time.Second, ipPolicy(isBlockedIP), ChannelPolicy{}, s)

	resp, err := clients["slack"].Get("http://hooks.slack.com/services/T/B/X")
	if err != nil {
		t.Fatalf("request through a loopback proxy was refused with the SSRF guard on: %v", err)
	}
	_ = resp.Body.Close()
	if len(seen.list()) != 1 {
		t.Fatalf("proxy was not used: %v", seen.list())
	}
}

// A destination on the NO_PROXY list is dialled directly, and that direct dial
// is still judged by the SSRF blocklist — the guard is bypassed for the proxy,
// not for everything the proxied client touches.
func TestNoProxyDestinationIsDialledDirectlyAndStillGuarded(t *testing.T) {
	var seen recorder
	proxySrv := recordingProxy(t, &seen)

	receiver := serveOn(t, publicHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	secret := serveOn(t, privateHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	s := buildProxySettings(proxySrv.URL, nil, publicHost+","+privateHost)
	clients := newDeliveryClients(5*time.Second, ipPolicy(blockOnly(privateHost)), ChannelPolicy{}, s)
	client := clients["webhook"]

	resp, err := client.Get(receiver.URL)
	if err != nil {
		t.Fatalf("NO_PROXY destination was not reachable directly: %v", err)
	}
	_ = resp.Body.Close()
	if len(seen.list()) != 0 {
		t.Errorf("NO_PROXY destination went through the proxy anyway: %v", seen.list())
	}

	if resp, err := client.Get(secret.URL); err == nil {
		_ = resp.Body.Close()
		t.Error("a blocked address on the NO_PROXY list was dialled; the guard is off for direct hops")
	}
}

// A typo in the proxy URL must not degrade to "send directly": in a network that
// blocks the provider that is an alert nobody receives, reported as a provider
// problem. Every attempt on the channel fails, carrying the reason.
func TestMisconfiguredProxyFailsLoudlyInsteadOfGoingDirect(t *testing.T) {
	s := buildProxySettings("htp://typo.internal:3128", nil, "")
	clients := newDeliveryClients(5*time.Second, ipPolicy(isBlockedIP), ChannelPolicy{}, s)

	cfg := DeliveryConfig{
		HTTPClient:  newDeliveryHTTPClient(5*time.Second, false, ChannelPolicy{}),
		proxies:     s,
		httpClients: clients,
	}
	client := cfg.clientFor("telegram")
	if client == cfg.HTTPClient {
		t.Fatal("channel fell back to the direct client")
	}
	_, err := client.Get("http://api.telegram.org/bot123/sendMessage")
	if err == nil {
		t.Fatal("request succeeded despite an unusable proxy")
	}
	if !strings.Contains(err.Error(), "misconfigured") {
		t.Errorf("error does not name the cause: %v", err)
	}
}

func TestSSRFGuardModePerChannel(t *testing.T) {
	s := buildProxySettings("", map[string]string{"telegram": "http://p.internal:3128"}, "")
	cfg := DeliveryConfig{BlockPrivateWebhooks: true, proxies: s}
	if got := cfg.ssrfGuardFor("telegram"); got.mode != modeProxied {
		t.Errorf("proxied channel guard = %d, want guardProxied: its destination is still judged, just not by a local dial", got.mode)
	}
	if got := cfg.ssrfGuardFor("webhook"); got.mode != modeDirect {
		t.Errorf("direct channel guard = %d, want guardDirect", got.mode)
	}
	cfg.BlockPrivateWebhooks = false
	if got := cfg.ssrfGuardFor("webhook"); got.mode != modeOff {
		t.Errorf("guard = %d with the guard disabled, want guardOff", got.mode)
	}
}

// The default proxy applies to webhooks too, and the proxied pre-flight used
// to be off entirely: a user-supplied http://169.254.169.254/ went through the
// proxy and the proxy host's metadata service answered. What this process can
// judge without dialling, it judges.
func TestProxiedGuardRefusesWhatItCanJudgeLocally(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5:8080/hook",
		"http://127.0.0.1/hook",
		"http://[::1]/hook",
		"http://[fd00::1]/hook",
		"http://localhost/hook", // resolves locally to loopback
	} {
		if err := checkWebhookURL(ctx, raw, guardProxied); err == nil {
			t.Errorf("%s passed the proxied pre-flight", raw)
		}
	}
	for _, raw := range []string{
		"http://93.184.216.34/hook",
		"https://hooks.slack.invalid/services/x", // no local answer: the proxy resolves it
	} {
		if err := checkWebhookURL(ctx, raw, guardProxied); err != nil {
			t.Errorf("%s refused by the proxied pre-flight: %v", raw, err)
		}
	}
	if err := checkWebhookURL(ctx, "ftp://93.184.216.34/x", guardProxied); err == nil {
		t.Error("a non-HTTP scheme passed the proxied pre-flight")
	}
}

// A destination the guard refuses is terminal: the address does not change
// between attempts, so retrying it only delays the reason reaching the
// timeline. It used to be scheduled for retry twice before failing.
func TestABlockedDestinationIsSkippedNotRetried(t *testing.T) {
	out := postWebhookGuarded(context.Background(), &http.Client{}, "http://169.254.169.254/latest/meta-data/",
		map[string]any{"title": "x"}, guardProxied, nil)
	if out.Status != deliverySkipped {
		t.Errorf("status = %q, want %q (terminal)", out.Status, deliverySkipped)
	}
	if out.ProviderStatus != skipBlockedDestination {
		t.Errorf("reason = %q, want %q", out.ProviderStatus, skipBlockedDestination)
	}
	if !strings.Contains(out.Err, "non-public address") {
		t.Errorf("detail does not name the cause: %q", out.Err)
	}
}

// Through a proxy the dial guard never sees a redirect hop, so the redirect
// policy has to refuse a non-public IP literal itself.
func TestRedirectToAPrivateIPLiteralIsRefused(t *testing.T) {
	check := deliveryCheckRedirect(ipPolicy(isBlockedIP), ChannelPolicy{})
	req, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err := check(req, []*http.Request{{}}); err == nil {
		t.Error("redirect to the metadata address was followed")
	}
	req, _ = http.NewRequest(http.MethodGet, "http://93.184.216.34/next", nil)
	if err := check(req, []*http.Request{{}}); err != nil {
		t.Errorf("redirect to a public address refused: %v", err)
	}
}

// Readiness must agree with delivery about what can reach a person: a channel
// whose proxy will not parse has no transport, even though its own credential
// is present.
func TestReadinessCountsAMisconfiguredProxyAsAMissingTransport(t *testing.T) {
	cfg := DeliveryConfig{
		TelegramToken: "123:abc",
		proxies:       buildProxySettings("", map[string]string{"telegram": "htp://typo:3128"}, ""),
	}
	gap := channelTransportGap("telegram", cfg)
	if gap == "" {
		t.Fatal("readiness reports a working Telegram transport while every delivery on it fails")
	}
	if !strings.Contains(gap, "proxy") {
		t.Errorf("gap does not name the proxy: %q", gap)
	}
	if channelTransportGap("telegram", DeliveryConfig{TelegramToken: "123:abc"}) != "" {
		t.Error("an unproxied deployment gained a spurious gap")
	}
}

// A ChatOps channel posts to its platform's host, so proxying Telegram has to
// cover the Telegram ChatOps channel as well — nobody sets a second variable for
// the same blocked provider. An explicit ChatOps setting still wins.
func TestChatopsFollowsThePlatformProxyUnlessToldOtherwise(t *testing.T) {
	cfg := DeliveryConfig{proxies: buildProxySettings("",
		map[string]string{"telegram": "socks5://tunnel.internal:1080"}, "")}
	if got := cfg.chatopsProxyChannel("telegram"); got != "telegram" {
		t.Errorf("Telegram ChatOps ignored the Telegram proxy: %q", got)
	}
	if got := cfg.chatopsProxyChannel("mattermost"); got != "chatops" {
		t.Errorf("an unproxied platform borrowed a proxy: %q", got)
	}

	explicit := DeliveryConfig{proxies: buildProxySettings("", map[string]string{
		"telegram": "socks5://tunnel.internal:1080",
		"chatops":  "direct",
	}, "")}
	if got := explicit.chatopsProxyChannel("telegram"); got != "chatops" {
		t.Errorf("an explicit ChatOps setting was overruled by the platform: %q", got)
	}
}

// startSOCKS5 runs a minimal no-auth SOCKS5 proxy that records the destination
// it is asked for and connects to redirectTo instead. Recording the destination
// is the assertion that matters: it proves the name went to the proxy rather
// than being resolved here, which is what a poisoned DNS deployment depends on.
func startSOCKS5(t *testing.T, redirectTo string, seen *recorder) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSOCKS5(conn, redirectTo, seen)
		}
	}()
	return ln.Addr().String()
}

func serveSOCKS5(conn net.Conn, redirectTo string, seen *recorder) {
	defer conn.Close() //nolint:errcheck
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, make([]byte, int(head[1]))); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil { // version 5, no auth
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}
	var host string
	switch req[3] {
	case 0x03: // domain name — what a socks5h client sends
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return
		}
		name := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return
		}
		host = string(name)
	case 0x01:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return
		}
		host = net.IP(ip).String()
	default:
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return
	}
	seen.add(net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))))

	upstream, err := net.Dial("tcp", redirectTo)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close() //nolint:errcheck
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(upstream, conn) }()
	_, _ = io.Copy(conn, upstream)
}

func TestSOCKS5ChannelReachesProviderWithoutResolvingItLocally(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(provider.Close)

	var seen recorder
	socksAddr := startSOCKS5(t, strings.TrimPrefix(provider.URL, "http://"), &seen)

	s := buildProxySettings("socks5://"+socksAddr, nil, "")
	clients := newDeliveryClients(5*time.Second, ipPolicy(isBlockedIP), ChannelPolicy{}, s)

	resp, err := clients["telegram"].Get("http://api.telegram.org/bot123/sendMessage")
	if err != nil {
		t.Fatalf("SOCKS5 delivery failed: %v", err)
	}
	_ = resp.Body.Close()
	if len(seen.list()) != 1 || seen.list()[0] != "api.telegram.org:80" {
		t.Fatalf("destination was not handed to the proxy by name: %v", seen.list())
	}
}

// email and call speak plain TCP, so they get a dialler rather than a client —
// and only from a SOCKS5 proxy, because an HTTP proxy has no requests to carry
// for them.
func TestTCPChannelsUseTheSOCKSDiallerAndIgnoreHTTPProxies(t *testing.T) {
	if d := buildProxySettings("http://egress.internal:3128", nil, "").dialFunc("email", time.Second); d != nil {
		t.Error("an HTTP proxy produced an SMTP dialler it cannot serve")
	}
	if c := newDeliveryClients(time.Second, nil, ChannelPolicy{}, buildProxySettings("socks5://p:1080", nil, ""))["call"]; c != nil {
		t.Error("a TCP-only channel was given an HTTP client")
	}

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("220 smtp.example ESMTP\r\n"))
		_ = conn.Close()
	}()

	var seen recorder
	socksAddr := startSOCKS5(t, target.Addr().String(), &seen)
	dial := buildProxySettings("socks5://"+socksAddr, nil, "").dialFunc("email", 5*time.Second)
	if dial == nil {
		t.Fatal("no SMTP dialler built for a SOCKS5 proxy")
	}
	conn, err := dial("tcp", "smtp.example:465")
	if err != nil {
		t.Fatalf("SMTP dial through SOCKS5 failed: %v", err)
	}
	defer conn.Close() //nolint:errcheck
	greeting := make([]byte, 8)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		t.Fatalf("no greeting through the tunnel: %v", err)
	}
	if len(seen.list()) != 1 || seen.list()[0] != "smtp.example:465" {
		t.Fatalf("SMTP destination was not handed to the proxy: %v", seen.list())
	}
}

// startTCPRelay stands in for an HAProxy "mode tcp" frontend: it speaks no
// protocol at all, it just copies bytes to the one backend it was configured
// with. It records the addresses it accepted so a test can prove the connection
// went through it rather than to the destination directly.
func startTCPRelay(t *testing.T, backend string, seen *recorder) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			seen.add(backend)
			go func() {
				defer conn.Close() //nolint:errcheck
				upstream, err := net.Dial("tcp", backend)
				if err != nil {
					return
				}
				defer upstream.Close() //nolint:errcheck
				go func() { _, _ = io.Copy(upstream, conn) }()
				_, _ = io.Copy(conn, upstream)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestTCPRelayEndpointNeedsAnExplicitPort(t *testing.T) {
	s := buildProxySettings("tcp://relay.internal", nil, "")
	if err := s.errs["telegram"]; err == nil {
		t.Fatal("a tcp relay without a port was accepted; there is no port to guess")
	}
	ok := buildProxySettings("tcp://relay.internal:443", nil, "").byChannel["telegram"]
	if ok == nil || ok.addr != "relay.internal:443" {
		t.Fatalf("tcp relay not resolved: %+v", ok)
	}
	if !ok.relay() || ok.socks() || !ok.carriesTCP() {
		t.Error("tcp endpoint not classified as a relay that carries TCP")
	}
}

// The point of a relay is that nothing above the socket changes: the request
// still names the provider, so Host, TLS SNI and certificate verification keep
// validating against the provider rather than against the relay.
func TestTCPRelayReachesProviderKeepingTheDestinationHost(t *testing.T) {
	var gotHost string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(provider.Close)

	var seen recorder
	relayAddr := startTCPRelay(t, strings.TrimPrefix(provider.URL, "http://"), &seen)

	s := buildProxySettings("tcp://"+relayAddr, nil, "")
	clients := newDeliveryClients(5*time.Second, ipPolicy(isBlockedIP), ChannelPolicy{}, s)

	resp, err := clients["telegram"].Get("http://api.telegram.org/bot123/sendMessage")
	if err != nil {
		t.Fatalf("relayed delivery failed: %v", err)
	}
	_ = resp.Body.Close()
	if len(seen.list()) != 1 {
		t.Fatalf("connection did not go through the relay: %v", seen.list())
	}
	if gotHost != "api.telegram.org" {
		t.Fatalf("relay rewrote the destination host: %q", gotHost)
	}
}

// A relay works below the protocol, so unlike an HTTP proxy it also carries the
// two channels that have no HTTP requests to proxy.
func TestTCPRelayCarriesTheTCPOnlyChannels(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		accepted <- struct{}{}
		_ = conn.Close()
	}()

	s := buildProxySettings("tcp://"+backend.Addr().String(), nil, "")
	dial := s.dialFunc("email", 5*time.Second)
	if dial == nil {
		t.Fatal("a tcp relay produced no SMTP dialler")
	}
	conn, err := dial("tcp", "smtp.example.com:25")
	if err != nil {
		t.Fatalf("relayed SMTP dial failed: %v", err)
	}
	_ = conn.Close()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP connection never reached the relay")
	}
}

// NO_PROXY keeps its meaning for a relay: a listed host is dialled by name, not
// handed to the relay's single backend.
func TestTCPRelayHonoursNoProxy(t *testing.T) {
	s := buildProxySettings("tcp://127.0.0.1:1", nil, "smtp.example.com")
	if _, err := s.dialFunc("email", time.Second)("tcp", "smtp.example.com:25"); err == nil {
		t.Fatal("expected a direct dial to the bypassed host, which nothing is listening on")
	} else if strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("bypassed host was sent to the relay anyway: %v", err)
	}
}
