package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// guardWebhookURL rejects URLs whose host resolves to a private, loopback,
// link-local, or unspecified address. It is an opt-in SSRF guard: callers pass
// block=false to skip the check (the default), or block=true to enforce it.
//
// This is the FIRST of two lines of defence and the weaker one: it judges the
// URL the caller supplied, before any request is made. On its own it was
// bypassable two ways — a 302 to a private address (the guard never sees the
// redirect target) and DNS rebinding (the address checked here and the address
// dialled a moment later are two independent lookups). It is kept because it
// produces the clear, early error an operator wants when a webhook is simply
// misconfigured. The line of defence that actually holds is in the transport:
// guardedDialContext validates the exact IP being connected to, on every hop.
// See newDeliveryHTTPClient.
func guardWebhookURL(ctx context.Context, rawURL string, block bool) error {
	return checkWebhookURL(ctx, rawURL, ssrfGuardOf(block))
}

// ssrfMode is how the pre-flight judges a destination.
type ssrfMode uint8

const (
	// modeOff: the SSRF guard is disabled for this installation.
	modeOff ssrfMode = iota
	// modeDirect: this process resolves and dials the destination itself.
	modeDirect
	// modeProxied: a proxy resolves and dials the destination (see guardProxiedHost).
	modeProxied
)

// ssrfGuard is the pre-flight for one channel: its mode and the installation's
// exceptions (NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS_EXCEPT), which the pre-flight
// has to honour exactly as the dialler does — otherwise an excepted receiver
// would pass the dial check and never get that far.
type ssrfGuard struct {
	mode   ssrfMode
	exempt *ssrfExemptions
}

var (
	guardOff     = ssrfGuard{mode: modeOff}
	guardDirect  = ssrfGuard{mode: modeDirect}
	guardProxied = ssrfGuard{mode: modeProxied}
)

// blocks reports whether the pre-flight refuses ip reached under host.
func (g ssrfGuard) blocks(host string, ip net.IP) bool {
	return isBlockedIP(ip) && !g.exempt.allows(host, ip)
}

func ssrfGuardOf(block bool) ssrfGuard {
	if block {
		return guardDirect
	}
	return guardOff
}

// checkWebhookURL is the pre-flight for one destination under the given guard.
func checkWebhookURL(ctx context.Context, rawURL string, guard ssrfGuard) error {
	if guard.mode == modeOff {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	if err := guardWebhookScheme(u); err != nil {
		return err
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("blocked webhook URL with empty host")
	}
	if guard.mode == modeProxied {
		return guardProxiedHost(ctx, host, guard)
	}
	ips, err := preflightLookup(ctx, host)
	if err != nil {
		return &unresolvedHostError{host: host, err: err}
	}
	for _, ip := range ips {
		if guard.blocks(host, ip.IP) {
			return fmt.Errorf("blocked webhook host %q resolves to non-public address %s", host, ip.IP)
		}
	}
	return nil
}

// preflightLookup resolves a destination for the direct pre-flight. A variable
// so tests can make the resolver fail and then recover.
var preflightLookup = (&net.Resolver{}).LookupIPAddr

// unresolvedHostError is a pre-flight that reached no verdict: the name did not
// resolve, so there was no address to judge. Every other pre-flight error is a
// verdict about the destination itself. The two must not share an outcome — a
// resolver outage of a few seconds used to mark the notification skipped as a
// blocked destination, terminally, so the page was never sent.
type unresolvedHostError struct {
	host string
	err  error
}

func (e *unresolvedHostError) Error() string {
	return fmt.Sprintf("resolve webhook host %q: %v", e.host, e.err)
}

func (e *unresolvedHostError) Unwrap() error { return e.err }

// isUnresolvedHost reports whether a pre-flight error is a failed lookup
// rather than a refusal.
func isUnresolvedHost(err error) bool {
	var u *unresolvedHostError
	return errors.As(err, &u)
}

// guardProxiedHost is the pre-flight for a destination a proxy will reach.
//
// Turning the guard off for proxied channels outright left the SSRF protection
// with nothing: NXS_ANOMALY_DELIVERY_PROXY_URL is the default for every channel,
// webhooks included, so a user-supplied http://169.254.169.254/ went through
// the proxy and the proxy host's metadata service answered (seen on the EE
// stand: HTTP 405 from the far side of a SOCKS proxy). What this process can
// still judge, it judges:
//
//   - an IP literal needs no resolution, so a private, loopback or link-local
//     one is refused exactly as it would be on a direct channel;
//   - a name is resolved locally, and a private answer is refused;
//   - no local answer is not a verdict — in the networks a proxy is deployed
//     for, the proxy is often the only thing that can resolve public names — so
//     the request goes ahead and the proxy resolves it.
//
// A name that only the proxy's resolver maps to an internal address is outside
// what this process can see; the egress allowlist is the control for that.
func guardProxiedHost(ctx context.Context, host string, guard ssrfGuard) error {
	return judgeProxiedHost(ctx, host, guard.blocks)
}

// loopbackNameAddrs stand in for what a "localhost" name reaches.
var loopbackNameAddrs = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}

// judgeProxiedHost is the verdict guardProxiedHost gives the first hop, as a
// function of the block policy alone, so that every redirect hop gets the same
// one (deliveryCheckRedirect). It used to be applied to the configured URL
// only: a redirect to a name was never judged, and through a proxy the dial
// guard never sees a hop either — a 307 to http://localhost/private reached
// the proxy with the notification body (GitHub #52).
//
// "localhost" and its subdomains are loopback by definition (RFC 6761) on
// whichever machine resolves them, the proxy included, so they are judged as
// 127.0.0.1 and ::1 without asking the local resolver — whose answer would not
// be the proxy's anyway. An exemption for the name still applies.
func judgeProxiedHost(ctx context.Context, host string, blocked blockPolicy) error {
	if blocked == nil {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if blocked(host, ip) {
			return fmt.Errorf("blocked webhook host %s: non-public address", ip)
		}
		return nil
	}
	if name := strings.TrimSuffix(strings.ToLower(host), "."); name == "localhost" || strings.HasSuffix(name, ".localhost") {
		for _, ip := range loopbackNameAddrs {
			if blocked(host, ip) {
				return fmt.Errorf("blocked webhook host %q: loopback name", host)
			}
		}
		return nil
	}
	var resolver net.Resolver
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil //nolint:nilerr // deliberate: behind a proxy only the proxy may resolve public names (see guardProxiedHost)
	}
	for _, ip := range ips {
		if blocked(host, ip.IP) {
			return fmt.Errorf("blocked webhook host %q resolves to non-public address %s", host, ip.IP)
		}
	}
	return nil
}

func guardWebhookScheme(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("blocked webhook scheme %q", u.Scheme)
	}
	return nil
}

// isBlockedIP reports whether ip is in a range that should never be a webhook target.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// Cloud metadata endpoint (169.254.169.254) is already link-local; also block
	// the IPv6 unique-local range fc00::/7 which IsPrivate covers, kept explicit here.
	return false
}

// deliveryRedirectLimit matches net/http's own default. Stated explicitly here
// because supplying CheckRedirect replaces that default rather than adding to it.
const deliveryRedirectLimit = 10

// newDeliveryHTTPClient builds the client used for every outbound webhook.
//
// When block is true the SSRF guard lives in the transport, which is the only
// place it can be complete:
//
//   - guardedDialContext resolves the host and refuses the connection if any
//     answer is a private/loopback/link-local address, then dials the vetted IP
//     directly. Because the address that was checked is the address that is
//     dialled, the name cannot resolve differently in between (DNS rebinding).
//   - Redirects go through the same dialler, so a 302 from a public host to
//     169.254.169.254 is refused at connect time. The pre-flight URL check never
//     sees redirect targets and cannot cover this.
//
// CheckRedirect additionally rejects non-HTTP schemes and restates the hop limit
// that supplying the hook would otherwise discard.
func newDeliveryHTTPClient(timeout time.Duration, block bool, egress ChannelPolicy) *http.Client {
	var blocked blockPolicy
	if block {
		blocked = ipPolicy(isBlockedIP)
	}
	return newDeliveryHTTPClientWithPolicy(timeout, blocked, egress)
}

// newDeliveryHTTPClientWithPolicy is newDeliveryHTTPClient with the blocklist
// supplied rather than assumed; a nil policy means no guard. Production always
// passes isBlockedIP. It is a separate constructor because the guard cannot
// otherwise be tested honestly: every address a test can bind is itself
// loopback, so isBlockedIP would reject the stand-in for the *public* first hop
// and the redirect would never be exercised. Injecting the policy lets a test
// designate one loopback address "public" and another "the metadata endpoint",
// which exercises the real question here — whether the blocklist is consulted
// on every hop — while isBlockedIP's own contents are tested separately.
func newDeliveryHTTPClientWithPolicy(timeout time.Duration, blocked blockPolicy, egress ChannelPolicy) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = guardedDialContext(dialer, blocked)
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: deliveryCheckRedirect(blocked, egress),
	}
}

// deliveryCheckRedirect is the redirect policy shared by the direct and the
// proxied delivery clients: same hop limit, same allowlist, same scheme rule
// wherever a notification goes out.
func deliveryCheckRedirect(blocked blockPolicy, egress ChannelPolicy) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= deliveryRedirectLimit {
			return fmt.Errorf("stopped after %d redirects", deliveryRedirectLimit)
		}
		// The egress allowlist is checked on every hop, not only on the
		// URL somebody configured. A redirect is a destination this
		// installation did not choose, and "we only send to approved
		// parties" has to survive a 302 to be worth stating.
		if ok, detail := egress.DestinationAllowed(req.URL.String()); !ok {
			return fmt.Errorf("redirect refused: %s", detail)
		}
		if blocked == nil {
			return nil
		}
		// Every hop is judged here as the first one is (judgeProxiedHost), not
		// only at dial time: through a proxy this process never dials the hop,
		// so the dial guard would never see it. On a direct client the dial
		// guard checks again, against the address it actually connects to.
		if err := judgeProxiedHost(req.Context(), req.URL.Hostname(), blocked); err != nil {
			return fmt.Errorf("redirect refused: %w", err)
		}
		return guardWebhookScheme(req.URL)
	}
}

// guardedDialContext returns a DialContext that refuses to connect to any
// address the blocked policy rejects. A nil policy is the ordinary dialler.
func guardedDialContext(dialer *net.Dialer, blocked blockPolicy) func(context.Context, string, string) (net.Conn, error) {
	if blocked == nil {
		return dialer.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var resolver net.Resolver
		ips, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve webhook host %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("webhook host %q resolved to no addresses", host)
		}
		// Any blocked answer rejects the whole connection rather than falling
		// through to a sibling public address. A name that answers with both a
		// public and a private address is the shape of a rebinding attempt, not
		// of an ordinary misconfiguration, and picking the public one would let
		// the attacker simply retry until timing favours them.
		for _, ip := range ips {
			if blocked(host, ip.IP) {
				return nil, fmt.Errorf("blocked webhook host %q resolves to non-public address %s", host, ip.IP)
			}
		}
		var lastErr error
		for _, ip := range ips {
			// Dial the vetted IP, not the name: re-resolving here is exactly the
			// gap this function exists to close. TLS is unaffected — the
			// transport takes SNI and certificate verification from the request
			// URL, not from the dial address.
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

func postWebhook(ctx context.Context, client *http.Client, url string, payload map[string]any, blockPrivate bool) (status, errMsg string) {
	res := postWebhookGuarded(ctx, client, url, payload, ssrfGuardOf(blockPrivate), nil)
	if res.Status == deliveryDelivered {
		return "delivered", ""
	}
	return "failed", res.Err
}

// postWebhookGuarded is postWebhook with the provider's answer kept: status
// code and a bounded excerpt of the body. Diagnosing "the webhook returned 403
// with «channel archived»" needs both, and neither survived the old two-string
// return. The SSRF pre-flight is the one chosen for the channel (ssrfGuardFor).
func postWebhookGuarded(ctx context.Context, client *http.Client, url string, payload map[string]any, guard ssrfGuard, headers map[string]string) deliveryOutcome {
	if err := checkWebhookURL(ctx, url, guard); err != nil {
		// A failed lookup is not a verdict: DNS that answers on the next
		// attempt makes the destination deliverable, so it is retried like any
		// other transport failure, and dead-letters if it never recovers.
		if isUnresolvedHost(err) {
			return failed("dns_lookup", err.Error(), 0, "")
		}
		// Terminal, like a refusal by the channel policy: the destination does
		// not change between attempts, so retrying it twice only delays the
		// moment the reason reaches the timeline.
		return skipped(skipBlockedDestination, err.Error())
	}
	body := []byte(utils.JSONDumps(payload))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return failed("http_post", err.Error(), 0, "")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return failed("http_post", err.Error(), 0, "")
	}
	defer resp.Body.Close() //nolint:errcheck
	// Read a bounded prefix: the excerpt is capped anyway, and an endpoint that
	// streams megabytes must not stall the delivery cycle.
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponse*2))
	return httpOutcome("http_post", resp.StatusCode, string(excerpt), "", resp.Header.Get("Retry-After"))
}

// toAlertmanagerPayload wraps an nxs-anomaly notification payload in the
// standard Alertmanager webhook envelope so that receivers designed for
// Alertmanager (e.g. nxs-prometheus-alerter-anomaly) can parse it without
// template changes.
func toAlertmanagerPayload(p map[string]any) map[string]any {
	labels, _ := p["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	// Ensure alertname is present.
	if _, ok := labels["alertname"]; !ok {
		if title := utils.StrVal(p, "title"); title != "" {
			labels["alertname"] = title
		}
	}
	// Ensure severity is present.
	if _, ok := labels["severity"]; !ok {
		if sev := utils.StrVal(p, "severity"); sev != "" {
			labels["severity"] = sev
		}
	}

	amStatus := "firing"
	if utils.StrVal(p, "status") == "resolved" {
		amStatus = "resolved"
	}

	annotations := map[string]any{}
	if title := utils.StrVal(p, "title"); title != "" {
		annotations["summary"] = title
	}

	alert := map[string]any{
		"status":       amStatus,
		"labels":       labels,
		"annotations":  annotations,
		"startsAt":     strDefault(utils.StrVal(p, "starts_at"), utils.ToISO(utils.UTCNow())),
		"endsAt":       "0001-01-01T00:00:00Z",
		"generatorURL": "",
		"fingerprint":  "",
	}

	return map[string]any{
		"receiver":          "nxs-anomaly",
		"status":            amStatus,
		"alerts":            []any{alert},
		"groupLabels":       labels,
		"commonLabels":      labels,
		"commonAnnotations": annotations,
		"externalURL":       "",
		"version":           "4",
		"groupKey":          utils.StrVal(p, "alert_group_id"),
		"truncatedAlerts":   0,
	}
}
