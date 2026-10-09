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
		if utils.StrVal(item, "webhook_url") == "" && !hasOutboundHeaders(item["headers"]) {
			return item
		}
		if authz.FromContext(ctx).Can(authz.ActionEdit) {
			return item
		}
		out := copyMap(item)
		out["webhook_url"] = mask(utils.StrVal(item, "webhook_url"))
		if hasOutboundHeaders(item["headers"]) {
			out["headers"] = maskOutboundHeaders(item["headers"])
		}
		return out
	case "integrations":
		return redactIntegration(ctx, item)
	case "notifications":
		// A TRIGGER_WEBHOOK notification carries its step's headers and a
		// CREATE_ISSUE one the step's inline tracker token: delivery needs them
		// on the row so retries keep working. The notification is readable by
		// anyone who can see the group — /notifications and /history — and
		// nobody edits a notification, so the values are masked for everyone.
		payload, _ := item["payload"].(map[string]any)
		masked := maskStepSecrets(payload)
		if masked == nil {
			return item
		}
		out := copyMap(item)
		out["payload"] = masked
		return out
	case "escalation_chains":
		return redactChainSecrets(ctx, item)
	}
	return item
}

// redactChainSecrets masks the credentials escalation steps carry — the
// headers of a TRIGGER_WEBHOOK step and the inline token of a CREATE_ISSUE
// step — for actors who may not edit configuration. It is the same line as a
// ChatOps channel's webhook_url, and for the same reason: the chain editor
// round-trips the steps it read, so an editor must get the real values back.
func redactChainSecrets(ctx context.Context, item map[string]any) map[string]any {
	if authz.FromContext(ctx).Can(authz.ActionEdit) {
		return item
	}
	steps, _ := item["steps"].([]any)
	var out map[string]any
	for i, raw := range steps {
		step, _ := raw.(map[string]any)
		masked := maskStepSecrets(step)
		if masked == nil {
			continue
		}
		if out == nil {
			out = copyMap(item)
			out["steps"] = append([]any(nil), steps...)
		}
		out["steps"].([]any)[i] = masked
	}
	if out == nil {
		return item
	}
	return out
}

// maskStepSecrets returns a copy of m — an escalation step, or the payload a
// step put on its notification — with headers values and an inline token
// masked, or nil when it carries neither. token_env names a variable rather
// than holding the secret, so it is left as is.
func maskStepSecrets(m map[string]any) map[string]any {
	hasHeaders := hasOutboundHeaders(m["headers"])
	token := utils.StrVal(m, "token")
	if !hasHeaders && token == "" {
		return nil
	}
	out := copyMap(m)
	if hasHeaders {
		out["headers"] = maskOutboundHeaders(m["headers"])
	}
	if token != "" {
		out["token"] = mask(token)
	}
	return out
}

// redactIntegration hides the two credentials an integration carries.
//
// The HMAC secret is never returned, to anyone: nothing reads it back except
// to check a signature, and a form that needs to change it takes a new value.
// webhook_secret_set says whether one is configured. An env: reference names a
// variable rather than holding the secret, so it is shown as is.
//
// The routing key is the permission to send alerts that page people. Actors
// who may edit configuration see it — they wire senders up; a viewer or a
// responder gets a masked key.
func redactIntegration(ctx context.Context, item map[string]any) map[string]any {
	out := hideIntegrationSecret(item)
	if !authz.FromContext(ctx).Can(authz.ActionEdit) {
		for _, k := range []string{"key", "routing_key"} {
			if v := utils.StrVal(item, k); v != "" {
				out[k] = mask(v)
			}
		}
	}
	return out
}

// redactListForReader applies redactForReader to a page of rows.
func redactListForReader(ctx context.Context, collection string, items []map[string]any) []map[string]any {
	switch collection {
	case "mobile_devices", "chatops_channels", "integrations", "notifications", "escalation_chains":
	default:
		return items
	}
	out := make([]map[string]any, len(items))
	for i, item := range items {
		out[i] = redactForReader(ctx, collection, item)
	}
	return out
}

// hideIntegrationSecret is the part of redactIntegration that applies to every
// response, including those to a create, update or key rotation — which only an
// editor can make, so the routing key stays.
func hideIntegrationSecret(item map[string]any) map[string]any {
	out := copyMap(item)
	secret := utils.StrVal(item, "webhook_secret")
	out["webhook_secret_set"] = secret != ""
	if !utils.IsSecretRef(secret) {
		out["webhook_secret"] = nil
	}
	return out
}
