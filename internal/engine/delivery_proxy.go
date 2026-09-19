package engine

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// delivery_proxy.go routes outbound notification transport through an operator's
// proxy.
//
// The reason it exists is that the provider is not always reachable from where
// this service runs: a network can block Telegram or Slack outright, or allow
// egress only through one audited hop. Without this, such a deployment has no
// way to page anyone — the notification fails at dial time, retries, and dead
// letters, which is the one failure mode alerting cannot have.
//
// Configuration is environment-only, like every other delivery credential
// (NXS_ANOMALY_TELEGRAM_BOT_TOKEN, the SMTP block): one answer per installation,
// set where the deployment is described rather than in the database.
//
//	NXS_ANOMALY_DELIVERY_PROXY_URL            default for every channel below
//	NXS_ANOMALY_DELIVERY_PROXY_<CHANNEL>_URL  overrides the default for one channel
//	NXS_ANOMALY_DELIVERY_NO_PROXY             hosts always contacted directly
//
// <CHANNEL> is one of proxyChannels, upper-cased. The per-channel override also
// takes the literal "direct" (or "none"), which sends that one channel straight
// out while everything else goes through the default proxy — the common shape
// when only the messengers are blocked and the internal webhook receiver is not.
//
// Supported schemes: http, https, socks5, socks5h, tcp. socks5 and socks5h
// behave identically here: the destination host is always resolved by the proxy,
// never locally, because a network that blocks a provider usually poisons its
// name too.
//
// tcp is not a proxy protocol at all — it is a transparent relay, the shape an
// operator gets from an HAProxy "mode tcp" frontend forwarding to the provider:
//
//	frontend telegram_api
//	    bind *:443
//	    mode tcp
//	    default_backend telegram_api_backend
//	backend telegram_api_backend
//	    mode tcp
//	    server telegram api.telegram.org:443 check
//
// Such a hop speaks nothing: no CONNECT, no SOCKS handshake, and it does not
// terminate TLS. The only thing that changes is the address the socket is opened
// to. So the relay is applied at dial time and nothing else is touched — the
// request keeps the provider's name, and so do the Host header, the TLS SNI and
// the certificate check, which still validate against the provider rather than
// against the relay. That is what keeps this safe without disabling verification.
//
// The consequence is that a relay is destination-blind: it forwards to its one
// configured backend regardless of what was asked for. It fits a channel with a
// fixed provider host and is stated as a warning for the channels whose
// destination comes from the escalation chain.
type proxySettings struct {
	// byChannel holds one endpoint per proxied channel. A channel that is absent
	// goes direct — that includes channels explicitly set to "direct".
	byChannel map[string]*proxyEndpoint
	// errs holds channels whose proxy is configured but unparseable. They are
	// kept rather than dropped: a typo in the proxy URL must not silently become
	// "send directly", which in a blocked network means the page never arrives
	// and nothing says why. Such a channel fails loudly on every attempt.
	errs map[string]error
	// noProxy lists hosts contacted directly even when a proxy applies, in
	// NO_PROXY syntax: an exact host, a domain (which also matches its
	// subdomains), or "*" for everything.
	noProxy []string
	// explicit records the channels the operator named individually, as opposed
	// to inheriting the default. Only the ChatOps channel needs this — see
	// chatopsProxyChannel — but it is a property of the configuration, not of
	// that lookup.
	explicit map[string]bool
}

// proxyEndpoint is one resolved proxy: the URL to send through, and the address
// to dial to reach it.
type proxyEndpoint struct {
	url  *url.URL
	addr string // host:port, with the scheme's default port filled in
}

func (e *proxyEndpoint) socks() bool {
	return e.url.Scheme == "socks5" || e.url.Scheme == "socks5h"
}

// relay reports whether the endpoint is a transparent TCP relay rather than a
// proxy. A relay carries anything TCP, which is why it — like SOCKS5, and unlike
// an HTTP proxy — also serves the email and call channels.
func (e *proxyEndpoint) relay() bool { return e.url.Scheme == "tcp" }

// carriesTCP reports whether the endpoint can carry a channel that is not HTTP.
func (e *proxyEndpoint) carriesTCP() bool { return e.socks() || e.relay() }

// redacted renders the endpoint for a log line without its password.
func (e *proxyEndpoint) redacted() string {
	u := *e.url
	if u.User != nil {
		if name := u.User.Username(); name != "" {
			u.User = url.UserPassword(name, "xxxxx")
		} else {
			u.User = nil
		}
	}
	return u.String()
}

// proxyChannels are the delivery channels with a network transport to proxy.
// "log" is absent because it has none.
var proxyChannels = []string{
	"webhook", "slack", "mattermost", "telegram", "chatops", "mobile", "issue",
	"email", "call",
}

// tcpOnlyChannels are the two channels that do not speak HTTP: SMTP and the
// Asterisk AMI socket. They can only be carried by SOCKS5 or a tcp relay — see
// dialFunc.
var tcpOnlyChannels = map[string]bool{"email": true, "call": true}

// operatorURLChannels are the channels whose destination is written in the
// escalation chain or the ChatOps channel rather than fixed by the provider.
// A tcp relay on one of these sends every destination to the same backend, which
// is worth saying out loud — see logStartup.
var operatorURLChannels = map[string]bool{"webhook": true, "issue": true, "chatops": true}

// proxyDefaultPorts fills in the port when the proxy URL omits it. "tcp" is
// absent deliberately: a relay's port is whatever the operator bound the
// frontend to, and guessing one would produce a connection error rather than an
// answer. parseProxyEndpoint requires it.
var proxyDefaultPorts = map[string]string{
	"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080",
}

func proxySettingsFromEnv() proxySettings {
	perChannel := map[string]string{}
	for _, ch := range proxyChannels {
		perChannel[ch] = os.Getenv("NXS_ANOMALY_DELIVERY_PROXY_" + strings.ToUpper(ch) + "_URL")
	}
	return buildProxySettings(
		os.Getenv("NXS_ANOMALY_DELIVERY_PROXY_URL"),
		perChannel,
		os.Getenv("NXS_ANOMALY_DELIVERY_NO_PROXY"),
	)
}

// buildProxySettings parses the configuration. Separate from the environment
// read so tests exercise the same parser rather than a second one that could
// disagree with it.
func buildProxySettings(defaultURL string, perChannel map[string]string, noProxy string) proxySettings {
	s := proxySettings{
		byChannel: map[string]*proxyEndpoint{},
		errs:      map[string]error{},
		noProxy:   splitList(noProxy),
		explicit:  map[string]bool{},
	}
	defaultURL = strings.TrimSpace(defaultURL)
	for _, ch := range proxyChannels {
		raw := strings.TrimSpace(perChannel[ch])
		if raw != "" {
			s.explicit[ch] = true
		} else {
			raw = defaultURL
		}
		if raw == "" || isDirectProxyValue(raw) {
			continue
		}
		ep, err := parseProxyEndpoint(raw)
		if err != nil {
			s.errs[ch] = err
			continue
		}
		s.byChannel[ch] = ep
	}
	return s
}

// isDirectProxyValue reports whether a value means "no proxy for this channel".
func isDirectProxyValue(raw string) bool {
	switch strings.ToLower(raw) {
	case "direct", "none", "off":
		return true
	}
	return false
}

func parseProxyEndpoint(raw string) (*proxyEndpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL %q: %w", raw, err)
	}
	port, known := proxyDefaultPorts[u.Scheme]
	if !known && u.Scheme != "tcp" {
		return nil, fmt.Errorf("unsupported proxy scheme %q (want http, https, socks5, socks5h or tcp)", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy URL %q has no host", raw)
	}
	if p := u.Port(); p != "" {
		port = p
	} else if u.Scheme == "tcp" {
		return nil, fmt.Errorf("tcp relay %q needs an explicit port (for example tcp://relay.internal:443)", raw)
	}
	return &proxyEndpoint{url: u, addr: net.JoinHostPort(u.Hostname(), port)}, nil
}

// bypass reports whether host is contacted directly despite a configured proxy.
// NO_PROXY semantics: an entry matches the host itself and any subdomain of it,
// a leading dot is accepted and ignored, and "*" matches everything.
func (s proxySettings) bypass(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, entry := range s.noProxy {
		entry = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(entry), "."))
		if entry == "*" {
			return true
		}
		if entry == "" {
			continue
		}
		if host == entry || strings.HasSuffix(host, "."+entry) {
			return true
		}
	}
	return false
}

// configured reports whether any channel is proxied at all. Used to keep the
// startup log silent on the overwhelmingly common direct deployment.
func (s proxySettings) configured() bool { return len(s.byChannel) > 0 || len(s.errs) > 0 }

// logStartup states what was understood, once, at startup. An operator who has
// just set these variables needs to see the deployment agree with them — and a
// misparsed URL has to be visible before an alert fires, not after one is lost.
func (s proxySettings) logStartup() {
	if !s.configured() {
		return
	}
	channels := make([]string, 0, len(s.byChannel))
	for ch := range s.byChannel {
		channels = append(channels, ch)
	}
	sort.Strings(channels)
	for _, ch := range channels {
		ep := s.byChannel[ch]
		slog.Info("delivery_proxy_configured",
			"channel", ch, "proxy", ep.redacted(), "no_proxy", strings.Join(s.noProxy, ","))
		if tcpOnlyChannels[ch] && !ep.carriesTCP() {
			// Stated rather than silently applied: SMTP and the AMI socket are
			// not HTTP, so an HTTP proxy cannot carry them, and pretending it
			// does would produce a connection error nobody can explain.
			slog.Warn("delivery_proxy_ignored_for_tcp_channel",
				"channel", ch, "proxy", ep.redacted(),
				"reason", "this channel is plain TCP and can only be carried by SOCKS5 or a tcp:// relay",
				"fix", "set NXS_ANOMALY_DELIVERY_PROXY_"+strings.ToUpper(ch)+"_URL to a socks5:// or tcp:// endpoint, or to \"direct\"")
		}
		if ep.relay() && operatorURLChannels[ch] {
			// A relay has one backend and ignores what was asked for, while this
			// channel's destination is written in the escalation chain. Every
			// destination on it will therefore land on the same server. That is
			// occasionally what an operator wants (a single internal receiver),
			// so it is a warning rather than a refusal — but it must not be
			// discovered from a notification that arrived at the wrong place.
			slog.Warn("delivery_proxy_relay_on_variable_destination",
				"channel", ch, "relay", ep.redacted(),
				"reason", "a tcp relay forwards to its own backend regardless of the destination, and this channel's destination comes from the configuration",
				"fix", "use a tcp relay only for channels with a fixed provider host, or switch this channel to an http:// or socks5:// proxy")
		}
	}
	for _, ch := range sortedKeys(s.errs) {
		slog.Error("delivery_proxy_invalid",
			"channel", ch, "error", s.errs[ch].Error(),
			"effect", "every notification on this channel fails until the proxy URL is corrected")
	}
}

func sortedKeys(m map[string]error) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ProxySummary reports the configured proxy per channel, password removed, for
// the readiness report and for operator-facing diagnostics. Channels that go
// direct are absent.
func (cfg DeliveryConfig) ProxySummary() map[string]string {
	out := map[string]string{}
	for ch, ep := range cfg.proxies.byChannel {
		out[ch] = ep.redacted()
	}
	for ch, err := range cfg.proxies.errs {
		out[ch] = "invalid: " + err.Error()
	}
	return out
}

// newDeliveryClients builds the per-channel HTTP clients. Channels that go
// direct get no entry and fall back to DeliveryConfig.HTTPClient, so a
// deployment without a proxy keeps exactly the client it had before.
func newDeliveryClients(timeout time.Duration, blocked func(net.IP) bool, egress ChannelPolicy, s proxySettings) map[string]*http.Client {
	clients := map[string]*http.Client{}
	for ch, err := range s.errs {
		clients[ch] = failingClient(fmt.Errorf("delivery proxy for channel %q is misconfigured: %w", ch, err))
	}
	for ch, ep := range s.byChannel {
		if tcpOnlyChannels[ch] {
			continue // no HTTP client for SMTP/AMI; see dialFunc
		}
		clients[ch] = newProxiedDeliveryClient(timeout, blocked, egress, ep, s)
	}
	return clients
}

// failingClient turns a configuration error into a delivery failure carrying it.
// The alternative — dropping the proxy and sending directly — is how a typo
// becomes an outage that reads as a provider problem.
func failingClient(err error) *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, err
	})}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newProxiedDeliveryClient builds the delivery client for one proxied channel.
//
// The SSRF guard survives, with one deliberate hole. Dialling still refuses
// private, loopback and link-local addresses (guardedDialContext) for every host
// this client connects to directly — which, with an HTTP proxy, is the NO_PROXY
// destinations. The proxy's own address is exempt: an egress proxy on 10.0.0.5
// is the ordinary deployment, and the operator naming it here is the decision
// the guard would otherwise second-guess.
//
// What cannot survive is dial-time validation of the *destination* through the
// proxy: this process never resolves or dials it, the proxy does. For a proxied
// channel the destination is judged before the request by guardProxiedHost (IP
// literals, and names that resolve locally to a non-public address), on every
// redirect hop by deliveryCheckRedirect (IP literals) and the egress allowlist,
// and that is the whole of the protection. It is stated here rather than hidden because
// it is a real reduction, and it is bounded: the channels people proxy point at
// fixed provider hosts, not at operator-supplied URLs.
func newProxiedDeliveryClient(timeout time.Duration, blocked func(net.IP) bool, egress ChannelPolicy, ep *proxyEndpoint, s proxySettings) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	guarded := guardedDialContext(dialer, blocked)
	transport := http.DefaultTransport.(*http.Transport).Clone()

	switch {
	case ep.relay():
		// Only the dial address changes. transport.Proxy stays nil and the TLS
		// configuration is untouched, so the handshake still uses the request's
		// own host for SNI and for certificate verification — the relay does not
		// terminate TLS and has no certificate of its own to present.
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if s.bypass(host) {
				return guarded(ctx, network, addr)
			}
			return dialer.DialContext(ctx, network, ep.addr)
		}
	case ep.socks():
		socksDial := socksDialContext(ep, dialer)
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if s.bypass(host) {
				return guarded(ctx, network, addr)
			}
			return socksDial(ctx, network, addr)
		}
	default:
		transport.Proxy = func(req *http.Request) (*url.URL, error) {
			if s.bypass(req.URL.Hostname()) {
				return nil, nil
			}
			return ep.url, nil
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == ep.addr {
				return dialer.DialContext(ctx, network, addr)
			}
			return guarded(ctx, network, addr)
		}
	}

	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: deliveryCheckRedirect(blocked, egress),
	}
}

// socksDialContext returns a dial function that reaches addr through the SOCKS5
// proxy. The destination name is handed to the proxy rather than resolved here:
// a network that blocks a provider commonly answers its name with nothing usable.
func socksDialContext(ep *proxyEndpoint, forward *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	var auth *proxy.Auth
	if ep.url.User != nil {
		pass, _ := ep.url.User.Password()
		auth = &proxy.Auth{User: ep.url.User.Username(), Password: pass}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Built per dial rather than once: proxy.SOCKS5 resolves nothing at
		// construction time, but it returns an error for a malformed address,
		// and that error belongs to the attempt it breaks.
		d, err := proxy.SOCKS5("tcp", ep.addr, auth, forward)
		if err != nil {
			return nil, fmt.Errorf("socks5 proxy %s: %w", ep.addr, err)
		}
		if cd, ok := d.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, network, addr)
		}
		return d.Dial(network, addr)
	}
}

// dialFunc returns the dialler for a channel that speaks plain TCP rather than
// HTTP — email (SMTP) and call (Asterisk AMI). nil means "dial directly", which
// is both the unproxied default and what an HTTP proxy resolves to here: HTTP
// proxying is a request-level mechanism, and these two channels have no requests
// to proxy. SOCKS5 and a tcp relay both carry them, since both work below the
// protocol: SOCKS5 by naming the destination in its handshake, the relay by
// having it fixed in its own configuration.
func (s proxySettings) dialFunc(channel string, timeout time.Duration) func(network, addr string) (net.Conn, error) {
	ep := s.byChannel[channel]
	if ep == nil || !ep.carriesTCP() {
		return nil
	}
	dialer := &net.Dialer{Timeout: timeout}
	if ep.relay() {
		// STARTTLS and SMTPS still verify against the server name the caller
		// passed, not against the relay: sendEmail builds its tls.Config from
		// the configured SMTP host and never sees this address.
		return func(network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if s.bypass(host) {
				return dialer.Dial(network, addr)
			}
			return dialer.Dial(network, ep.addr)
		}
	}
	dial := socksDialContext(ep, dialer)
	return func(network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if s.bypass(host) {
			return dialer.Dial(network, addr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return dial(ctx, network, addr)
	}
}
