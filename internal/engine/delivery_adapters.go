package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
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

// ssrfGuard is how the pre-flight judges a destination.
type ssrfGuard uint8

const (
	// guardOff: the SSRF guard is disabled for this installation.
	guardOff ssrfGuard = iota
	// guardDirect: this process resolves and dials the destination itself.
	guardDirect
	// guardProxied: a proxy resolves and dials the destination (see guardProxiedURL).
	guardProxied
)

func ssrfGuardOf(block bool) ssrfGuard {
	if block {
		return guardDirect
	}
	return guardOff
}

// checkWebhookURL is the pre-flight for one destination under the given guard.
func checkWebhookURL(ctx context.Context, rawURL string, guard ssrfGuard) error {
	if guard == guardOff {
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
	if guard == guardProxied {
		return guardProxiedHost(ctx, host)
	}
	var resolver net.Resolver
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve webhook host %q: %w", host, err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip.IP) {
			return fmt.Errorf("blocked webhook host %q resolves to non-public address %s", host, ip.IP)
		}
	}
	return nil
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
func guardProxiedHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("blocked webhook host %s: non-public address", ip)
		}
		return nil
	}
	var resolver net.Resolver
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		if isBlockedIP(ip.IP) {
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
	var blocked func(net.IP) bool
	if block {
		blocked = isBlockedIP
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
func newDeliveryHTTPClientWithPolicy(timeout time.Duration, blocked func(net.IP) bool, egress ChannelPolicy) *http.Client {
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
func deliveryCheckRedirect(blocked func(net.IP) bool, egress ChannelPolicy) func(*http.Request, []*http.Request) error {
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
		// A redirect to a non-public IP literal is refused here, not only at
		// dial time: through a proxy this process never dials the hop, so the
		// dial guard would never see it.
		if ip := net.ParseIP(req.URL.Hostname()); ip != nil && blocked(ip) {
			return fmt.Errorf("redirect refused: non-public address %s", ip)
		}
		return guardWebhookScheme(req.URL)
	}
}

// guardedDialContext returns a DialContext that refuses to connect to any
// address the blocked policy rejects. A nil policy is the ordinary dialler.
func guardedDialContext(dialer *net.Dialer, blocked func(net.IP) bool) func(context.Context, string, string) (net.Conn, error) {
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
			if blocked(ip.IP) {
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

func sendTelegram(ctx context.Context, client *http.Client, chatID, text, token, groupID string, shift telegramShiftOptions, publicURL string) (status, errMsg, providerResp string) {
	if token == "" {
		return "failed", "NXS_ANOMALY_TELEGRAM_BOT_TOKEN is not set", ""
	}
	if chatID == "" {
		return "failed", "telegram target chat id is empty", ""
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	// Telegram API is a fixed public host — never apply the private-IP guard here.
	s, err := postWebhook(ctx, client, url, telegramMessageWithActions(chatID, text, groupID, shift, publicURL), false)
	if s == "delivered" {
		return "delivered", "", "telegram_sendMessage"
	}
	return "failed", err, ""
}

// chatGroupAction is one interactive action on an alert group: the ChatOps
// command word it runs and the label the responder sees.
type chatGroupAction struct {
	command string
	label   string
}

// chatGroupActions map an interactive action to the command it runs. A tap goes
// through the same command path as the typed word, so the team and role
// boundaries cannot differ between the two.
//
// The undo pair is here with the rest rather than in a table of its own: they
// are ordinary group commands, and what makes them undo is only where they are
// offered — on the message a completed action left behind.
var chatGroupActions = map[string]chatGroupAction{
	"ack":       {command: "ack", label: "Acknowledge"},
	"resolve":   {command: "resolve", label: "Resolve"},
	"unack":     {command: "unack", label: "Undo acknowledge"},
	"unresolve": {command: "unresolve", label: "Reopen"},
	"show":      {command: "show", label: "Details"},
}

// silenceOptions are the durations offered as buttons, in minutes.
//
// Silence is the answer neither verdict gives at three in the morning:
// acknowledging claims the incident is being worked, resolving claims it is
// over, and "I have seen it, it is noise, stop calling" is neither. Three
// durations, because a fourth wraps the row on a phone.
var silenceOptions = []int{60, 240, 480}

// silenceLabel names a duration the way a responder reads it, not the way it is
// stored.
func silenceLabel(minutes int) string {
	if minutes%60 == 0 {
		return fmt.Sprintf("Silence %dh", minutes/60)
	}
	return fmt.Sprintf("Silence %dm", minutes)
}

// bulkChatActions are the verbs `bulk` accepts. Kept next to the button builder
// so a keyboard cannot offer a verb the command rejects.
var bulkChatActions = map[string]bool{"ack": true, "silence": true, "resolve": true}

// telegramCallbackDataLimit is Telegram's hard cap on callback_data, in bytes.
const telegramCallbackDataLimit = 64

// telegramMessagePayload builds the sendMessage body, attaching the acknowledge
// and resolve buttons when the notification is about an alert group.
//
// Buttons are omitted rather than truncated when the callback payload would not
// fit: Telegram rejects the whole sendMessage when callback_data is over the
// limit, so an unusually long group id would cost the notification itself.
// Losing a shortcut is recoverable; losing the alert is not.
func telegramMessagePayload(chatID, text, groupID string) map[string]any {
	return telegramMessageWithActions(chatID, text, groupID, telegramShiftOptions{}, "")
}

// telegramShiftOptions are the extras a shift-handover notice carries: it has no
// alert group to act on, but it does have a schedule to answer questions about.
type telegramShiftOptions struct {
	offerCheckin bool
	scheduleID   string
}

// telegramMessageWithActions builds the sendMessage body and the keyboard that
// belongs on it: the alert keyboard for a group, the shift keyboard for a
// handover notice, and none at all for anything else.
func telegramMessageWithActions(chatID, text, groupID string, shift telegramShiftOptions, publicURL string) map[string]any {
	payload := map[string]any{
		"chat_id": chatID,
		// Telegram's sendMessage limit is 4096 UTF-16 code units, not bytes;
		// capping by rune keeps Cyrillic alerts from being cut to half their
		// allowance and, more importantly, from being cut mid-rune — the API
		// rejects invalid UTF-8.
		"text":                     utils.TruncateRunes(text, 4096),
		"disable_web_page_preview": true,
	}
	var rows []any
	if groupID == "" {
		rows = telegramShiftRows(shift)
	} else {
		rows = telegramGroupRows(groupID, publicURL)
	}
	if len(rows) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	return payload
}

// telegramShiftRows are the buttons under a shift-handover notice: confirm the
// shift, take it over, or ask who the schedule currently names.
//
// All three exist as typed commands already, and all three need an argument
// nobody remembers at 09:00 on a Monday — the schedule id. The notice knows it,
// so the button carries it.
func telegramShiftRows(shift telegramShiftOptions) []any {
	if !shift.offerCheckin {
		return nil
	}
	rows := []any{[]any{
		map[string]any{"text": "I am on duty", "callback_data": "duty:on"},
		map[string]any{"text": "Take this shift", "callback_data": "duty:take"},
	}}
	if data := "oncall:" + shift.scheduleID; shift.scheduleID != "" && len(data) <= telegramCallbackDataLimit {
		rows = append(rows, []any{map[string]any{"text": "Who is on call", "callback_data": data}})
	}
	return rows
}

// telegramGroupRows are the buttons under a live alert: the two verdicts, the
// silence durations, and a link out to the group's own page.
func telegramGroupRows(groupID, publicURL string) []any {
	var rows []any
	if row := telegramActionRow(groupID, "ack", "resolve"); len(row) > 0 {
		rows = append(rows, row)
	}
	var silence []any
	for _, minutes := range silenceOptions {
		data := fmt.Sprintf("silence:%d:%s", minutes, groupID)
		if len(data) > telegramCallbackDataLimit {
			continue
		}
		silence = append(silence, map[string]any{"text": silenceLabel(minutes), "callback_data": data})
	}
	if len(silence) > 0 {
		rows = append(rows, silence)
	}
	if btn := telegramGroupLinkButton(groupID, publicURL); btn != nil {
		rows = append(rows, []any{btn})
	}
	return rows
}

// telegramActionRow renders the named actions as one row, in the order given.
//
// Ordered explicitly by the caller: map iteration would shuffle the buttons
// between messages, and a responder taps by position under time pressure.
func telegramActionRow(groupID string, actions ...string) []any {
	var row []any
	for _, action := range actions {
		a, known := chatGroupActions[action]
		if !known {
			continue
		}
		data := action + ":" + groupID
		if len(data) > telegramCallbackDataLimit {
			continue
		}
		row = append(row, map[string]any{"text": a.label, "callback_data": data})
	}
	return row
}

// telegramGroupLinkButton opens the group's own page. A URL button is not a
// callback: the tap never reaches this service, which is why it works even when
// the bot is refusing everything else.
func telegramGroupLinkButton(groupID, publicURL string) map[string]any {
	url := AlertGroupURL(publicURL, groupID)
	if url == "" {
		return nil
	}
	return map[string]any{"text": "Open in nxs-anomaly", "url": url}
}

// AlertGroupURL is where a person reads the whole group: its timeline, its
// alerts and every action a keyboard has no room for.
//
// Empty when the deployment does not know its own public address, and the
// button is then omitted rather than pointed at a URL that would not resolve —
// the same rule the Mattermost buttons already follow.
func AlertGroupURL(publicURL, groupID string) string {
	if publicURL == "" || groupID == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + "/alert-groups/" + groupID
}

// SlackMessagePayload builds a Slack incoming-webhook body, attaching the
// acknowledge and resolve buttons when the notification is about an alert group.
//
// The plain text is kept alongside the blocks: it is what a notification preview
// and a client that cannot render blocks will show, and losing it would mean an
// alert that reads as empty on a phone lock screen.
func SlackMessagePayload(text, groupID, publicURL string) map[string]any {
	payload := map[string]any{"text": text}
	if groupID == "" {
		return payload
	}
	payload["blocks"] = []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		map[string]any{"type": "actions", "elements": slackActionElements(groupID, publicURL)},
	}
	return payload
}

// slackActionElements are the buttons on a Slack alert: the two verdicts, the
// silence durations, and — when this deployment knows its own address — a link
// out to the group's page. The link is a plain URL button, so tapping it never
// reaches this service.
func slackActionElements(groupID, publicURL string) []any {
	var elements []any
	for _, action := range []string{"ack", "resolve"} {
		elements = append(elements, map[string]any{
			"type":      "button",
			"text":      map[string]any{"type": "plain_text", "text": chatGroupActions[action].label},
			"action_id": action,
			"value":     groupID,
		})
	}
	for _, minutes := range silenceOptions {
		elements = append(elements, map[string]any{
			"type":      "button",
			"text":      map[string]any{"type": "plain_text", "text": silenceLabel(minutes)},
			"action_id": fmt.Sprintf("silence:%d", minutes),
			"value":     groupID,
		})
	}
	if url := AlertGroupURL(publicURL, groupID); url != "" {
		elements = append(elements, map[string]any{
			"type":      "button",
			"text":      map[string]any{"type": "plain_text", "text": "Open in nxs-anomaly"},
			"action_id": "open_ui",
			"url":       url,
		})
	}
	return elements
}

// SlackSettledPayload rewrites an alert message once one of its buttons has
// been used: the alert keeps its text, the verdict is appended, and the only
// button left is the one that takes the action back.
//
// The message used to be replaced by the verdict alone — "Acknowledged
// grp_a1b2…" — which threw away the incident the channel had been reading, and
// left the id, which nobody reads.
func SlackSettledPayload(originalText, verdict, undoAction, groupID string) map[string]any {
	text := settledText(originalText, verdict)
	payload := map[string]any{"replace_original": true, "text": text}
	blocks := []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
	}
	if a, known := chatGroupActions[undoAction]; known && groupID != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []any{
			map[string]any{
				"type":      "button",
				"text":      map[string]any{"type": "plain_text", "text": a.label},
				"action_id": undoAction,
				"value":     groupID,
			},
		}})
	}
	payload["blocks"] = blocks
	return payload
}

// settledText is the alert plus the verdict it reached. Falls back to the
// verdict alone when the platform did not hand back what the message said,
// which is the old behaviour and still better than an empty post.
func settledText(originalText, verdict string) string {
	if strings.TrimSpace(originalText) == "" {
		return "✓ " + verdict
	}
	return originalText + "\n\n✓ " + verdict
}

// MattermostMessagePayload builds a Mattermost incoming-webhook body.
//
// Mattermost buttons carry an absolute callback URL rather than an opaque token
// the platform hands back, so they can only be attached when this deployment
// knows its own public address and has an action secret to authenticate the
// callback with. Without either, the alert still goes out — plain. A button that
// posts to a URL nobody answers is worse than no button.
func MattermostMessagePayload(text, groupID, publicURL, actionSecret string) map[string]any {
	payload := map[string]any{"text": text}
	if groupID == "" || publicURL == "" || actionSecret == "" {
		return payload
	}
	var actions []any
	for _, action := range []string{"ack", "resolve"} {
		actions = append(actions, mattermostAction(action, chatGroupActions[action].label, groupID, publicURL, actionSecret))
	}
	for _, minutes := range silenceOptions {
		actions = append(actions,
			mattermostAction(fmt.Sprintf("silence:%d", minutes), silenceLabel(minutes), groupID, publicURL, actionSecret))
	}
	// The attachment carries the buttons and nothing else. It used to repeat the
	// alert text, which Mattermost renders in addition to the message rather
	// than instead of it — so every actionable post showed the incident twice.
	attachment := map[string]any{"actions": actions}
	if url := AlertGroupURL(publicURL, groupID); url != "" {
		// Mattermost has no URL button: a link in the attachment title is how a
		// post sends someone to a page without a callback.
		attachment["title"] = "Open in nxs-anomaly"
		attachment["title_link"] = url
	}
	payload["attachments"] = []any{attachment}
	return payload
}

// mattermostAction builds one interactive button. The secret rides in the
// context because Mattermost does not sign these callbacks: it is the only
// credential the request carries, and it is compared in constant time on
// arrival.
func mattermostAction(action, label, groupID, publicURL, actionSecret string) map[string]any {
	return map[string]any{
		"id":   action,
		"name": label,
		"integration": map[string]any{
			"url": strings.TrimRight(publicURL, "/") + "/integrations/v1/chatops/mattermost",
			"context": map[string]any{
				"action":   action,
				"group_id": groupID,
				"token":    actionSecret,
			},
		},
	}
}

// MattermostSettledUpdate rewrites a post once one of its buttons has been used.
//
// The message itself is deliberately absent from the update: Mattermost leaves
// out what an update does not name, so omitting it keeps the alert exactly as
// the channel has been reading it. Replacing it with the verdict — which is
// what this used to do — turned the incident into an identifier.
func MattermostSettledUpdate(verdict, undoAction, groupID, publicURL, actionSecret string) map[string]any {
	attachment := map[string]any{"text": "✓ " + verdict}
	if a, known := chatGroupActions[undoAction]; known && groupID != "" && publicURL != "" && actionSecret != "" {
		attachment["actions"] = []any{mattermostAction(undoAction, a.label, groupID, publicURL, actionSecret)}
	}
	return map[string]any{
		"update": map[string]any{
			"props": map[string]any{"attachments": []any{attachment}},
		},
	}
}

// ChatActionCommand turns an interactive action name and its target into the
// ChatOps command it stands for, so Slack and Mattermost buttons resolve through
// the same table as the Telegram ones.
func ChatActionCommand(action, groupID string) (string, bool) {
	if groupID == "" {
		return "", false
	}
	// An action name is either a plain verb ("ack") or a verb carrying its own
	// argument ("silence:60"). One encoding serves all three platforms: it fits
	// a Telegram callback, a Slack action_id and a Mattermost context alike.
	if verb, arg, carriesArg := strings.Cut(action, ":"); carriesArg {
		if verb != "silence" {
			return "", false
		}
		minutes, err := strconv.Atoi(arg)
		if err != nil || minutes <= 0 {
			return "", false
		}
		return fmt.Sprintf("silence %s %d", groupID, minutes), true
	}
	a, known := chatGroupActions[action]
	if !known {
		return "", false
	}
	return a.command + " " + groupID, true
}

// TelegramUndoRow is the single-button keyboard left on a settled alert, or nil
// when the action that settled it cannot be taken back.
func TelegramUndoRow(undoAction, groupID string) []any {
	if undoAction == "" || groupID == "" {
		return nil
	}
	row := telegramActionRow(groupID, undoAction)
	if len(row) == 0 {
		return nil
	}
	return row
}

// UndoActionFor names the action that takes a completed one back, or "" when
// there is nothing to undo. It is what puts a single button on a settled
// message: a mis-tapped Resolve from a phone was otherwise unfixable without
// opening the web UI.
func UndoActionFor(command string) string {
	switch {
	case strings.HasPrefix(command, "ack "):
		return "unack"
	case strings.HasPrefix(command, "resolve "):
		return "unresolve"
	}
	return ""
}

// ParseTelegramCallbackData turns a tapped button back into a ChatOps command,
// reporting false for anything this service did not put on a keyboard. It lives
// next to the code that builds the buttons so the two cannot drift.
func ParseTelegramCallbackData(data string) (string, bool) {
	verb, rest, found := strings.Cut(data, ":")
	if !found || rest == "" {
		return "", false
	}
	switch verb {
	case "silence":
		// silence:<minutes>:<group_id> — the duration is part of the button, so
		// the person picks it by tapping rather than by typing.
		minutes, groupID, ok := strings.Cut(rest, ":")
		if !ok {
			return "", false
		}
		return ChatActionCommand("silence:"+minutes, groupID)
	case "bulk":
		// bulk:<verb> or bulk:silence:<minutes> — the storm case, where acting
		// on one group at a time is the problem.
		what, arg, carriesArg := strings.Cut(rest, ":")
		if !bulkChatActions[what] {
			return "", false
		}
		if carriesArg {
			return "bulk " + what + " " + arg, true
		}
		return "bulk " + what, true
	case "alerts":
		return "alerts " + rest, true
	case "duty":
		if rest != "on" && rest != "off" && rest != "take" {
			return "", false
		}
		return "duty " + rest, true
	case "oncall":
		return "oncall " + rest, true
	default:
		return ChatActionCommand(verb, rest)
	}
}

// TelegramReplyPayload renders a command's answer as a Telegram message body,
// attaching a keyboard when the answer is something to act on.
//
// It takes the engine's own result map rather than a rendered string because the
// listing needs the groups themselves, not the sentence describing them.
func TelegramReplyPayload(chatID, text string, result map[string]any, publicURL string) map[string]any {
	return telegramReplyPayload(chatID, text, result, publicURL)
}

// telegramReplyPayload is the unexported body, so the engine's own callers and
// its tests reach the keyboards without going through the exported name.
func telegramReplyPayload(chatID, text string, result map[string]any, publicURL string) map[string]any {
	response, _ := result["response"].(map[string]any)
	if response == nil {
		return telegramMessagePayload(chatID, text, "")
	}
	// A card is about one group, so it carries that group's own keyboard.
	if card, ok := response["alert_group_card"].(map[string]any); ok {
		return telegramMessageWithActions(chatID, text, utils.StrVal(card, "id"),
			telegramShiftOptions{}, publicURL)
	}
	items, isListing := response["alerts_page"].([]map[string]any)
	if !isListing {
		// The greeting is the only other answer with something to press.
		if utils.BoolVal(response, "offer_start_keyboard", false) {
			payload := telegramMessagePayload(chatID, text, "")
			payload["reply_markup"] = map[string]any{"inline_keyboard": telegramStartRows()}
			return payload
		}
		return telegramMessagePayload(chatID, text, "")
	}
	payload := telegramMessagePayload(chatID, text, "")
	if rows := telegramListingRows(items, response); len(rows) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	return payload
}

// telegramStartRows are the buttons the bot offers on first contact: the three
// things a responder opens it for, none of which needs an argument.
func telegramStartRows() []any {
	return []any{
		[]any{map[string]any{"text": "My open alerts", "callback_data": "alerts:1"}},
		[]any{
			map[string]any{"text": "I am on duty", "callback_data": "duty:on"},
			map[string]any{"text": "I am off duty", "callback_data": "duty:off"},
		},
	}
}

// telegramListingRows draws one page of the alert listing.
//
// Each group gets a row of two: the label opens the group, the narrow button
// acknowledges it. They used to be one button whose label read "[critical] Disk
// full" and whose action was acknowledge — a label promising navigation and a
// tap performing a state change, with no confirmation and no way back.
func telegramListingRows(items []map[string]any, response map[string]any) []any {
	var rows []any
	for _, item := range items {
		id := utils.StrVal(item, "id")
		open := "show:" + id
		ack := "ack:" + id
		if len(open) > telegramCallbackDataLimit || len(ack) > telegramCallbackDataLimit {
			continue
		}
		rows = append(rows, []any{
			map[string]any{"text": alertButtonLabel(item), "callback_data": open},
			map[string]any{"text": "✓", "callback_data": ack},
		})
	}
	if len(rows) > 0 {
		// Offered on the listing rather than on an alert, because this is where
		// the storm is visible: forty groups is where acting one at a time
		// stops being possible.
		rows = append(rows, []any{
			map[string]any{"text": "Acknowledge all open", "callback_data": "bulk:ack"},
			map[string]any{"text": "Silence all 1h", "callback_data": "bulk:silence:60"},
		})
	}
	if nav := telegramPagerRow(utils.IntVal(response, "page"), utils.IntVal(response, "pages")); nav != nil {
		rows = append(rows, nav)
	}
	return rows
}

// alertButtonLabel names a group on its button: severity first, because that is
// what decides whether it is worth tapping, then as much title as fits.
func alertButtonLabel(item map[string]any) string {
	severity := strDefault(utils.StrVal(item, "severity"), "unknown")
	title := strDefault(utils.StrVal(item, "title"), "alert")
	return utils.TruncateRunes(fmt.Sprintf("[%s] %s", severity, title), 60)
}

// telegramPagerRow builds the previous/next row, or nil when everything fits on
// one page. Only reachable pages get a button: a dead "next" on the last page
// invites a tap that does nothing.
func telegramPagerRow(page, pages int) []any {
	if pages <= 1 {
		return nil
	}
	var row []any
	if page > 1 {
		row = append(row, map[string]any{
			"text": "‹ Previous", "callback_data": "alerts:" + strconv.Itoa(page-1),
		})
	}
	if page < pages {
		row = append(row, map[string]any{
			"text": "Next ›", "callback_data": "alerts:" + strconv.Itoa(page+1),
		})
	}
	return row
}

func callMessage(payload map[string]any) string {
	severity := strDefault(utils.StrVal(payload, "severity"), "unknown")
	title := strDefault(utils.StrVal(payload, "title"), "alert")
	if severity == "critical" {
		return fmt.Sprintf("Emergency alert. %s. See messages for details.", title)
	}
	return fmt.Sprintf("Alert. %s. See messages for details.", title)
}

// emailSubject names what the message is about, so a mailbox of pages can be
// triaged — and threaded — per incident. The old fixed subject made every alert
// the same conversation.
func emailSubject(payload map[string]any) string {
	if utils.StrVal(payload, "kind") == "report_digest" {
		return "On-call quality report"
	}
	title := utils.StrVal(payload, "title")
	if title == "" {
		return "nxs-anomaly notification"
	}
	subject := title
	if sev := utils.StrVal(payload, "severity"); sev != "" {
		subject = "[" + sev + "] " + subject
	}
	if utils.StrVal(payload, "status") == "resolved" {
		subject = "[RESOLVED] " + subject
	}
	return subject
}

// buildEmailMessage renders an RFC 5322 message with a UTF-8 body. Date and
// Message-ID are required or expected by receiving servers; without MIME
// headers a Cyrillic alert title arrived as mojibake or not at all.
func buildEmailMessage(from, recipient, subject, body string, now time.Time, host string) []byte {
	var qp bytes.Buffer
	w := quotedprintable.NewWriter(&qp)
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", recipient)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", utils.MakeID("msg"), strDefault(host, "nxs-anomaly"))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	b.Write(qp.Bytes())
	return b.Bytes()
}

func sendEmail(recipient, subject, text string, cfg SMTPConfig) (status, errMsg, providerResp string) {
	if cfg.Host == "" {
		return "failed", "NXS_ANOMALY_SMTP_HOST is not set", ""
	}
	if recipient == "" {
		return "failed", "email target recipient is empty", ""
	}
	host := cfg.Host
	port := cfg.Port
	username := cfg.Username
	password := cfg.Password
	sender := cfg.Sender
	senderName := cfg.From
	useTLS := cfg.UseTLS

	from := fmt.Sprintf("%s <%s>", senderName, sender)
	msgHost := sender
	if i := strings.LastIndex(sender, "@"); i >= 0 {
		msgHost = sender[i+1:]
	}
	msg := buildEmailMessage(from, recipient, subject, text, time.Now(), msgHost)
	addr := fmt.Sprintf("%s:%d", host, port)

	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	// Without a proxy this is the historical path verbatim: tls.Dial or
	// smtp.SendMail. With a SOCKS5 proxy configured for the email channel the TCP
	// connection has to come from the proxy dialler, so TLS is layered on top of
	// it by hand and the plain path drives the client directly.
	if cfg.Dial != nil {
		return sendEmailVia(cfg.Dial, addr, host, sender, recipient, msg, auth, useTLS)
	}

	var sendErr error
	if useTLS {
		tlsConf := &tls.Config{ServerName: host}
		conn, err := tls.Dial("tcp", addr, tlsConf)
		if err != nil {
			return "failed", err.Error(), ""
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			return "failed", err.Error(), ""
		}
		defer client.Close() //nolint:errcheck
		if auth != nil {
			if err := client.Auth(auth); err != nil {
				return "failed", err.Error(), ""
			}
		}
		if err := client.Mail(sender); err != nil {
			return "failed", err.Error(), ""
		}
		if err := client.Rcpt(recipient); err != nil {
			return "failed", err.Error(), ""
		}
		w, err := client.Data()
		if err != nil {
			return "failed", err.Error(), ""
		}
		if _, sendErr = w.Write(msg); sendErr == nil {
			sendErr = w.Close()
		}
	} else {
		sendErr = smtp.SendMail(addr, auth, sender, []string{recipient}, msg)
	}
	if sendErr != nil {
		return "failed", sendErr.Error(), ""
	}
	return "delivered", "", "smtp"
}

// sendEmailVia sends one message over a connection the given dialler produced —
// the SOCKS5 path. It mirrors what net/smtp does for us on the direct path,
// including opportunistic STARTTLS on the plain port: dropping that would make
// proxying an SMTP server quietly downgrade the connection.
func sendEmailVia(dial func(network, addr string) (net.Conn, error), addr, host, sender, recipient string, msg []byte, auth smtp.Auth, useTLS bool) (status, errMsg, providerResp string) {
	conn, err := dial("tcp", addr)
	if err != nil {
		return "failed", err.Error(), ""
	}
	if useTLS {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close() //nolint:errcheck
			return "failed", err.Error(), ""
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close() //nolint:errcheck
		return "failed", err.Error(), ""
	}
	defer client.Close() //nolint:errcheck
	if !useTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return "failed", err.Error(), ""
			}
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return "failed", err.Error(), ""
		}
	}
	if err := client.Mail(sender); err != nil {
		return "failed", err.Error(), ""
	}
	if err := client.Rcpt(recipient); err != nil {
		return "failed", err.Error(), ""
	}
	w, err := client.Data()
	if err != nil {
		return "failed", err.Error(), ""
	}
	if _, err := w.Write(msg); err != nil {
		return "failed", err.Error(), ""
	}
	if err := w.Close(); err != nil {
		return "failed", err.Error(), ""
	}
	return "delivered", "", "smtp"
}

func sendAsteriskCall(phone, message string, instances []asteriskInstance) (status, errMsg, providerResp string) {
	if len(instances) == 0 {
		return "failed", "No Asterisk instance configured", ""
	}
	var lastErr string
	for _, inst := range instances {
		s, e, r := sendAsteriskCallToInstance(inst, phone, message)
		if s == "delivered" {
			return s, e, r
		}
		lastErr = e
	}
	if lastErr == "" {
		lastErr = "All Asterisk instances failed"
	}
	return "failed", lastErr, ""
}

type asteriskInstance struct {
	host, username, secret, channel, context, exten, priority, callerID, variableName string
	// dial, when set, opens the AMI socket through the configured SOCKS5 proxy.
	// nil is the direct dial this has always done.
	dial func(network, addr string) (net.Conn, error)
}

func sendAsteriskCallToInstance(inst asteriskInstance, phone, message string) (status, errMsg, providerResp string) {
	if inst.host == "" || inst.username == "" || inst.secret == "" ||
		inst.channel == "" || inst.context == "" || inst.exten == "" || phone == "" {
		return "failed", fmt.Sprintf("Asterisk AMI settings incomplete for %s", inst.host), ""
	}
	ch := inst.channel
	if !strings.HasSuffix(ch, "/") {
		ch += "/"
	}
	ch += phone

	dial := inst.dial
	if dial == nil {
		dial = func(network, addr string) (net.Conn, error) {
			return net.DialTimeout(network, addr, 5*time.Second)
		}
	}
	conn, err := dial("tcp", inst.host+":5038")
	if err != nil {
		return "failed", err.Error(), ""
	}
	defer conn.Close() //nolint:errcheck

	cmd := strings.Join([]string{
		"Action: Login",
		"Username: " + inst.username,
		"Secret: " + inst.secret,
		"Events: off",
		"",
		"Action: Originate",
		"Channel: " + ch,
		"Context: " + inst.context,
		"Exten: " + inst.exten,
		"Priority: " + inst.priority,
		"CallerID: " + inst.callerID,
		"Async: true",
		"Variable: " + inst.variableName + "=" + message,
		"",
		"Action: Logoff",
		"",
	}, "\r\n")

	// Set a deadline so we never block past the AMI exchange window.
	conn.SetDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	if _, err := conn.Write([]byte(cmd + "\r\n")); err != nil {
		return "failed", err.Error(), ""
	}
	// ReadAll reads until Asterisk closes the connection after Logoff.
	respBytes, _ := io.ReadAll(conn)
	resp := string(respBytes)
	if strings.Contains(resp, "Response: Error") {
		return "failed", resp, ""
	}
	return "delivered", "", "asterisk_ami_originate"
}

func createRedmineIssue(ctx context.Context, client *http.Client, url, token, project, subject, body string) (status, issueID, errMsg string) {
	apiURL := strings.TrimRight(url, "/") + "/issues.json"
	payload := map[string]any{
		"issue": map[string]any{
			"project_id":  project,
			"subject":     subject,
			"description": body,
		},
	}
	bodyBytes := []byte(utils.JSONDumps(payload))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "failed", "", err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Redmine-API-Key", token)
	resp, err := client.Do(req)
	if err != nil {
		return "failed", "", err.Error()
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "failed", "", fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return "created", "", ""
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

func createGenericIssue(ctx context.Context, client *http.Client, url, token, subject, body string) (status, issueID, errMsg string) {
	payload := map[string]any{"subject": subject, "body": body}
	bodyBytes := []byte(utils.JSONDumps(payload))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "failed", "", err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "failed", "", err.Error()
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "failed", "", fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return "created", "", ""
}
