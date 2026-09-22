package engine

import (
	"fmt"
	"net/http"
	"sort"

	"golang.org/x/net/http/httpguts"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Custom request headers on outbound webhooks: a ChatOps channel's and a
// TRIGGER_WEBHOOK step's.
//
// They exist so a credential for the receiving endpoint can travel in a header
// (Authorization, X-API-Key) instead of the URL, where every proxy and access
// log along the way keeps a copy. Every value is therefore treated as a secret:
// it may be an "env:VAR" reference, resolved at send time, and the production
// profile refuses an inline one — the same rule as a channel's webhook_url.

// maxOutboundHeaders bounds how many a single destination carries. Nothing
// real needs more than a few; the bound keeps a mistake from becoming a
// request the receiver refuses for its size.
const maxOutboundHeaders = 16

// reservedOutboundHeaders are the ones the transport owns. Letting
// configuration set them would either be ignored by net/http or produce a
// request whose framing disagrees with its body.
var reservedOutboundHeaders = map[string]bool{
	"Host":              true,
	"Content-Length":    true,
	"Content-Type":      true,
	"Transfer-Encoding": true,
	"Connection":        true,
}

// sanitizeOutboundHeaders validates a "headers" object from a create or update
// request. nil (field absent or null) yields nil; an empty object clears them.
func sanitizeOutboundHeaders(raw any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errValidation("headers must be an object of header name to value")
	}
	if len(m) > maxOutboundHeaders {
		return nil, errValidation(fmt.Sprintf("headers: at most %d are allowed", maxOutboundHeaders))
	}
	out := make(map[string]any, len(m))
	seen := map[string]string{}
	for name, v := range m {
		value, ok := v.(string)
		if !ok {
			return nil, errValidation(fmt.Sprintf("headers.%s must be a string", name))
		}
		if !httpguts.ValidHeaderFieldName(name) {
			return nil, errValidation(fmt.Sprintf("headers: %q is not a valid header name", name))
		}
		canonical := http.CanonicalHeaderKey(name)
		if reservedOutboundHeaders[canonical] {
			return nil, errValidation(fmt.Sprintf("headers: %s is set by the transport and cannot be configured", canonical))
		}
		// Two spellings of one name would leave which one is sent to map order.
		if prev, dup := seen[canonical]; dup {
			return nil, errValidation(fmt.Sprintf("headers: %q and %q are the same header", prev, name))
		}
		seen[canonical] = name
		if value == "" {
			return nil, errValidation(fmt.Sprintf("headers.%s must not be empty", name))
		}
		if !utils.IsSecretRef(value) && !httpguts.ValidHeaderFieldValue(value) {
			return nil, errValidation(fmt.Sprintf("headers.%s contains characters not allowed in a header value", name))
		}
		if err := rejectInlineSecret("headers."+name, value); err != nil {
			return nil, err
		}
		out[canonical] = value
	}
	return out, nil
}

// resolveOutboundHeaders turns stored headers into the ones to send, resolving
// env: references. A reference to an unset variable is reported rather than
// sent empty: the receiver would answer 401, and "the credential is missing
// here" is the diagnosis the timeline should show instead.
func resolveOutboundHeaders(raw any) (map[string]string, error) {
	m, _ := raw.(map[string]any)
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for name, v := range m {
		stored, _ := v.(string)
		value := utils.ResolveSecretRef(stored)
		// The messages name the reference, never the value: they end up on
		// the notification, which is readable far more widely than the config.
		if value == "" {
			return nil, fmt.Errorf("header %s references %s, which is not set", name, stored)
		}
		if !httpguts.ValidHeaderFieldValue(value) {
			return nil, fmt.Errorf("header %s: the value of %s is not a valid header value", name, stored)
		}
		out[name] = value
	}
	return out, nil
}

// maskOutboundHeaders returns a copy with every value masked, keeping the
// names: "which headers are sent" is diagnostic, their values are credentials.
func maskOutboundHeaders(raw any) map[string]any {
	m, _ := raw.(map[string]any)
	out := make(map[string]any, len(m))
	for name, v := range m {
		s, _ := v.(string)
		out[name] = mask(s)
	}
	return out
}

// outboundHeaderNames lists the configured names, sorted, for the group log.
func outboundHeaderNames(raw any) []string {
	m, _ := raw.(map[string]any)
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// hasOutboundHeaders reports whether raw carries at least one header.
func hasOutboundHeaders(raw any) bool {
	m, _ := raw.(map[string]any)
	return len(m) > 0
}
