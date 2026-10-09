package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// chatopsMessageUpdate is how a webhook-backed ChatOps channel edits a message
// it posted: the request to send, and where the id of the posted message is
// found in the platform's answer to the post.
//
// An incoming webhook is a one-way door: it takes a message and says "ok".
// Editing needs an API that returns the message's id and accepts an edit by
// it, and every chat platform and gateway spells both differently. So nothing
// is assumed — the channel says which method, which URL (with {message_id}
// where the id goes) and which JSON path of the post's response holds the id.
// The edit body is the post's body plus message_id, so a gateway that already
// takes the post takes the edit with one field more.
type chatopsMessageUpdate struct {
	Method        string
	URL           string
	MessageIDPath string
}

// chatopsMessageUpdateMethods are the methods an edit may use. GET and DELETE
// are not edits, and anything more exotic is a sign of a misconfiguration.
var chatopsMessageUpdateMethods = map[string]bool{
	http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true,
}

// chatopsMessageUpdateOf reads a channel's message_update, or nil when the
// channel does not edit messages. A Telegram channel is delivered through the
// bot rather than a webhook and never edits through this.
func chatopsMessageUpdateOf(channel map[string]any) *chatopsMessageUpdate {
	if strings.EqualFold(utils.StrVal(channel, "platform"), "telegram") {
		return nil
	}
	m, _ := channel["message_update"].(map[string]any)
	if utils.StrVal(m, "url") == "" {
		return nil
	}
	return &chatopsMessageUpdate{
		Method:        strDefault(strings.ToUpper(utils.StrVal(m, "method")), http.MethodPatch),
		URL:           utils.StrVal(m, "url"),
		MessageIDPath: strDefault(utils.StrVal(m, "message_id_path"), "id"),
	}
}

// sanitizeChatopsMessageUpdate validates message_update on a channel write.
// null and {} remove it, which turns editing off.
func sanitizeChatopsMessageUpdate(raw any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errValidation("message_update must be an object")
	}
	if len(m) == 0 {
		return nil, nil
	}
	method := strings.ToUpper(strDefault(utils.StrVal(m, "method"), http.MethodPatch))
	if !chatopsMessageUpdateMethods[method] {
		return nil, errValidation("message_update.method must be POST, PUT or PATCH")
	}
	target := utils.StrVal(m, "url")
	if target == "" {
		return nil, errValidation("message_update.url is required")
	}
	if err := rejectInlineSecret("message_update.url", target); err != nil {
		return nil, err
	}
	if !utils.IsSecretRef(target) {
		u, err := url.Parse(strings.ReplaceAll(target, "{message_id}", "id"))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errValidation("message_update.url must be an http(s) URL or an env: reference")
		}
	}
	return map[string]any{
		"method":          method,
		"url":             target,
		"message_id_path": strDefault(utils.StrVal(m, "message_id_path"), "id"),
	}, nil
}

// messageIDAt reads the value at a dotted path ("id", "result.message_id",
// "messages.0.id") out of a JSON body. Numbers come back as written: decoding
// them as float64 would turn a 19-digit id into an exponent.
func messageIDAt(body []byte, path string) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var cur any
	if err := dec.Decode(&cur); err != nil {
		return ""
	}
	for _, seg := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return ""
			}
			cur = v[i]
		default:
			return ""
		}
	}
	switch v := cur.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}

// chatopsAlertMessageID finds the platform id of the alert message a status
// message belongs to. pending is true while that message is still on its way:
// its id does not exist yet, but will.
func (e *Engine) chatopsAlertMessageID(ctx context.Context, payload map[string]any) (id string, pending bool, err error) {
	ntfID := utils.StrVal(payload, "chatops_alert_notification_id")
	if ntfID == "" {
		return "", false, nil
	}
	raw, err := e.store.GetItem(ctx, "notifications", ntfID)
	if err != nil || raw == nil {
		return "", false, err
	}
	n := model.WrapNotification(raw)
	if id := n.ProviderMessageID(); id != "" {
		return id, false, nil
	}
	switch n.Status() {
	case model.NotificationDeliveryScheduled, model.NotificationDelivering,
		model.NotificationRetryScheduled, model.NotificationRetrying, model.NotificationBatched:
		return "", true, nil
	}
	return "", false, nil
}

// updateChatopsMessage edits the alert message a status message belongs to.
// done is false when there is nothing to edit — no id kept for the message, or
// the platform refused the edit — and the caller posts the status as a new
// message instead, which is what a channel without message_update gets anyway.
func (e *Engine) updateChatopsMessage(ctx context.Context, ntf, channel, payload map[string]any, update *chatopsMessageUpdate, text string, headers map[string]string) (res deliveryOutcome, done bool) {
	if superseded, err := e.chatopsStatusSuperseded(ctx, payload); err != nil {
		return failed("chatops_update", err.Error(), 0, ""), true
	} else if superseded {
		// An edit replaces the message, so the latest status is the only one
		// worth sending: an acknowledge that failed and is retried after the
		// resolve went through would otherwise turn "resolved" back into
		// "acknowledged". Not a new message either — the newer edit carries
		// the news.
		return skipped(skipSuperseded, "a newer status of this alert replaces this one"), true
	}
	id, pending, err := e.chatopsAlertMessageID(ctx, payload)
	if err != nil {
		return failed("chatops_update", err.Error(), 0, ""), true
	}
	if id == "" {
		// An acknowledgement can land within seconds of the alert, before the
		// alert itself is out. Waiting a retry for it beats posting the status
		// as a separate message and then the alert below it; on the last
		// attempt the status goes out on its own rather than not at all.
		if pending && model.WrapNotification(ntf).RetryCount()+1 < e.deliveryCfg.MaxRetries {
			res = failed("chatops_update", "the alert message is not delivered yet", 0, "")
			// Nothing was sent, so the channel's breaker must not count it: a
			// burst of acknowledgements waiting on one slow alert would open
			// the breaker on a healthy channel, and hold back the very alert
			// they wait for.
			res.NotSent = true
			return res, true
		}
		return deliveryOutcome{}, false
	}
	target := strings.ReplaceAll(utils.ResolveSecretRef(update.URL), "{message_id}", url.PathEscape(id))
	if target == "" {
		return skipped(skipNotConfigured,
			"chatops channel "+utils.StrVal(channel, "name")+": message_update.url resolves to nothing"), true
	}
	// Checked here for the same reason as the webhook URL: the dispatch
	// pre-flight judged the notification's target, which is the channel id.
	if ok, detail := e.deliveryCfg.Channels.DestinationAllowed(target); !ok {
		return scrubEditURL(skipped(skipDestinationNotAllowed, detail), target), true
	}
	platform := utils.StrVal(channel, "platform")
	proxyChannel := e.deliveryCfg.chatopsProxyChannel(platform)
	res, _ = sendJSONGuarded(ctx, e.deliveryCfg.clientFor(proxyChannel), update.Method, target,
		map[string]any{"text": text, "message_id": id}, e.deliveryCfg.ssrfGuardFor(proxyChannel), headers, 0)
	res.ProviderStatus = platform + "_chatops_update"
	res = scrubEditURL(res, target)
	// A refusal is an answer, not an outage: the message was deleted, is too
	// old to edit, or the bot may not touch it. Retrying asks the same
	// question again; posting the news as a new message still tells the room.
	if res.Status == deliveryFailed && res.Code >= 400 && res.Code < 500 && res.Code != http.StatusTooManyRequests {
		slog.Info("chatops_update_refused",
			"channel_id", utils.StrVal(channel, "id"), "code", res.Code,
			"effect", "the status is posted as a new message")
		return deliveryOutcome{}, false
	}
	return res, true
}

// skipSuperseded is the reason on a status edit that a newer status of the
// same alert made pointless. Not a missing transport: nobody went untold.
const skipSuperseded = "superseded"

// chatopsStatusSuperseded reports whether the group has had a newer status
// change than the one this message carries. The group's counter is written
// when a status is queued, so an older edit still waiting for a retry sees the
// newer one even before that one is delivered.
func (e *Engine) chatopsStatusSuperseded(ctx context.Context, payload map[string]any) (bool, error) {
	seq := utils.IntVal(payload, "chatops_status_seq")
	if seq == 0 {
		return false, nil
	}
	raw, err := e.store.GetItem(ctx, "alert_groups", utils.StrVal(payload, "alert_group_id"))
	if err != nil || raw == nil {
		return false, err
	}
	return model.WrapAlertGroup(raw).ChatopsStatusSeq() > seq, nil
}

// scrubEditURL takes the edit URL out of what is stored about the attempt.
//
// The URL is an API address that often carries its credential in the path or
// the query, and a transport error quotes it whole (Put "https://…": dial tcp
// …). The channel masks the URL on its read paths; the notification's
// last_error and the attempt row are read by anyone who can see the group, so
// they keep the scheme and host — enough to tell which endpoint failed — and
// nothing after them.
func scrubEditURL(res deliveryOutcome, target string) deliveryOutcome {
	shown := "the message_update URL"
	forms := []string{target}
	if u, err := url.Parse(target); err == nil {
		if u.Host != "" {
			shown = u.Scheme + "://" + u.Host + "/…"
		}
		forms = append(forms, u.String(), u.Redacted())
	}
	for _, f := range forms {
		if f == "" {
			continue
		}
		res.Err = strings.ReplaceAll(res.Err, f, shown)
		res.Response = strings.ReplaceAll(res.Response, f, shown)
	}
	return res
}
