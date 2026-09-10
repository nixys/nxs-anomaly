package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Inbound ChatOps: slash commands that arrive from Slack or Telegram directly,
// with their own signature proving they did.
//
// Until now the only way in was POST /api/v1/chatops/command, an internal
// endpoint authenticated by an nxs-anomaly credential: it could carry a command
// "from Slack" that Slack never sent. These handlers verify the provider's own
// signature instead, so an acknowledge that claims to come from a chat platform
// really did.
//
// All three platforms also deliver taps on the buttons attached to alert
// notifications: Telegram as a callback_query, Slack as a signed interactive
// payload, Mattermost as a callback carrying the context its button was built
// with. Every one of them is translated into the ChatOps command it stands for
// and runs through the same path as the typed word, so identity, team and role
// are decided in one place rather than once per platform.

// chatopsSigMaxSkew bounds how old a signed request may be. Slack recommends
// five minutes; the same bound serves as the replay window here.
const chatopsSigMaxSkew = 5 * time.Minute

// maxChatopsBody bounds a slash-command body.
const maxChatopsBody = 64 * 1024

type chatopsInboundConfig struct {
	slackSigningSecret     string
	telegramSecretToken    string
	mattermostActionSecret string
	// mattermostCommandToken is separate from the action secret because
	// Mattermost generates it: an operator can paste it in, but cannot choose
	// it, so the two values are never the same one.
	mattermostCommandToken string
}

func chatopsInboundConfigFromEnv() chatopsInboundConfig {
	return chatopsInboundConfig{
		slackSigningSecret:     strings.TrimSpace(os.Getenv("NXS_ANOMALY_SLACK_SIGNING_SECRET")),
		telegramSecretToken:    strings.TrimSpace(os.Getenv("NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET")),
		mattermostActionSecret: strings.TrimSpace(os.Getenv("NXS_ANOMALY_MATTERMOST_ACTION_SECRET")),
		mattermostCommandToken: strings.TrimSpace(os.Getenv("NXS_ANOMALY_MATTERMOST_COMMAND_TOKEN")),
	}
}

// handleSlackCommand accepts a Slack slash command.
//
// Slack signs the raw body, so the body is read once, verified, and only then
// parsed — parsing first and re-encoding would change the bytes the signature
// covers.
func (srv *Server) handleSlackCommand(w http.ResponseWriter, r *http.Request) {
	if !srv.allowChatopsInbound(w, r, "slack") {
		return
	}
	secret := srv.chatopsInbound.slackSigningSecret
	if secret == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": "slack integration is not configured (NXS_ANOMALY_SLACK_SIGNING_SECRET)",
		})
		return
	}
	body, ok := readRawBody(w, r)
	if !ok {
		return
	}
	if err := verifySlackSignature(r.Header, body, secret, time.Now()); err != nil {
		slog.Warn("chatops_inbound_rejected", "platform", "slack", "error", err)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "signature verification failed"})
		return
	}

	form, err := url.ParseQuery(string(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed slash command payload"})
		return
	}
	command := strings.TrimSpace(form.Get("command") + " " + form.Get("text"))
	srv.dispatchChatopsCommand(w, r, "slack", form.Get("channel_id"), form.Get("user_name"),
		form.Get("user_id"), command)
}

// handleSlackInteractive accepts a tap on a Slack message button.
//
// Slack posts these form-encoded with the action JSON in a "payload" field,
// signed with the same v0 scheme as slash commands — so the credential check is
// the one already proven, and only the shape of the body differs.
//
// The reply replaces the message: an alert that has been acknowledged must stop
// offering "Acknowledge", and the ephemeral confirmation Slack shows instead
// would be gone within seconds.
func (srv *Server) handleSlackInteractive(w http.ResponseWriter, r *http.Request) {
	if !srv.allowChatopsInbound(w, r, "slack") {
		return
	}
	secret := srv.chatopsInbound.slackSigningSecret
	if secret == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": "slack integration is not configured (NXS_ANOMALY_SLACK_SIGNING_SECRET)",
		})
		return
	}
	body, ok := readRawBody(w, r)
	if !ok {
		return
	}
	if err := verifySlackSignature(r.Header, body, secret, time.Now()); err != nil {
		slog.Warn("chatops_inbound_rejected", "platform", "slack", "error", err)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "signature verification failed"})
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed interactive payload"})
		return
	}
	var interaction struct {
		User struct {
			ID   string `json:"id"`
			Name string `json:"username"`
		} `json:"user"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		// What the message being acted on says, so the verdict can be appended
		// to the alert rather than replace it.
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(form.Get("payload")), &interaction); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed interactive payload"})
		return
	}
	if len(interaction.Actions) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "interactive payload carries no action"})
		return
	}
	action := interaction.Actions[0]
	command, ok := engine.ChatActionCommand(action.ActionID, action.Value)
	if !ok {
		slog.Warn("chatops_action_unknown", "platform", "slack", "action", action.ActionID)
		writeJSON(w, http.StatusOK, map[string]any{
			"replace_original": false,
			"text":             "This button is no longer supported",
		})
		return
	}
	out := srv.runChatopsCommand(r.Context(), "slack", interaction.Channel.ID, interaction.User.Name,
		interaction.User.ID, command)
	if out.err != nil {
		// A refusal changes nothing, so the message keeps its buttons and only
		// the person who tapped is told why.
		writeJSON(w, http.StatusOK, map[string]any{"replace_original": false, "text": out.text})
		return
	}
	writeJSON(w, http.StatusOK, engine.SlackSettledPayload(
		interaction.Message.Text, out.text, engine.UndoActionFor(command), action.Value))
}

// handleMattermostAction accepts a tap on a Mattermost message button.
//
// Mattermost does not sign these callbacks: the button carries a context object
// that comes back verbatim, and whatever secret was put there is the credential.
// It is compared in constant time, and the endpoint refuses to run without one
// configured — otherwise anyone who learned the URL could acknowledge alerts.
func (srv *Server) handleMattermostAction(w http.ResponseWriter, r *http.Request) {
	if !srv.allowChatopsInbound(w, r, "mattermost") {
		return
	}
	secret := srv.chatopsInbound.mattermostActionSecret
	if secret == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": "mattermost integration is not configured (NXS_ANOMALY_MATTERMOST_ACTION_SECRET)",
		})
		return
	}
	body, ok := readRawBody(w, r)
	if !ok {
		return
	}
	var action struct {
		UserID    string `json:"user_id"`
		UserName  string `json:"user_name"`
		ChannelID string `json:"channel_id"`
		Context   struct {
			Action  string `json:"action"`
			GroupID string `json:"group_id"`
			Token   string `json:"token"`
		} `json:"context"`
	}
	if err := json.Unmarshal(body, &action); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed mattermost action"})
		return
	}
	if subtleCompare(action.Context.Token, secret) != 1 {
		slog.Warn("chatops_inbound_rejected", "platform", "mattermost", "error", "action token mismatch")
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "action token mismatch"})
		return
	}
	command, ok := engine.ChatActionCommand(action.Context.Action, action.Context.GroupID)
	if !ok {
		slog.Warn("chatops_action_unknown", "platform", "mattermost", "action", action.Context.Action)
		writeJSON(w, http.StatusOK, map[string]any{"ephemeral_text": "This button is no longer supported"})
		return
	}
	out := srv.runChatopsCommand(r.Context(), "mattermost", action.ChannelID, action.UserName,
		action.UserID, command)
	if out.err != nil {
		// A refusal changes nothing, so the post keeps its buttons and only the
		// person who tapped is told why.
		writeJSON(w, http.StatusOK, map[string]any{"ephemeral_text": out.text})
		return
	}
	writeJSON(w, http.StatusOK, engine.MattermostSettledUpdate(out.text,
		engine.UndoActionFor(command), action.Context.GroupID,
		srv.eng.PublicURL(), secret))
}

// handleMattermostCommand accepts a Mattermost slash command.
//
// Mattermost posts these form-encoded with the token it generated for the
// command, which is the only credential the request carries — it does not sign
// them — so the token is compared in constant time, exactly as the action
// callback's is.
//
// Until this existed the Mattermost bot was one-way: it could show two buttons
// and take a tap, and there was no way to ask it anything. Every command the
// engine supports was reachable from Telegram and Slack and from nowhere else.
func (srv *Server) handleMattermostCommand(w http.ResponseWriter, r *http.Request) {
	if !srv.allowChatopsInbound(w, r, "mattermost") {
		return
	}
	token := srv.chatopsInbound.mattermostCommandToken
	if token == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": "mattermost commands are not configured (NXS_ANOMALY_MATTERMOST_COMMAND_TOKEN)",
		})
		return
	}
	body, ok := readRawBody(w, r)
	if !ok {
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed slash command payload"})
		return
	}
	if subtleCompare(form.Get("token"), token) != 1 {
		slog.Warn("chatops_inbound_rejected", "platform", "mattermost", "error", "command token mismatch")
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "command token mismatch"})
		return
	}
	// One slash command carries all of them, and its trigger word is whatever
	// the operator registered — "/nxs", "/oncall", anything. So the trigger is
	// dropped and the arguments are the ChatOps command: "/nxs ack grp_1" runs
	// "ack grp_1". Slack keeps its own shape, where the trigger word is the verb
	// and each one is registered separately.
	command := strings.TrimSpace(form.Get("text"))
	if command == "" {
		command = "help"
	}
	srv.dispatchChatopsCommand(w, r, "mattermost", form.Get("channel_id"), form.Get("user_name"),
		form.Get("user_id"), command)
}

// handleTelegramCommand accepts a Telegram bot update.
//
// Telegram does not sign the body; it echoes a secret token in a header, set
// when the webhook is registered. That token is the credential, so it is
// compared in constant time and the endpoint refuses to run without one.
func (srv *Server) handleTelegramCommand(w http.ResponseWriter, r *http.Request) {
	if !srv.allowChatopsInbound(w, r, "telegram") {
		return
	}
	secret := srv.chatopsInbound.telegramSecretToken
	if secret == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": "telegram integration is not configured (NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET)",
		})
		return
	}
	presented := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if subtleCompare(presented, secret) != 1 {
		slog.Warn("chatops_inbound_rejected", "platform", "telegram", "error", "secret token mismatch")
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "secret token mismatch"})
		return
	}
	body, ok := readRawBody(w, r)
	if !ok {
		return
	}
	var update struct {
		Message struct {
			Text string `json:"text"`
			From struct {
				ID       any    `json:"id"`
				Username string `json:"username"`
			} `json:"from"`
			Chat struct {
				ID any `json:"id"`
			} `json:"chat"`
		} `json:"message"`
		CallbackQuery *struct {
			ID   string `json:"id"`
			Data string `json:"data"`
			From struct {
				ID       any    `json:"id"`
				Username string `json:"username"`
			} `json:"from"`
			Message struct {
				MessageID int64  `json:"message_id"`
				Text      string `json:"text"`
				Chat      struct {
					ID any `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
	}
	if err := json.Unmarshal(body, &update); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed telegram update"})
		return
	}
	if cq := update.CallbackQuery; cq != nil {
		srv.handleTelegramCallback(w, r, telegramCallback{
			id:          cq.ID,
			data:        cq.Data,
			chatID:      telegramID(cq.Message.Chat.ID),
			messageID:   cq.Message.MessageID,
			messageText: cq.Message.Text,
			fromID:      telegramID(cq.From.ID),
			fromHandle:  cq.From.Username,
		})
		return
	}
	srv.dispatchChatopsCommand(w, r, "telegram",
		telegramID(update.Message.Chat.ID),
		update.Message.From.Username,
		telegramID(update.Message.From.ID),
		strings.TrimSpace(update.Message.Text))
}

// telegramCallback is one tapped inline button.
type telegramCallback struct {
	id          string // callback_query_id, needed to stop the button spinning
	data        string // what this service put on the button
	chatID      string
	messageID   int64  // the message the button sits on, so it can be redrawn in place
	messageText string // what that message says, so a verdict can be appended to it
	fromID      string
	fromHandle  string
}

// handleTelegramCallback runs a tapped button as the ChatOps command it stands
// for, then tells Telegram what happened.
//
// The tap takes the same path as the typed word — same identity resolution,
// same team and role boundaries — because a shortcut that decided access
// differently would be a second, weaker way in.
//
// The webhook always answers 200. Telegram redelivers an update it could not
// deliver, and a refused acknowledge is a final answer, not a transient
// failure: retrying it would re-run the command on every redelivery. The verdict
// reaches the person through answerCallbackQuery instead.
func (srv *Server) handleTelegramCallback(w http.ResponseWriter, r *http.Request, cb telegramCallback) {
	command, ok := engine.ParseTelegramCallbackData(cb.data)
	if !ok {
		slog.Warn("chatops_callback_unknown_action", "platform", "telegram", "data", cb.data)
		srv.answerTelegramCallback(r.Context(), cb.id, "This button is no longer supported")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "text": "unsupported callback action"})
		return
	}
	out := srv.runChatopsCommand(r.Context(), "telegram", cb.chatID, cb.fromHandle, cb.fromID, command)
	srv.answerTelegramCallback(r.Context(), cb.id, out.text)
	// Paging replaces the listing in place. Sending a new message per tap would
	// bury the chat under near-identical lists, and the one worth reading would
	// be whichever happened to be last.
	publicURL := srv.eng.PublicURL()
	if reply := telegramPagerReply(cb, out, publicURL); reply != nil {
		writeJSON(w, http.StatusOK, reply)
		return
	}
	if reply := telegramCardReply(cb, out, publicURL); reply != nil {
		writeJSON(w, http.StatusOK, reply)
		return
	}
	if reply := telegramSettledReply(cb, command, out); reply != nil {
		writeJSON(w, http.StatusOK, reply)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "text": out.text})
}

// telegramSettledReply rewrites the alert message once its buttons have been
// used, or nil when this tap changed nothing.
//
// Without it the message keeps offering "Acknowledge" after the alert was
// acknowledged: the toast that said so is gone within seconds, and what is left
// on screen is a button implying the work is still waiting. The verdict is
// appended to the text the message already carries, and the keyboard is dropped
// because there is nothing left to press.
func telegramSettledReply(cb telegramCallback, command string, out chatopsOutcome) map[string]any {
	if out.err != nil || cb.messageID == 0 || cb.chatID == "" || cb.messageText == "" {
		return nil
	}
	response, _ := out.result["response"].(map[string]any)
	if response == nil || response["alert_group"] == nil {
		return nil
	}
	return map[string]any{
		"method":     "editMessageText",
		"chat_id":    cb.chatID,
		"message_id": cb.messageID,
		"text":       cb.messageText + "\n\n✓ " + out.text,
		// An empty inline_keyboard removes the buttons; omitting reply_markup
		// entirely would leave the old ones in place.
		"reply_markup":             telegramUndoMarkup(command, response),
		"disable_web_page_preview": true,
	}
}

// telegramUndoMarkup leaves exactly one button on a settled alert: the one that
// takes the action back.
//
// A mis-tapped Resolve from a phone was otherwise unfixable from the chat — the
// only way back was to open the web UI, which is precisely what somebody paged
// at three in the morning cannot conveniently do. An empty inline_keyboard is
// what removes the buttons; omitting reply_markup would leave the old ones.
func telegramUndoMarkup(command string, response map[string]any) map[string]any {
	undo := engine.UndoActionFor(command)
	groupID := ""
	if g, ok := response["alert_group"].(map[string]any); ok {
		groupID = utils.StrVal(g, "id")
	}
	if row := engine.TelegramUndoRow(undo, groupID); row != nil {
		return map[string]any{"inline_keyboard": []any{row}}
	}
	return map[string]any{"inline_keyboard": []any{}}
}

// telegramPagerReply renders the webhook answer that redraws a listing in place,
// or nil when this tap produced no listing to redraw.
func telegramPagerReply(cb telegramCallback, out chatopsOutcome, publicURL string) map[string]any {
	if out.err != nil || cb.messageID == 0 || cb.chatID == "" {
		return nil
	}
	response, _ := out.result["response"].(map[string]any)
	if response == nil || response["alerts_page"] == nil {
		return nil
	}
	reply := engine.TelegramReplyPayload(cb.chatID, out.text, out.result, publicURL)
	reply["method"] = "editMessageText"
	reply["message_id"] = cb.messageID
	return reply
}

// telegramCardReply answers a tap on a listing row with the group's own card as
// a new message, or nil when this tap opened nothing.
//
// A new message rather than an edit: the listing is what the person came from
// and will go back to, and replacing it with one group would cost them the other
// thirty-nine.
func telegramCardReply(cb telegramCallback, out chatopsOutcome, publicURL string) map[string]any {
	if out.err != nil || cb.chatID == "" {
		return nil
	}
	response, _ := out.result["response"].(map[string]any)
	if response == nil || response["alert_group_card"] == nil {
		return nil
	}
	reply := engine.TelegramReplyPayload(cb.chatID, out.text, out.result, publicURL)
	reply["method"] = "sendMessage"
	return reply
}

// answerTelegramCallback reports the verdict back to the tapped button, logging
// rather than failing: the command has already run, and an unanswered callback
// must not be retried into running it twice.
func (srv *Server) answerTelegramCallback(ctx context.Context, callbackID, text string) {
	if callbackID == "" {
		return
	}
	if err := srv.eng.AnswerTelegramCallback(ctx, callbackID, text); err != nil {
		slog.Warn("chatops_callback_answer_failed", "platform", "telegram", "error", err)
	}
}

// telegramID renders a Telegram numeric id as the string the rest of the
// service stores. Telegram sends ids as JSON numbers, but a chat id is also
// accepted as a string when an operator typed one into a channel's external id,
// so both shapes decode to the same text.
func telegramID(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	}
	return ""
}

// dispatchChatopsCommand resolves the platform channel to a configured ChatOps
// channel and runs the command through the same engine path as the internal
// API, so behaviour cannot drift between the two entry points.
// A refusal is answered in the platform's own shape, with 200, for the same
// reason the button path already does: a chat platform reads a non-2xx as a
// delivery it should retry, and re-delivering a command that was refused runs it
// again. Telegram additionally ignores any body that is not a method call, so
// the old JSON error object reached nobody — a typo answered with silence, and
// the update was redelivered until Telegram gave up on it.
func (srv *Server) dispatchChatopsCommand(w http.ResponseWriter, r *http.Request, platform, externalID, chatHandle, platformUserID, command string) {
	out := srv.runChatopsCommand(r.Context(), platform, externalID, chatHandle, platformUserID, command)
	if out.err != nil || out.status != http.StatusOK {
		writeJSON(w, http.StatusOK, chatopsErrorReply(platform, externalID, out))
		return
	}
	writeJSON(w, http.StatusOK, chatopsReply(platform, externalID, out.result, srv.eng.PublicURL()))
}

// chatopsErrorReply renders a refusal the way the platform will actually show
// it. The text is the engine's own message, which already says what was wrong
// and what to do about it.
func chatopsErrorReply(platform, chatID string, out chatopsOutcome) map[string]any {
	text := out.text
	if text == "" && out.err != nil {
		text = out.err.Error()
	}
	if text == "" {
		text = "command failed"
	}
	switch platform {
	case "slack", "mattermost":
		// Ephemeral: a refusal belongs to whoever typed it, not to the channel.
		return map[string]any{"response_type": "ephemeral", "text": text}
	default:
		if chatID == "" {
			return map[string]any{"ok": false, "text": text}
		}
		return map[string]any{"method": "sendMessage", "chat_id": chatID, "text": text}
	}
}

// chatopsOutcome is what an inbound command produced, in the shape both entry
// points need: a typed command answers over HTTP, a tapped button answers
// through Telegram's own callback API, and neither may reach a different
// verdict than the other.
type chatopsOutcome struct {
	result map[string]any // engine answer, on success
	text   string         // what to show the person
	status int            // HTTP status this entry point should use
	err    error          // engine error, mapped to a status by writeResult
}

// runChatopsCommand resolves the sender, the channel and runs the command.
func (srv *Server) runChatopsCommand(parent context.Context, platform, externalID, chatHandle, platformUserID, command string) chatopsOutcome {
	if command == "" {
		return chatopsOutcome{text: "empty command", status: http.StatusBadRequest}
	}
	ctx := authz.NewContext(parent, chatopsServiceActor(platform))
	principal, ok := srv.chatopsPrincipal(ctx, platform, platformUserID)
	if !ok {
		return chatopsOutcome{
			text:   "this account is registered here but currently has no role",
			status: http.StatusForbidden,
		}
	}
	ctx = authz.NewContext(parent, principal)
	channelID, err := srv.eng.FindChatopsChannelByExternalID(ctx, platform, externalID)
	if err != nil {
		return chatopsOutcome{text: "internal error", status: http.StatusInternalServerError}
	}
	post := srv.eng.PostChatopsCommand
	if channelID == "" {
		// A private chat with the bot is not anybody's ChatOps channel, and it
		// is where every personal notification — and therefore every button on
		// one — lands. Refusing here is what made those buttons do nothing.
		//
		// Only for somebody this deployment recognises: the command then runs
		// under their own role and team scope. A shared chat that nobody
		// claims falls back to the platform service principal, and running
		// unbound commands as that would give any chat the bot was added to
		// the right to acknowledge alerts.
		if principal.Kind != authz.KindUser || principal.ID == "" {
			// Named explicitly: an operator wiring this up needs to know the
			// channel is unknown here, not that "something went wrong".
			return chatopsOutcome{
				text:   fmt.Sprintf("no %s chatops channel is bound to %q", platform, externalID),
				status: http.StatusNotFound,
			}
		}
		post = srv.eng.PostChatopsDirectCommand
	}
	result, err := post(ctx, map[string]any{
		"channel_id": channelID,
		"command":    command,
		"actor":      chatHandle,
	})
	if err != nil {
		return chatopsOutcome{err: err, text: err.Error(), status: http.StatusOK}
	}
	return chatopsOutcome{result: result, text: chatopsResponseText(result), status: http.StatusOK}
}

// chatopsResponseText digs the human-readable line out of the engine's answer.
func chatopsResponseText(result map[string]any) string {
	if response, ok := result["response"].(map[string]any); ok {
		if text, _ := response["text"].(string); text != "" {
			return text
		}
	}
	return "Command accepted"
}

// chatopsServiceActor is what an inbound command runs as when no person behind
// it can be identified.
//
// The signature proved the platform sent the request, so it acts as a
// responder — enough to acknowledge or resolve, and nothing more. The principal
// is named after the platform so the audit trail distinguishes a signed command
// from an operator using the internal API.
func chatopsServiceActor(platform string) authz.Actor {
	return authz.Actor{
		ID:          "chatops:" + platform,
		Kind:        authz.KindService,
		DisplayName: platform + " (signed webhook)",
		Role:        authz.RoleResponder,
	}
}

// chatopsPrincipal resolves the person behind an inbound command, falling back
// to the platform service principal when there is none to find.
//
// Three outcomes, and the middle one is why this is not a plain lookup:
//
//   - No platform account id (Slack today, or an update without a sender): the
//     service principal, exactly as before.
//   - An account id nobody claims: also the service principal. A shared team
//     chat is a legitimate way to run these commands, and refusing unknown
//     senders would break every installation that uses one.
//   - An account id that maps to a user whose role was cleared: refused. Here
//     falling back would *grant* rights the person no longer has, which is the
//     one case where the fallback is worse than the failure.
//
// A resolved user brings their own role and team scope, so the acknowledge is
// attributed to them and bounded by what they may reach. The service principal
// is neither.
func (srv *Server) chatopsPrincipal(ctx context.Context, platform, platformUserID string) (authz.Actor, bool) {
	service := chatopsServiceActor(platform)
	if platformUserID == "" {
		return service, true
	}
	user, err := srv.eng.FindUserByChatAccount(ctx, platform, platformUserID)
	if err != nil {
		// Fail closed on a lookup that did not run: reading an error as "nobody
		// claims this id" would silently hand out the service principal's rights.
		slog.Error("chatops_identity_lookup_failed", "platform", platform, "error", err)
		return authz.Actor{}, false
	}
	if user == nil {
		return service, true
	}
	role := authz.ParseRole(utils.StrVal(user, "role"))
	if !role.Valid() {
		slog.Warn("chatops_identity_no_role", "platform", platform, "user_id", utils.StrVal(user, "id"))
		return authz.Actor{}, false
	}
	actor := userActor(user, role)
	if err := srv.applyTeamScope(ctx, &actor); err != nil {
		slog.Error("team_scope_lookup_failed", "user_id", actor.ID, "error", err)
		return authz.Actor{}, false
	}
	return actor, true
}

// chatopsReply reshapes the engine's answer into what the platform will
// actually show the person who typed the command.
//
// The engine already composes the text ("Acknowledged grp_1"), but nests it
// under "response". Slack renders a slash-command reply from a top-level
// "text", so returning the raw object left the responder staring at nothing
// while the acknowledge silently succeeded. Ephemeral by default: the answer
// belongs to whoever asked, not to the whole channel.
//
// Telegram had the same bug, unfixed: it ignores a webhook response that is not
// a method call, so a typed command answered nothing at all. The reply is now
// the method call — which also means the answer costs no second HTTP request to
// the Bot API.
func chatopsReply(platform, chatID string, result map[string]any, publicURL string) map[string]any {
	text := chatopsResponseText(result)
	switch platform {
	case "slack", "mattermost":
		return map[string]any{"response_type": "ephemeral", "text": text}
	default:
		if chatID == "" {
			// No chat to answer in: keep a readable body so the endpoint stays
			// diagnosable with curl.
			return map[string]any{"ok": true, "text": text}
		}
		reply := engine.TelegramReplyPayload(chatID, text, result, publicURL)
		reply["method"] = "sendMessage"
		return reply
	}
}

// allowChatopsInbound rate-limits the signed inbound endpoints per client
// address.
//
// They are public, and verification is not free: the body is read and an HMAC
// computed before a request can be rejected. Every other public route in this
// service is limited; these were the exception. The key is the caller's address
// rather than an integration key, because an unsigned request has no identity
// yet.
func (srv *Server) allowChatopsInbound(w http.ResponseWriter, r *http.Request, platform string) bool {
	if srv.webhookLimiter == nil {
		return true
	}
	if srv.webhookLimiter.allow("chatops:" + platform + ":" + srv.clientIP(r)) {
		return true
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
	return false
}

// readRawBody reads a bounded request body. Signature verification needs the
// exact bytes, so this returns them rather than a decoded map.
func readRawBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxChatopsBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cannot read body"})
		return nil, false
	}
	if len(body) > maxChatopsBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "body too large"})
		return nil, false
	}
	return body, true
}

// verifySlackSignature implements Slack's v0 scheme: HMAC-SHA256 over
// "v0:{timestamp}:{body}" keyed with the signing secret, compared in constant
// time, with a freshness bound that doubles as the replay window.
func verifySlackSignature(h http.Header, body []byte, secret string, now time.Time) error {
	tsRaw := h.Get("X-Slack-Request-Timestamp")
	if tsRaw == "" {
		return fmt.Errorf("missing X-Slack-Request-Timestamp")
	}
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("malformed timestamp")
	}
	age := now.Sub(time.Unix(ts, 0))
	if age < 0 {
		age = -age
	}
	if age > chatopsSigMaxSkew {
		return fmt.Errorf("stale request (%s old)", age.Round(time.Second))
	}
	presented := h.Get("X-Slack-Signature")
	if presented == "" {
		return fmt.Errorf("missing X-Slack-Signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	// Writing to an hmac.Hash never returns an error; the discard is explicit
	// so the linter and the next reader both know it was considered.
	_, _ = fmt.Fprintf(mac, "v0:%s:%s", tsRaw, body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if subtleCompare(presented, expected) != 1 {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// subtleCompare is a constant-time string comparison returning 1 on equality.
func subtleCompare(a, b string) int {
	if len(a) != len(b) {
		// hmac.Equal is constant time only for equal lengths; a length
		// mismatch is not secret — the signature format is public.
		return 0
	}
	if hmac.Equal([]byte(a), []byte(b)) {
		return 1
	}
	return 0
}
