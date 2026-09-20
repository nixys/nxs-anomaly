package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/tracing"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func (e *Engine) deliverNotificationViaAdapter(ctx context.Context, ntf map[string]any) (outcome deliveryOutcome) {
	n := model.WrapNotification(ntf)
	channel := n.Channel()
	target := n.Target()
	payload := n.Payload()
	if payload == nil {
		payload = map[string]any{}
	}

	// The span the whole feature is for. A provider call is the one step in the
	// pipeline whose duration is outside this service's control, so "the alert
	// took four minutes" is usually answered here — and the per-channel latency
	// histogram cannot say which notification was the slow one.
	// Linked, not parented, to the ingest that caused this. The two are joined by
	// a database row rather than a call — the webhook returned in milliseconds
	// and this runs minutes later in another process — so making the delivery a
	// child would nest a four-minute span inside a request that already finished
	// and pull it out of the worker's own trace. A link keeps both views: the
	// delivery belongs to this worker cycle, and "what caused it" is one hop away.
	ctx, span := tracing.StartLinked(ctx, "delivery."+channel,
		utils.StrVal(payload, "trace_parent"),
		tracing.NotificationID(n.ID()),
		tracing.Channel(channel),
		tracing.AlertGroupID(utils.StrVal(payload, "alert_group_id")),
		attribute.Int("nxs.retry_count", n.RetryCount()),
	)
	defer func() {
		span.SetAttributes(
			tracing.Outcome(outcome.Status),
			attribute.String("nxs.provider_status", outcome.ProviderStatus),
		)
		if outcome.Status == deliveryFailed {
			// Not RecordError: the failure is a string on the outcome, and a failed
			// delivery is an expected state of the world rather than a bug.
			span.SetStatus(codes.Error, outcome.Err)
		}
		span.End()
	}()

	slog.Debug("delivery_attempt",
		"notification_id", n.ID(),
		"channel", channel,
		"retry_count", n.RetryCount(),
		"trace_id", tracing.TraceID(ctx),
	)

	// The outbound-channel policy is checked here rather than only at
	// configuration time, and that redundancy is the point: objects configured
	// before the policy was tightened, and objects created through a path that
	// predates it, still must not reach a refused channel or an unlisted
	// destination. Terminal skip, not failure — no retry can make a refused
	// channel allowed, and the reason is recorded on the notification so the
	// timeline says "policy", not "nothing happened".
	if outcome, refused := e.deliveryCfg.Channels.checkOutboundPolicy(channel, target); refused {
		slog.Warn("delivery_refused_by_policy",
			"notification_id", n.ID(), "channel", channel,
			"reason", outcome.ProviderStatus, "detail", outcome.Err)
		return outcome
	}

	switch channel {
	case "webhook":
		return postWebhookGuarded(ctx, e.deliveryCfg.clientFor("webhook"), target,
			toAlertmanagerPayload(payload), e.deliveryCfg.ssrfGuardFor("webhook"), nil)

	case "slack", "mattermost":
		text := renderNotificationText(ntf, payload, "")
		groupID := utils.StrVal(payload, "alert_group_id")
		body := SlackMessagePayload(text, groupID, e.deliveryCfg.PublicURL)
		if channel == "mattermost" {
			body = MattermostMessagePayload(text, groupID,
				e.deliveryCfg.PublicURL, e.deliveryCfg.MattermostActionSecret)
		}
		res := postWebhookGuarded(ctx, e.deliveryCfg.clientFor(channel), target,
			body, e.deliveryCfg.ssrfGuardFor(channel), nil)
		res.ProviderStatus = channel + "_webhook"
		return res

	case "telegram":
		text := renderNotificationText(ntf, payload, e.getNotificationTemplate(ctx, utils.StrVal(payload, "integration_id"), "telegram"))
		return e.sendTelegramOutcome(ctx, target, text,
			utils.StrVal(payload, "alert_group_id"), telegramShiftOptions{
				offerCheckin: utils.BoolVal(payload, "offer_checkin", false),
				scheduleID:   utils.StrVal(payload, "schedule_id"),
			})

	case "email":
		text := renderNotificationText(ntf, payload, e.getNotificationTemplate(ctx, utils.StrVal(payload, "integration_id"), "email"))
		status, errMsg, providerResp := sendEmail(target, emailSubject(payload), text, e.deliveryCfg.SMTP)
		if status == "delivered" {
			return delivered(strDefault(providerResp, "smtp"), 0, "")
		}
		// No SMTP host configured is a gap, not a broken transport.
		if e.deliveryCfg.SMTP.Host == "" {
			return skipped(skipNotConfigured, "NXS_ANOMALY_SMTP_HOST is not set")
		}
		return failed("smtp", errMsg, 0, "")

	case "call":
		msg := callMessage(payload)
		status, errMsg, providerResp := sendAsteriskCall(target, msg, e.deliveryCfg.AsteriskInstances)
		if status == "delivered" {
			return delivered(strDefault(providerResp, "asterisk"), 0, providerResp)
		}
		if len(e.deliveryCfg.AsteriskInstances) == 0 {
			return skipped(skipNotConfigured, "no Asterisk instance is configured")
		}
		return failed("asterisk", errMsg, 0, providerResp)

	case "issue":
		status, errMsg, providerResp := e.deliverIssue(ctx, payload)
		if status == "delivered" {
			return delivered(strDefault(providerResp, "issue"), 0, providerResp)
		}
		if status == deliverySkipped {
			return skipped(skipBlockedDestination, errMsg)
		}
		return failed("issue", errMsg, 0, providerResp)

	case "mobile":
		return e.deliverMobilePush(ctx, target, payload)

	case "chatops":
		return e.deliverChatops(ctx, ntf, target, payload)

	case "log":
		// The log channel's delivery contract is the log line. It used to
		// report delivered without writing anything, which made "notified via
		// log" unverifiable.
		slog.Info("notification_log_delivery",
			"notification_id", n.ID(),
			"user_id", n.UserID(),
			"alert_group_id", n.AlertGroupID(),
			"reason", n.Reason(),
			"text", renderNotificationText(ntf, payload, ""))
		return delivered("log", 0, "")

	default:
		return failed("", fmt.Sprintf("unsupported channel: %s", channel), 0, "")
	}
}

// sendTelegramOutcome wraps the Telegram adapter in the outcome contract.
func (e *Engine) sendTelegramOutcome(ctx context.Context, chatID, text, groupID string, shift telegramShiftOptions) deliveryOutcome {
	status, errMsg, providerResp := sendTelegram(ctx, e.deliveryCfg.clientFor("telegram"), chatID, text,
		e.deliveryCfg.TelegramToken, groupID, shift, e.deliveryCfg.PublicURL)
	if status == "delivered" {
		return delivered(strDefault(providerResp, "telegram_sendMessage"), 200, "")
	}
	// A missing bot token is a configuration gap, not a transport failure:
	// retrying it four times and dead-lettering says the wrong thing.
	if e.deliveryCfg.TelegramToken == "" {
		return skipped(skipNotConfigured, "NXS_ANOMALY_TELEGRAM_BOT_TOKEN is not set")
	}
	return failed("telegram_sendMessage", errMsg, 0, "")
}

// AnswerTelegramCallback closes the loading state on a tapped inline button and
// shows the person what the tap did.
//
// Telegram spins the button until this is called, so an unanswered callback
// reads as "nothing happened" even though the acknowledge already went through.
// It runs after the command, never instead of it: the returned error is worth
// logging, but it cannot undo what the tap already changed.
func (e *Engine) AnswerTelegramCallback(ctx context.Context, callbackID, text string) error {
	if e.deliveryCfg.TelegramToken == "" {
		return fmt.Errorf("NXS_ANOMALY_TELEGRAM_BOT_TOKEN is not set")
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", e.deliveryCfg.TelegramToken)
	payload := map[string]any{
		"callback_query_id": callbackID,
		// Telegram caps the toast at 200 characters and rejects longer ones.
		"text": utils.TruncateRunes(text, 200),
	}
	// Telegram API is a fixed public host — the private-IP guard does not apply.
	if status, err := postWebhook(ctx, e.deliveryCfg.clientFor("telegram"), url, payload, false); status != "delivered" {
		return fmt.Errorf("answerCallbackQuery: %s", err)
	}
	return nil
}

// deliverMobilePush hands a notification to the push relay, when one is
// configured.
//
// nxs-anomaly has no FCM/APNs client — a real mobile app is out of the beta
// scope — so the supported transport is a relay endpoint the operator runs:
// NXS_ANOMALY_MOBILE_PUSH_URL receives the device, its push token and the
// rendered notification, and its 2xx is the delivery ACK. Without that URL
// there is no transport at all, and the notification is skipped rather than
// reported as delivered.
func (e *Engine) deliverMobilePush(ctx context.Context, deviceID string, payload map[string]any) deliveryOutcome {
	if e.deliveryCfg.MobilePushURL == "" {
		return skipped(skipNotConfigured,
			"mobile push relay is not configured (NXS_ANOMALY_MOBILE_PUSH_URL)")
	}
	device, err := e.store.GetItem(ctx, "mobile_devices", deviceID)
	if err != nil {
		return failed("mobile_push", err.Error(), 0, "")
	}
	if device == nil {
		return failed("mobile_push", "mobile device "+deviceID+" not found", 0, "")
	}
	token := utils.ResolveSecretRef(utils.StrVal(device, "push_token"))
	if token == "" {
		return skipped(skipNotConfigured, "device has no push token")
	}
	body := map[string]any{
		"device_id":  deviceID,
		"platform":   utils.StrVal(device, "platform"),
		"push_token": token,
		"user_id":    utils.StrVal(device, "user_id"),
		"title":      strDefault(utils.StrVal(payload, "title"), "Alert notification"),
		"severity":   utils.StrVal(payload, "severity"),
		"reason":     utils.StrVal(payload, "reason"),
		"group_id":   utils.StrVal(payload, "alert_group_id"),
	}
	headers := map[string]string{}
	if tok := e.deliveryCfg.MobilePushToken; tok != "" {
		headers["Authorization"] = "Bearer " + tok
	}
	res := postWebhookGuarded(ctx, e.deliveryCfg.clientFor("mobile"), e.deliveryCfg.MobilePushURL,
		body, e.deliveryCfg.ssrfGuardFor("mobile"), headers)
	res.ProviderStatus = "mobile_push"
	return res
}

// deliverChatops posts to a ChatOps channel's incoming webhook.
//
// A channel with no webhook URL exists only inside this service: the command
// API can answer it, but nothing pushes to it. That is a configuration gap,
// reported as such, not a delivery.
func (e *Engine) deliverChatops(ctx context.Context, ntf map[string]any, channelID string, payload map[string]any) deliveryOutcome {
	channel, err := e.store.GetItem(ctx, "chatops_channels", channelID)
	if err != nil {
		return failed("chatops", err.Error(), 0, "")
	}
	if channel == nil {
		return failed("chatops", "chatops channel "+channelID+" not found", 0, "")
	}
	webhookURL := utils.ResolveSecretRef(utils.StrVal(channel, "webhook_url"))
	if webhookURL == "" {
		return skipped(skipNotConfigured,
			"chatops channel "+utils.StrVal(channel, "name")+" has no webhook_url")
	}
	// The channel's own URL, not the notification's target, so the egress
	// allowlist has to be applied here as well as in the dispatch pre-flight.
	if ok, detail := e.deliveryCfg.Channels.DestinationAllowed(webhookURL); !ok {
		return skipped(skipDestinationNotAllowed, detail)
	}
	text := renderNotificationText(ntf, payload, "")
	proxyChannel := e.deliveryCfg.chatopsProxyChannel(utils.StrVal(channel, "platform"))
	res := postWebhookGuarded(ctx, e.deliveryCfg.clientFor(proxyChannel), webhookURL,
		map[string]any{"text": text}, e.deliveryCfg.ssrfGuardFor(proxyChannel), nil)
	res.ProviderStatus = utils.StrVal(channel, "platform") + "_chatops"
	return res
}

// templateCacheEntry is a cached template plus the time it was read. The
// timestamp is what makes the cache safe across replicas: invalidation below
// only reaches the process that handled the write, so an API pod serving the
// integration update leaves every worker holding the old text. With no expiry
// that staleness was permanent and restarting the worker was the only cure.
type templateCacheEntry struct {
	value    string
	loadedAt time.Time
}

// templateCacheTTL bounds how long a template written by ANOTHER replica stays
// invisible here. It follows the reference cache, which startup aligns to the
// worker poll interval, so both caches go stale on the same clock instead of
// two independently-tuned ones.
func (e *Engine) templateCacheTTL() time.Duration {
	if e.refCache != nil {
		return e.refCache.getTTL()
	}
	return refCacheTTL
}

func (e *Engine) getNotificationTemplate(ctx context.Context, integrationID, channel string) string {
	if integrationID == "" {
		return ""
	}
	key := integrationID + ":" + channel
	now := time.Now()
	var stale string
	var haveStale bool
	if v, ok := e.templateCache.Load(key); ok {
		if entry, ok := v.(templateCacheEntry); ok {
			if now.Sub(entry.loadedAt) <= e.templateCacheTTL() {
				return entry.value
			}
			stale, haveStale = entry.value, true
		}
	}
	integ, err := e.store.GetItem(ctx, "integrations", integrationID)
	if err != nil {
		// Re-reading is how the entry expires, so a database blip must not
		// downgrade a configured template to the built-in default text. The
		// expired copy is the best available answer; the next delivery retries.
		if haveStale {
			return stale
		}
		return ""
	}
	if integ == nil {
		return ""
	}
	templates, _ := integ["templates"].(map[string]any)
	t := utils.StrVal(templates, channel)
	if t == "" {
		t = utils.StrVal(templates, "default")
	}
	e.templateCache.Store(key, templateCacheEntry{value: t, loadedAt: now})
	return t
}

// invalidateTemplateCache drops cached notification templates for an integration.
// Called after integration update/delete so a template change made in THIS
// process takes effect on the next delivery rather than after templateCacheTTL.
func (e *Engine) invalidateTemplateCache(integrationID string) {
	prefix := integrationID + ":"
	e.templateCache.Range(func(k, _ any) bool {
		if ks, ok := k.(string); ok && strings.HasPrefix(ks, prefix) {
			e.templateCache.Delete(k)
		}
		return true
	})
}

// executeCreateIssue schedules issue creation through the unlocked delivery
// pipeline instead of calling the tracker inline. The tracker call is outbound
// HTTP; doing it here would hold the escalation advisory lock and an open DB
// transaction for the duration of an external request. As an "issue" channel
// notification it instead gets the pipeline's claim/retry/circuit-breaker
// handling for free. token_env is passed through (not resolved) so the env
// secret is never persisted in the notification payload.
func (e *Engine) executeCreateIssue(state *store.State, g model.AlertGroup, step map[string]any, timestamp string) {
	tmplCtx := map[string]any{
		"title":    g.Title(),
		"severity": strDefault(g.Severity(), "unknown"),
		"group_id": g.ID(),
		"labels":   g.LabelsRaw(),
		"status":   strDefault(g.Status(), "open"),
	}
	subjectTmpl := strDefault(utils.StrVal(step, "subject_template"), "[{{ severity }}] {{ title }}")
	bodyTmpl := strDefault(utils.StrVal(step, "body_template"), "Alert group: {{ group_id }}\n\nTitle: {{ title }}\nSeverity: {{ severity }}")
	subject := renderTemplate(subjectTmpl, tmplCtx)
	body := renderTemplate(bodyTmpl, tmplCtx)

	trackerType := strDefault(utils.StrVal(step, "tracker_type"), "redmine")
	url := utils.StrVal(step, "url")

	// One issue per group and tracker: a REPEAT of the chain must not file the
	// same incident again.
	issueKey := fmt.Sprintf("%s::issue:%s:create issue via %s", g.ID(), url, trackerType)
	ntf := buildNotification(g, "", "issue", url, "create issue via "+trackerType, timestamp, issueKey)
	ntf.ScheduleDelivery(map[string]any{
		"tracker_type": trackerType,
		"token":        utils.StrVal(step, "token"),     // inline only; already in chain config
		"token_env":    utils.StrVal(step, "token_env"), // resolved at delivery, never persisted
		"project":      utils.StrVal(step, "project"),
		"subject":      subject,
		"body":         body,
		"url":          url,
	})
	addNotification(state, ntf, nil)
	g.AppendLog("create_issue_scheduled",
		fmt.Sprintf("Issue creation via %s scheduled", trackerType),
		map[string]any{"url": url})
}

// deliverIssue performs the tracker HTTP call for an "issue" channel notification
// in the unlocked delivery stage. token_env is resolved here so the env secret
// stays out of the persisted payload.
func (e *Engine) deliverIssue(ctx context.Context, payload map[string]any) (status, errMsg, providerResp string) {
	url := utils.StrVal(payload, "url")
	// Same as chatops: the tracker URL comes off the escalation step, not off
	// the notification target the dispatch pre-flight judged.
	if ok, detail := e.deliveryCfg.Channels.DestinationAllowed(url); !ok {
		return "failed", detail, ""
	}
	if err := checkWebhookURL(ctx, url, e.deliveryCfg.ssrfGuardFor("issue")); err != nil {
		return deliverySkipped, err.Error(), ""
	}
	token := utils.StrVal(payload, "token")
	if token == "" {
		if te := utils.StrVal(payload, "token_env"); te != "" {
			token = os.Getenv(te)
		}
	}
	trackerType := strDefault(utils.StrVal(payload, "tracker_type"), "redmine")
	subject := utils.StrVal(payload, "subject")
	body := utils.StrVal(payload, "body")
	var st, errStr string
	if trackerType == "redmine" {
		st, _, errStr = createRedmineIssue(ctx, e.deliveryCfg.clientFor("issue"), url, token, utils.StrVal(payload, "project"), subject, body)
	} else {
		st, _, errStr = createGenericIssue(ctx, e.deliveryCfg.clientFor("issue"), url, token, subject, body)
	}
	if st == "created" {
		return "delivered", "", trackerType + "_issue_created"
	}
	return "failed", errStr, ""
}

// sendDeadLetterEvent fires a non-blocking POST to DeadLetterWebhookURL when a
// notification transitions to permanently "failed". Must be called in a goroutine.
func (e *Engine) sendDeadLetterEvent(ctx context.Context, ntf map[string]any) {
	// Runs in its own goroutine; a panic here must not crash the worker.
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("dead_letter_panicked", "panic", rec)
			e.sink().IncWorkerPanic("dead_letter")
		}
	}()
	dlURL := e.deliveryCfg.DeadLetterWebhookURL
	if dlURL == "" {
		return
	}
	n := model.WrapNotification(ntf)
	event := map[string]any{
		"event":           "notification.failed",
		"notification_id": n.ID(),
		"alert_group_id":  n.AlertGroupID(),
		"user_id":         ntf["user_id"],
		"channel":         n.Channel(),
		"target":          ntf["target"],
		"retry_count":     n.RetryCount(),
		"last_error":      ntf["last_error"],
		"failed_at":       utils.ToISO(utils.UTCNow()),
	}
	data, err := json.Marshal(event)
	if err != nil {
		slog.Error("dead_letter_marshal_failed", "notification_id", n.ID(), "error", err)
		return
	}
	// Bound the fire-and-forget POST by the configured webhook timeout.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.deliveryCfg.WebhookTimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dlURL, bytes.NewReader(data))
	if err != nil {
		slog.Error("dead_letter_request_failed", "notification_id", n.ID(), "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.deliveryCfg.clientFor("webhook").Do(req)
	if err != nil {
		slog.Error("dead_letter_delivery_failed", "notification_id", n.ID(), "error", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("dead_letter_non_2xx", "notification_id", n.ID(), "status", resp.StatusCode)
	}
}
