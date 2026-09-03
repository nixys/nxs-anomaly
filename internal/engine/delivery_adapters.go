package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
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
	if !block {
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
	res := postWebhookDetailed(ctx, client, url, payload, blockPrivate, nil)
	if res.Status == deliveryDelivered {
		return "delivered", ""
	}
	return "failed", res.Err
}

// postWebhookDetailed is postWebhook with the provider's answer kept: status
// code and a bounded excerpt of the body. Diagnosing "the webhook returned 403
// with «channel archived»" needs both, and neither survived the old two-string
// return.
func postWebhookDetailed(ctx context.Context, client *http.Client, url string, payload map[string]any, blockPrivate bool, headers map[string]string) deliveryOutcome {
	if err := guardWebhookURL(ctx, url, blockPrivate); err != nil {
		return failed("http_post", err.Error(), 0, "")
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

func sendTelegram(ctx context.Context, client *http.Client, chatID, text, token, groupID string, offerCheckin bool) (status, errMsg, providerResp string) {
	if token == "" {
		return "failed", "NXS_ANOMALY_TELEGRAM_BOT_TOKEN is not set", ""
	}
	if chatID == "" {
		return "failed", "telegram target chat id is empty", ""
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	// Telegram API is a fixed public host — never apply the private-IP guard here.
	s, err := postWebhook(ctx, client, url, telegramMessageWithActions(chatID, text, groupID, offerCheckin), false)
	if s == "delivered" {
		return "delivered", "", "telegram_sendMessage"
	}
	return "failed", err, ""
}

// telegramActions map an inline button to the ChatOps command it runs, and to
// the label the responder sees. A tap goes through the same command path as the
// typed word, so the team and role boundaries cannot differ between the two.
var telegramActions = map[string]string{
	"ack":     "Acknowledge",
	"resolve": "Resolve",
}

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
	return telegramMessageWithActions(chatID, text, groupID, false)
}

// telegramMessageWithActions is telegramMessagePayload with the shift-handover
// variant: no alert group to act on, one button that confirms the shift.
func telegramMessageWithActions(chatID, text, groupID string, offerCheckin bool) map[string]any {
	payload := map[string]any{
		"chat_id": chatID,
		// Telegram's sendMessage limit is 4096 UTF-16 code units, not bytes;
		// capping by rune keeps Cyrillic alerts from being cut to half their
		// allowance and, more importantly, from being cut mid-rune — the API
		// rejects invalid UTF-8.
		"text":                     utils.TruncateRunes(text, 4096),
		"disable_web_page_preview": true,
	}
	if groupID == "" {
		if offerCheckin {
			// The shift notice already reached the person; asking them to type a
			// command to confirm it is a step nobody takes at 09:00 on a Monday.
			payload["reply_markup"] = map[string]any{"inline_keyboard": []any{
				[]any{map[string]any{"text": "I am on duty", "callback_data": "duty:on"}},
			}}
		}
		return payload
	}
	var row []any
	// Ordered explicitly: map iteration would shuffle the buttons between
	// messages, and a responder taps by position under time pressure.
	for _, action := range []string{"ack", "resolve"} {
		data := action + ":" + groupID
		if len(data) > telegramCallbackDataLimit {
			continue
		}
		row = append(row, map[string]any{"text": telegramActions[action], "callback_data": data})
	}
	if len(row) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": []any{row}}
	}
	return payload
}

// telegramNavActions are buttons that move around a listing rather than act on
// an alert group. They are separate from telegramActions because only the latter
// are rendered onto an alert notification.
var telegramNavActions = map[string]bool{"alerts": true, "duty": true}

// SlackMessagePayload builds a Slack incoming-webhook body, attaching the
// acknowledge and resolve buttons when the notification is about an alert group.
//
// The plain text is kept alongside the blocks: it is what a notification preview
// and a client that cannot render blocks will show, and losing it would mean an
// alert that reads as empty on a phone lock screen.
func SlackMessagePayload(text, groupID string) map[string]any {
	payload := map[string]any{"text": text}
	if groupID == "" {
		return payload
	}
	var elements []any
	for _, action := range []string{"ack", "resolve"} {
		elements = append(elements, map[string]any{
			"type":      "button",
			"text":      map[string]any{"type": "plain_text", "text": telegramActions[action]},
			"action_id": action,
			"value":     groupID,
		})
	}
	payload["blocks"] = []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		map[string]any{"type": "actions", "elements": elements},
	}
	return payload
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
	callback := strings.TrimRight(publicURL, "/") + "/integrations/v1/chatops/mattermost"
	var actions []any
	for _, action := range []string{"ack", "resolve"} {
		actions = append(actions, map[string]any{
			"id":   action,
			"name": telegramActions[action],
			"integration": map[string]any{
				"url": callback,
				// The secret rides in the context because Mattermost does not
				// sign these callbacks: it is the only credential the request
				// carries, and it is compared in constant time on arrival.
				"context": map[string]any{
					"action":   action,
					"group_id": groupID,
					"token":    actionSecret,
				},
			},
		})
	}
	payload["attachments"] = []any{map[string]any{"text": text, "actions": actions}}
	return payload
}

// ChatActionCommand turns an interactive action name and its target into the
// ChatOps command it stands for, so Slack and Mattermost buttons resolve through
// the same table as the Telegram ones.
func ChatActionCommand(action, groupID string) (string, bool) {
	if groupID == "" {
		return "", false
	}
	if _, known := telegramActions[action]; !known {
		return "", false
	}
	return action + " " + groupID, true
}

// ParseTelegramCallbackData turns a tapped button back into a ChatOps command,
// reporting false for anything this service did not put on a keyboard. It lives
// next to the code that builds the buttons so the two cannot drift.
func ParseTelegramCallbackData(data string) (string, bool) {
	action, arg, found := strings.Cut(data, ":")
	if !found || arg == "" {
		return "", false
	}
	_, isGroupAction := telegramActions[action]
	if !isGroupAction && !telegramNavActions[action] {
		return "", false
	}
	return action + " " + arg, true
}

// TelegramReplyPayload renders a command's answer as a Telegram message body,
// attaching a keyboard when the answer is something to act on.
//
// It takes the engine's own result map rather than a rendered string because the
// listing needs the groups themselves, not the sentence describing them.
func TelegramReplyPayload(chatID, text string, result map[string]any) map[string]any {
	payload := telegramMessagePayload(chatID, text, "")
	response, _ := result["response"].(map[string]any)
	if response == nil {
		return payload
	}
	items, _ := response["alerts_page"].([]map[string]any)
	if items == nil {
		return payload
	}
	var rows []any
	for _, item := range items {
		id := utils.StrVal(item, "id")
		data := "ack:" + id
		if len(data) > telegramCallbackDataLimit {
			continue
		}
		rows = append(rows, []any{map[string]any{
			"text":          alertButtonLabel(item),
			"callback_data": data,
		}})
	}
	if nav := telegramPagerRow(utils.IntVal(response, "page"), utils.IntVal(response, "pages")); nav != nil {
		rows = append(rows, nav)
	}
	if len(rows) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	return payload
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

func sendEmail(recipient, text string, cfg SMTPConfig) (status, errMsg, providerResp string) {
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
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Nixys Monitoring Alert\r\n\r\n%s", from, recipient, text))
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
