package engine

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// data_policy.go holds the two installation-wide decisions a personal-data
// deployment has to make and that nothing else in the engine can infer:
//
//   - how long each category of stored data is kept (RetentionPolicy), and
//   - which outbound channels may be used at all and where they may send
//     (ChannelPolicy).
//
// Both are read once at startup, like the rest of DeliveryConfig. They are
// deliberately not per-team or per-integration settings: a rule that some
// objects can opt out of is not a policy, and an operator answering a regulator
// needs one answer per installation, not a query.
//
// See docs/DATA_INVENTORY.md for what is stored where, and which knob bounds it.

// RetentionPolicy bounds how long each category of data lives.
//
// Zero means "keep forever" for every field, which is the compatible default and
// is also the honest one: silently deleting a customer's incident history on
// upgrade would be worse than growing a table. The chart presets set explicit
// values; docs/DATA_INVENTORY.md explains what each category contains.
type RetentionPolicy struct {
	// AlertGroupDays bounds resolved alert groups and their alerts. Alert
	// payloads carry whatever the monitoring system put in them.
	AlertGroupDays int
	// AuditDays bounds the audit trail: actor names, IPs, and what they did.
	AuditDays int
	// ChatopsMessageDays bounds the mirror of ChatOps traffic.
	ChatopsMessageDays int
	// NotificationDays bounds terminal notifications: who was paged, on which
	// channel, at which address.
	NotificationDays int
	// DeliveryAttemptDays bounds per-attempt provider records: response
	// excerpts, error text, timings. Shorter than NotificationDays by default in
	// the presets — the attempt detail ages out of usefulness fastest and is the
	// most verbose.
	DeliveryAttemptDays int
	// WebSessionDays bounds browser sessions, which carry the client IP and user
	// agent. Independent of session expiry: an expired session stops
	// authenticating immediately, this is when the row goes.
	WebSessionDays int
}

// Retention categories, as reported by the metric and the worker log. Stable
// identifiers: an operator's dashboard and their retention policy document both
// refer to them.
const (
	RetentionAlertGroups      = "alert_groups"
	RetentionAudit            = "audit_events"
	RetentionChatopsMessages  = "chatops_messages"
	RetentionNotifications    = "notifications"
	RetentionDeliveryAttempts = "delivery_attempts"
	RetentionWebSessions      = "web_sessions"
)

func retentionPolicyFromEnv() RetentionPolicy {
	days := func(name string, def int) int {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				return n
			}
		}
		return def
	}
	return RetentionPolicy{
		// The two pre-existing knobs keep their historical defaults so an
		// upgrade changes nothing about what is deleted.
		AlertGroupDays:     days("NXS_ANOMALY_ALERT_GROUP_TTL_DAYS", 30),
		ChatopsMessageDays: days("NXS_ANOMALY_CHATOPS_MESSAGES_TTL_DAYS", 30),
		AuditDays:          days("NXS_ANOMALY_AUDIT_RETENTION_DAYS", 0),
		// The three new ones default to "keep", for the same reason: an upgrade
		// must not start deleting data nobody asked it to delete.
		NotificationDays:    days("NXS_ANOMALY_NOTIFICATION_RETENTION_DAYS", 0),
		DeliveryAttemptDays: days("NXS_ANOMALY_DELIVERY_ATTEMPT_RETENTION_DAYS", 0),
		WebSessionDays:      days("NXS_ANOMALY_WEB_SESSION_RETENTION_DAYS", 0),
	}
}

// Unset reports whether nothing at all was chosen: no category has a horizon.
// The readiness report uses this to say so out loud rather than to enforce a
// number, because the right horizon is the client's to pick, not ours.
func (p RetentionPolicy) Unset() bool {
	return p.AuditDays == 0 && p.NotificationDays == 0 &&
		p.DeliveryAttemptDays == 0 && p.WebSessionDays == 0
}

// ChannelPolicy is the installation-wide decision about outbound delivery: which
// channels exist at all here, and which destinations they may reach.
//
// It sits above every per-user and per-chain setting. A channel blocked here
// cannot be configured, cannot be delivered to, and shows up in the readiness
// report as long as anything still points at it — because the failure mode worth
// preventing is not "a message was sent", it is "an operator believed a person
// was paged on a channel this installation refuses to use".
type ChannelPolicy struct {
	// Blocked holds channel codes ("telegram", "slack", "webhook", …) refused
	// installation-wide. Empty means everything the build supports is allowed.
	Blocked map[string]bool
	// EgressAllowlist restricts where operator-supplied destination URLs may
	// point. Empty means no allowlist — the compatible default.
	EgressAllowlist []egressRule
	// egressRaw is the configured list, verbatim, for error messages and the
	// readiness report. An operator debugging a refusal needs to see what the
	// rule set actually was.
	egressRaw string
	// PseudonymiseAnalytics drops user, team and actor identifiers from the
	// analytics event stream before it leaves for Kafka/ClickHouse. Personal
	// data (names, addresses, phone numbers) never enters those events at all;
	// this covers the pseudonymous identifiers on top.
	PseudonymiseAnalytics bool
}

// egressRule is one entry of the allowlist: either a hostname (optionally with a
// leading dot, which also matches subdomains) or a CIDR block.
type egressRule struct {
	host string
	net  *net.IPNet
}

// Skip reasons produced by the channel policy. Terminal, like every skip: no
// retry can make a refused channel allowed.
const (
	skipChannelBlocked        = "channel_blocked_by_policy"
	skipDestinationNotAllowed = "destination_not_allowlisted"
)

// channelPolicyFromEnv reads the policy at startup.
func channelPolicyFromEnv() ChannelPolicy {
	return buildChannelPolicy(
		os.Getenv("NXS_ANOMALY_BLOCKED_CHANNELS"),
		os.Getenv("NXS_ANOMALY_EGRESS_ALLOWLIST"),
		os.Getenv("NXS_ANOMALY_ANALYTICS_PSEUDONYMISE") == "true",
	)
}

// buildChannelPolicy parses the two comma-separated lists. Separate from the
// environment read so tests exercise the same parser rather than a second one
// that could disagree with it.
func buildChannelPolicy(blocked, allowlist string, pseudonymise bool) ChannelPolicy {
	p := ChannelPolicy{Blocked: map[string]bool{}, PseudonymiseAnalytics: pseudonymise}
	for _, c := range splitList(blocked) {
		p.Blocked[strings.ToLower(c)] = true
	}
	p.egressRaw = strings.TrimSpace(allowlist)
	for _, entry := range splitList(p.egressRaw) {
		if _, ipnet, err := net.ParseCIDR(entry); err == nil {
			p.EgressAllowlist = append(p.EgressAllowlist, egressRule{net: ipnet})
			continue
		}
		p.EgressAllowlist = append(p.EgressAllowlist, egressRule{host: strings.ToLower(entry)})
	}
	return p
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ChannelBlocked reports whether this channel is refused installation-wide.
func (p ChannelPolicy) ChannelBlocked(channel string) bool {
	return p.Blocked[strings.ToLower(strings.TrimSpace(channel))]
}

// BlockedList returns the blocked channels, for messages and the readiness
// report.
func (p ChannelPolicy) BlockedList() []string {
	out := make([]string, 0, len(p.Blocked))
	for c := range p.Blocked {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// EgressAllowlistRaw returns the configured allowlist verbatim ("" when none).
func (p ChannelPolicy) EgressAllowlistRaw() string { return p.egressRaw }

// DestinationAllowed reports whether an operator-supplied URL may be contacted,
// and if not, why.
//
// What it checks is the destination as *written*, not as resolved: the
// allowlist is about "may this installation talk to that party at all", which is
// a question about the name in the configuration. The SSRF guard
// (BlockPrivateWebhooks) is the one that follows DNS and judges the address the
// name resolves to, and the two are complementary — an allowlisted hostname
// resolving into the cluster's own network is still refused by the guard.
//
// Fixed-host transports (Telegram's api.telegram.org, SMTP, Asterisk) are not
// destinations an operator types into a target field, so they are governed by
// ChannelBlocked instead. Blocking Telegram is a channel decision, not a
// hostname one.
func (p ChannelPolicy) DestinationAllowed(rawURL string) (bool, string) {
	if len(p.EgressAllowlist) == 0 {
		return true, ""
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return false, fmt.Sprintf("destination %q is not a URL this policy can judge; the egress allowlist refuses what it cannot parse", rawURL)
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		for _, r := range p.EgressAllowlist {
			if r.net != nil && r.net.Contains(ip) {
				return true, ""
			}
			if r.host != "" && r.host == host {
				return true, ""
			}
		}
		return false, p.refusal(host)
	}
	for _, r := range p.EgressAllowlist {
		switch {
		case r.host == "":
			// A CIDR rule cannot authorise a name: whether the name resolves
			// into that block is a runtime fact that can change between the
			// check and the request.
			continue
		case strings.HasPrefix(r.host, "."):
			if host == strings.TrimPrefix(r.host, ".") || strings.HasSuffix(host, r.host) {
				return true, ""
			}
		case host == r.host:
			return true, ""
		}
	}
	return false, p.refusal(host)
}

func (p ChannelPolicy) refusal(host string) string {
	return fmt.Sprintf("destination host %q is outside NXS_ANOMALY_EGRESS_ALLOWLIST (%s)", host, p.egressRaw)
}

// channelsWithOperatorSuppliedURL are the channels whose target is a URL somebody
// typed in, and so the ones the egress allowlist governs.
var channelsWithOperatorSuppliedURL = map[string]bool{
	"webhook": true, "slack": true, "mattermost": true,
}

// checkOutboundPolicy returns a terminal skip outcome when the policy refuses
// this channel/target pair, and ok=false when it does not.
func (p ChannelPolicy) checkOutboundPolicy(channel, target string) (deliveryOutcome, bool) {
	if p.ChannelBlocked(channel) {
		return skipped(skipChannelBlocked, fmt.Sprintf(
			"channel %q is refused by this installation (NXS_ANOMALY_BLOCKED_CHANNELS=%s)",
			channel, strings.Join(p.BlockedList(), ","))), true
	}
	if channelsWithOperatorSuppliedURL[channel] {
		if ok, detail := p.DestinationAllowed(target); !ok {
			return skipped(skipDestinationNotAllowed, detail), true
		}
	}
	return deliveryOutcome{}, false
}

// validateTarget refuses a notification target the policy does not permit, at
// configuration time.
//
// The two refusals say different things, and the message matters more than
// usual here: an operator hitting this is being told their installation has a
// rule, not that they made a typo. Both name the environment variable that
// carries the decision, because the fix is an operator's, not a user's.
func (p ChannelPolicy) validateTarget(channel, target string) error {
	if p.ChannelBlocked(channel) {
		return errValidation(fmt.Sprintf(
			"channel %q is refused by this installation's outbound policy (NXS_ANOMALY_BLOCKED_CHANNELS=%s)",
			channel, strings.Join(p.BlockedList(), ",")))
	}
	// An empty target is a channel whose address comes from elsewhere (the log
	// channel, or a policy step that reuses the user's configured address).
	// There is nothing to judge, and refusing it would block the default target.
	if strings.TrimSpace(target) == "" || !channelsWithOperatorSuppliedURL[channel] {
		return nil
	}
	if ok, detail := p.DestinationAllowed(target); !ok {
		return errValidation(detail)
	}
	return nil
}
