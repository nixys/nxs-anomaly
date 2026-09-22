package engine

import (
	"log/slog"
	"net"
	"os"
	"strings"
)

// Exceptions to the SSRF guard (NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS_EXCEPT).
//
// The guard refuses every private address, and that is right for a URL somebody
// typed into a form — but a receiver inside the operator's own network (a chat
// gateway in the same cluster, an internal ticket system) is private by
// definition. Before this the only way to reach one was to switch the guard off
// for everything, which the production profile rightly refuses. An exception
// names that one destination instead.
//
// Entries are comma-separated, in the syntax of NXS_ANOMALY_EGRESS_ALLOWLIST:
//
//   - a host name, matched against the name as written in the URL (or the
//     redirect) — "gw.chat.svc.cluster.local";
//   - a domain with a leading dot, matching it and every subdomain —
//     ".internal.example";
//   - a CIDR or an IP address, matched against the address actually dialled —
//     "10.20.0.0/16", "10.96.14.7".
//
// An exception lifts only the *private* ranges (RFC 1918 and fc00::/7).
// Loopback, link-local — the cloud metadata endpoint is 169.254.169.254 — and
// the unspecified address stay refused whatever the list says: nothing an
// operator wants to page through lives there, and they are exactly what an SSRF
// is after. Everything else about the guard is unchanged: the check is made on
// the address being dialled, on every redirect hop.
const ssrfExemptionsEnv = "NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS_EXCEPT"

type ssrfExemptions struct {
	raw   string
	nets  []*net.IPNet
	hosts []string // lower-case; a leading "." is a domain suffix
}

// parseSSRFExemptions returns nil for an empty list, so "no exceptions" costs
// nothing on the hot path.
func parseSSRFExemptions(raw string) *ssrfExemptions {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	x := &ssrfExemptions{raw: raw}
	for _, entry := range splitList(raw) {
		if _, ipnet, err := net.ParseCIDR(entry); err == nil {
			x.nets = append(x.nets, ipnet)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			x.nets = append(x.nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		if strings.Contains(entry, "/") {
			// Not a CIDR, and no host name contains a slash: as a name it would
			// silently match nothing, which is the opposite of what was meant.
			slog.Warn("invalid_setting", "key", ssrfExemptionsEnv, "value", entry,
				"reason", "neither a CIDR nor a host name; ignored")
			continue
		}
		x.hosts = append(x.hosts, strings.TrimSuffix(strings.ToLower(entry), "."))
	}
	return x
}

func ssrfExemptionsFromEnv() *ssrfExemptions {
	return parseSSRFExemptions(os.Getenv(ssrfExemptionsEnv))
}

// allows reports whether ip, reached under the name host, is excepted from the
// guard. host is the name as written (or an IP literal); ip is what it resolved
// to.
func (x *ssrfExemptions) allows(host string, ip net.IP) bool {
	if x == nil || !ip.IsPrivate() {
		return false
	}
	for _, n := range x.nets {
		if n.Contains(ip) {
			return true
		}
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, h := range x.hosts {
		if strings.HasPrefix(h, ".") {
			if host == h[1:] || strings.HasSuffix(host, h) {
				return true
			}
			continue
		}
		if host == h {
			return true
		}
	}
	return false
}

// String is the list as configured, for messages.
func (x *ssrfExemptions) String() string {
	if x == nil {
		return ""
	}
	return x.raw
}

// blockPolicy decides whether a connection to ip, reached under the name host,
// is refused. nil means no guard.
type blockPolicy func(host string, ip net.IP) bool

// ipPolicy adapts a policy that looks only at the address.
func ipPolicy(blocked func(net.IP) bool) blockPolicy {
	if blocked == nil {
		return nil
	}
	return func(_ string, ip net.IP) bool { return blocked(ip) }
}

// policy is the guard with these exceptions applied.
func (x *ssrfExemptions) policy() blockPolicy {
	return func(host string, ip net.IP) bool {
		return isBlockedIP(ip) && !x.allows(host, ip)
	}
}
