package engine

import (
	"context"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Read-path redaction for credential-shaped fields.
//
// Two values became real credentials when their channels gained real
// transports: a device's push token, which the relay uses to reach a phone, and
// a ChatOps channel's incoming-webhook URL, which is itself the permission to
// post into that channel. Both were returned verbatim by the ordinary list and
// get endpoints, to anyone who could read at all.
//
// Redaction happens here — on the engine's read path used by the API — and not
// in the store, because delivery reads the same rows through the store and
// needs the real values. That split is deliberate: the delivery path is code,
// the read path is a response to a person.

// mask keeps a short suffix so an operator can still tell two tokens apart
// while never seeing enough to use one.
func mask(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return "***"
	}
	return "***" + v[len(v)-4:]
}

// redactForReader returns a copy of item with sensitive fields masked for this
// actor, or the item unchanged when it has none.
//
// The webhook URL stays visible to actors who may edit configuration: they are
// the ones who set it, and a settings form that shows "***" cannot be edited
// without silently wiping the value. A viewer or responder has no such need.
func redactForReader(ctx context.Context, collection string, item map[string]any) map[string]any {
	if item == nil {
		return nil
	}
	switch collection {
	case "mobile_devices":
		if utils.StrVal(item, "push_token") == "" {
			return item
		}
		out := copyMap(item)
		out["push_token"] = mask(utils.StrVal(item, "push_token"))
		return out
	case "chatops_channels":
		if utils.StrVal(item, "webhook_url") == "" {
			return item
		}
		if authz.FromContext(ctx).Can(authz.ActionEdit) {
			return item
		}
		out := copyMap(item)
		out["webhook_url"] = mask(utils.StrVal(item, "webhook_url"))
		return out
	}
	return item
}

// redactListForReader applies redactForReader to a page of rows.
func redactListForReader(ctx context.Context, collection string, items []map[string]any) []map[string]any {
	switch collection {
	case "mobile_devices", "chatops_channels":
	default:
		return items
	}
	out := make([]map[string]any, len(items))
	for i, item := range items {
		out[i] = redactForReader(ctx, collection, item)
	}
	return out
}
