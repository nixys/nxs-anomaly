package engine

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// ── Chatops Channels ──────────────────────────────────────────────────────────

func (e *Engine) CreateChatopsChannel(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"platform", "name"}); err != nil {
		return nil, errValidation(err.Error())
	}
	if err := rejectInlineSecret("webhook_url", utils.StrVal(payload, "webhook_url")); err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	teamID := nilIfEmpty(utils.StrVal(payload, "team_id"))
	userID := nilIfEmpty(utils.StrVal(payload, "user_id"))
	if teamID != nil {
		if err := e.ensureTeamsExist(ctx, []string{teamID.(string)}); err != nil {
			return nil, err
		}
	}
	if userID != nil {
		if err := e.ensureUsersExist(ctx, []string{userID.(string)}); err != nil {
			return nil, err
		}
	}

	result, err := e.store.UpdateCollections(ctx, []string{"chatops_channels"}, []string{"chatops_channels"},
		func(state *store.State) (any, error) {
			platform := strings.ToLower(fmt.Sprintf("%v", payload["platform"]))
			if err := duplicateChatopsBinding(state, "", platform, utils.StrVal(payload, "external_id")); err != nil {
				return nil, err
			}
			channel := map[string]any{
				"id":               utils.MakeID("chat"),
				"platform":         platform,
				"name":             fmt.Sprintf("%v", payload["name"]),
				"team_id":          teamID,
				"user_id":          userID,
				"commands_enabled": utils.BoolVal(payload, "commands_enabled", true),
				// Incoming-webhook URL of the Slack/Mattermost channel. Without
				// it the channel exists only inside this service: commands can
				// be answered, but nothing can be pushed to it, and
				// notifications for it are skipped rather than "delivered".
				"webhook_url": utils.StrVal(payload, "webhook_url"),
				// Identifier of the channel on the platform itself (Slack
				// channel id, Telegram chat id). Inbound slash commands arrive
				// naming this, not our internal id.
				"external_id":           utils.StrVal(payload, "external_id"),
				"notifications_enabled": utils.BoolVal(payload, "notifications_enabled", true),
				"created_at":            ts,
				"updated_at":            ts,
			}
			stampProvisioner(ctx, channel)
			state.ChatopsChannels[channel["id"].(string)] = channel
			e.auditIn(state, ctx, AuditCreate, "chatops_channel", channel["id"].(string), auditFields(payload, nil))
			return channel, nil
		}, advisoryLock["create_chatops_channel"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) UpdateChatopsChannel(ctx context.Context, channelID string, payload map[string]any) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	var teamID any
	teamIDSet := false
	if v, ok := payload["team_id"]; ok {
		teamIDSet = true
		teamID = nilIfEmpty(fmt.Sprintf("%v", v))
		if teamID != nil {
			if err := e.ensureTeamsExist(ctx, []string{teamID.(string)}); err != nil {
				return nil, err
			}
		}
	}
	var userID any
	userIDSet := false
	if v, ok := payload["user_id"]; ok {
		userIDSet = true
		userID = nilIfEmpty(fmt.Sprintf("%v", v))
		if userID != nil {
			if err := e.ensureUsersExist(ctx, []string{userID.(string)}); err != nil {
				return nil, err
			}
		}
	}
	// The whole collection, not just this row: moving a channel's external id
	// has to see the other channels to know the id is free. There are as many
	// of these as a team has chat rooms.
	result, err := e.store.UpdateCollections(ctx, []string{"chatops_channels"}, []string{"chatops_channels"},
		func(state *store.State) (any, error) {
			channel := state.ChatopsChannels[channelID]
			if channel == nil {
				return nil, errNotFound(fmt.Sprintf("chatops_channel %s not found", channelID))
			}
			if err := guardProvisioned(ctx, "chatops_channels", channel); err != nil {
				return nil, err
			}
			if v, ok := payload["name"]; ok {
				channel["name"] = fmt.Sprintf("%v", v)
			}
			if v, ok := payload["platform"]; ok {
				channel["platform"] = strings.ToLower(fmt.Sprintf("%v", v))
			}
			if v, ok := payload["notifications_enabled"]; ok {
				channel["notifications_enabled"] = utils.BoolVal(map[string]any{"v": v}, "v", true)
			}
			if v, ok := payload["webhook_url"]; ok {
				sv := fmt.Sprintf("%v", v)
				if err := rejectInlineSecret("webhook_url", sv); err != nil {
					return nil, err
				}
				channel["webhook_url"] = sv
			}
			if v, ok := payload["external_id"]; ok {
				if err := duplicateChatopsBinding(state, channelID,
					utils.StrVal(channel, "platform"), fmt.Sprintf("%v", v)); err != nil {
					return nil, err
				}
				channel["external_id"] = fmt.Sprintf("%v", v)
			}
			if v, ok := payload["commands_enabled"]; ok {
				channel["commands_enabled"] = utils.BoolVal(map[string]any{"v": v}, "v", true)
			}
			if teamIDSet {
				channel["team_id"] = teamID
			}
			if userIDSet {
				channel["user_id"] = userID
			}
			channel["updated_at"] = ts
			e.auditIn(state, ctx, AuditUpdate, "chatops_channel", channelID, auditFields(payload, nil))
			return channel, nil
		}, advisoryLock["update_chatops_channel"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) PostChatopsCommand(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"channel_id", "command"}); err != nil {
		return nil, errValidation(err.Error())
	}
	return e.postChatopsCommand(ctx, payload, false)
}

// PostChatopsDirectCommand runs a command that arrived in a private chat with
// the bot, where there is no configured ChatOps channel to run it in.
//
// This is the path every button on a personal notification needs. Alerts are
// delivered to a person's own Telegram chat, the notification carries
// Acknowledge and Resolve, and the tap comes back with that private chat's id —
// which is nobody's ChatOps channel, so the command was refused with "no
// telegram chatops channel is bound to 123456789" and the buttons did nothing.
//
// It is not a way around the channel rules. The caller must have resolved the
// sender to a real user (server.chatopsPrincipal), and the command then runs
// under that person's own role and team scope, with no channel team to narrow
// it further — the same rights they have in the web UI, which is where they
// would otherwise have to go to press the same button.
func (e *Engine) PostChatopsDirectCommand(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"command"}); err != nil {
		return nil, errValidation(err.Error())
	}
	return e.postChatopsCommand(ctx, payload, true)
}

// postChatopsCommand is the shared body. With direct set, no channel is loaded
// or required and the command runs against a channel-shaped stand-in that
// carries no team; everything else — the loads, the guards, the audit trail —
// is the one path both entry points go through.
func (e *Engine) postChatopsCommand(ctx context.Context, payload map[string]any, direct bool) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	channelID := utils.StrVal(payload, "channel_id")
	if direct {
		channelID = ""
	}
	commandLine := strings.TrimSpace(utils.StrVal(payload, "command"))
	// The payload's "actor" is a name claimed by the chat platform, not an
	// authenticated identity. The principal that authenticated the API call stays
	// accountable; the claimed handle rides along as data so the trail shows both.
	chatHandle := strDefault(utils.StrVal(payload, "actor"), "chatops-user")
	principal := authz.FromContext(ctx)

	// Load only what the command needs: the channel by id, and for group
	// commands either the unresolved groups (status) or the one referenced
	// group (ack/resolve). help/unknown commands load no groups at all.
	var loads []store.LoadSpec
	if !direct {
		loads = loadItems("chatops_channels", channelID)
	}
	cmdParts := strings.Fields(commandLine)
	if len(cmdParts) > 0 {
		switch strings.ToLower(strings.TrimPrefix(cmdParts[0], "/")) {
		case "status", "alerts", "bulk":
			loads = append(loads, store.LoadSpec{Collection: "alert_groups",
				Filters: map[string]any{"status": store.NotEqualFilter{Value: "resolved"}}})
		case "ack", "resolve", "show", "silence", "unack", "unacknowledge", "unresolve", "reopen":
			if len(cmdParts) > 1 {
				loads = append(loads, loadItems("alert_groups", cmdParts[1])...)
			}
		case "oncall", "whoisoncall":
			loads = append(loads,
				store.LoadSpec{Collection: "schedules"}, store.LoadSpec{Collection: "users"})
		case "duty":
			// Schedules decide when the check-in lapses; the user row is what it
			// is written on.
			loads = append(loads,
				store.LoadSpec{Collection: "schedules"}, store.LoadSpec{Collection: "users"})
		case "priority":
			// Teams bound the change: a chat may reorder its own people only.
			loads = append(loads,
				store.LoadSpec{Collection: "users"}, store.LoadSpec{Collection: "teams"})
		case "report":
			// Small collection, no filter: which row is "latest for this
			// caller's teams" is decided in the switch body, same as
			// status/alerts decide group visibility there rather than here.
			loads = append(loads, store.LoadSpec{Collection: "reports"})
		}
		// A group command is answered against the team owning the group, and a
		// group's team is its integration's. The collection is small, and
		// resolving the owner outside the lock would decide access from a
		// snapshot the mutation no longer runs on.
		switch strings.ToLower(strings.TrimPrefix(cmdParts[0], "/")) {
		case "status", "alerts", "ack", "resolve", "show", "silence",
			"unack", "unacknowledge", "unresolve", "reopen", "bulk":
			loads = append(loads, store.LoadSpec{Collection: "integrations"})
		}
		// Resolving from a chat tells whoever was paged that it is over, and
		// that needs their notification targets. "bulk" is here because one of
		// its verbs is resolve.
		switch strings.ToLower(strings.TrimPrefix(cmdParts[0], "/")) {
		case "resolve", "bulk":
			loads = append(loads, store.LoadSpec{Collection: "users"})
		}
	}
	result, err := e.store.UpdateCollectionsFiltered(ctx,
		loads,
		// notifications is in the list because two commands produce them:
		// resolving tells whoever was paged that it is over, and a takeover
		// tells the person it relieved. Left out, both were written into the
		// state and dropped when it was saved.
		[]string{"alert_groups", "chatops_messages", "users", "schedules", "notifications"},
		func(state *store.State) (any, error) {
			channel := directChatopsChannel()
			if !direct {
				channel = state.ChatopsChannels[channelID]
				if channel == nil {
					return nil, errNotFound(fmt.Sprintf("chatops channel %s not found", channelID))
				}
				if !utils.BoolVal(channel, "commands_enabled", true) {
					return nil, errValidation("commands are disabled for this chatops channel")
				}
			}
			response, err := e.executeChatopsCommand(ctx, state, channel, commandLine, principal, chatHandle)
			if err != nil {
				return nil, err
			}
			msg := map[string]any{
				"id":         utils.MakeID("chatmsg"),
				"channel_id": channelID,
				"direction":  "inbound",
				"actor":      chatHandle,
				"command":    commandLine,
				"response":   response,
				"created_at": ts,
			}
			state.ChatopsMessages[msg["id"].(string)] = msg
			return msg, nil
		}, advisoryLock["post_chatops_command"])
	if err != nil {
		return nil, err
	}
	// Reaching here means the command ran, so a resolve command resolved: its
	// alerts follow the group the same way they do from the API. Chat buttons
	// arrive here too — the interactive handlers run this same command.
	if len(cmdParts) > 1 && strings.EqualFold(strings.TrimPrefix(cmdParts[0], "/"), "resolve") {
		e.syncAlertStatusForGroups(ctx, []string{cmdParts[1]}, model.AlertStatusResolved)
	}
	return result.(map[string]any), nil
}

// directChatopsChannel stands in for the channel a private-chat command does
// not have. It carries no team_id on purpose: the only boundary left is the
// principal's own team scope, which is exactly the rule the REST API applies to
// the same person.
//
// A fresh map per call rather than a package-level value: it is handed to the
// same code that receives real, mutable channel rows, and one shared map would
// be a race between two concurrent commands the day that code writes to it.
func directChatopsChannel() map[string]any {
	return map[string]any{"platform": "telegram", "commands_enabled": true}
}

// chatopsGroupAccess reports whether principal, acting from channel, may see or
// act on g.
//
// Two boundaries apply and both must hold. The actor's own team scope is the
// one every other entry point enforces. The channel's team is the context the
// command arrived in: someone who belongs to two teams, typing in a chat bound
// to one of them, acts as that team — otherwise a team's chat would reach every
// group its members can touch anywhere else.
//
// The two cases where there is nothing to scope by are allowed, matching
// authorizeItem exactly — a group is reachable through the chat if and only if
// it is reachable through the REST API, and two functions answering the same
// question differently is the drift this whole file exists to avoid:
//
//   - No integration_id: the group was never attached to one (a direct page, a
//     seeded row). It carries no team, so no boundary is being crossed.
//   - An integration_id whose row is gone: hard-deleted, or a group older than
//     its integration. Also carries no team.
//
// Refusing the second case was tried and reverted: it made a group ackable from
// the API but not from the chat, which is a difference in what a responder can
// do depending on where they read the alert.
func chatopsGroupAccess(state *store.State, channel map[string]any, principal authz.Actor, g model.AlertGroup) bool {
	integrationID := g.IntegrationID()
	if integrationID == "" {
		return true
	}
	integration := state.Integrations[integrationID]
	if integration == nil {
		return true
	}
	teamID := utils.StrVal(integration, "team_id")
	if !principal.MayAccessTeam(teamID) {
		return false
	}
	if channelTeam := utils.StrVal(channel, "team_id"); channelTeam != "" && teamID != "" && teamID != channelTeam {
		return false
	}
	return true
}

// An installation digest contains all teams. Never send it to a team channel,
// even when the requesting administrator can read it privately.
func chatopsReportAccess(channel map[string]any, principal authz.Actor, reportTeamID string) bool {
	if reportTeamID == "" && principal.TeamScoped {
		return false
	}
	if !principal.MayAccessTeam(reportTeamID) {
		return false
	}
	if channelTeam := utils.StrVal(channel, "team_id"); channelTeam != "" && reportTeamID != channelTeam {
		return false
	}
	return true
}

// reportChatSummary is the ChatOps `report` command's reply text: the same
// PlainText() rendering stored on the row at generation time (see
// Report.toMap), so a chat and the emailed digest never disagree.
func reportChatSummary(report map[string]any) string {
	if text := utils.StrVal(report, "summary_text"); text != "" {
		return text
	}
	return "On-call quality report " + utils.StrVal(report, "id") + " has no summary text."
}

// dutyTakeDefaultHours is how long an unqualified "duty take" covers. Two hours
// is long enough to work an incident and short enough that forgetting to hand
// back does not silently rewrite the rota for the night.
const dutyTakeDefaultHours = 2

// dutyTakeMaxHours caps it. Beyond this it is not an emergency stand-in but a
// schedule change, and that belongs in the schedule where the whole team can
// see it rather than in a chat message nobody will scroll back to.
const dutyTakeMaxHours = 24

// dutyTake puts the caller on call by writing a schedule override.
//
// The schedule is chosen rather than typed when there is only one the caller can
// act on: an id is exactly what somebody woken at 4am should not have to find.
// With several, the candidates are listed and the caller names one — guessing
// would be a coin flip over who stops being paged.
func dutyTake(ctx context.Context, state *store.State, channel map[string]any, principal authz.Actor, args []string, now time.Time) (map[string]any, error) {
	hours := dutyTakeDefaultHours
	var scheduleID string
	for _, arg := range args {
		if n, err := strconv.Atoi(arg); err == nil {
			hours = n
			continue
		}
		scheduleID = arg
	}
	if hours < 1 || hours > dutyTakeMaxHours {
		return nil, errValidation(fmt.Sprintf("duty take accepts 1 to %d hours", dutyTakeMaxHours))
	}

	reachable := reachableSchedules(state, channel, principal)
	if scheduleID == "" {
		switch len(reachable) {
		case 0:
			return nil, errNotFound("no schedule you can act on")
		case 1:
			scheduleID = reachable[0]
		default:
			sort.Strings(reachable)
			return nil, errValidation("several schedules are yours; name one: duty take <schedule_id> [hours] — " +
				joinStrings(reachable, ", "))
		}
	}
	sched := state.Schedules[scheduleID]
	if sched == nil {
		return nil, errNotFound(fmt.Sprintf("schedule %s not found", scheduleID))
	}
	if !scheduleReachable(sched, channel, principal) {
		return nil, errForbiddenTeam("schedules")
	}

	// Read before writing: once the override lands, this schedule answers with
	// the person taking over, and whoever is being relieved is unrecoverable.
	relieved := ScheduleOnCallAt(sched, now)

	until := now.Add(time.Duration(hours) * time.Hour)
	ts := utils.ToISO(now)
	override := appendScheduleOverride(ctx, sched, principal.ID, now, until,
		"Taken from ChatOps by "+principal.DisplayName, ts)
	notifyRelieved(state, relieved, principal, sched, until, ts)

	// The override is what pages them; the check-in records that they said so,
	// and lapses with the override rather than outliving it.
	if user := state.Users[principal.ID]; user != nil {
		user["on_duty"] = true
		user["duty_checkin_at"] = ts
		user["duty_checkin_until"] = utils.ToISO(until)
		user["updated_at"] = ts
	}
	return map[string]any{
		"text":        fmt.Sprintf("You are on call for %s until %s", utils.StrVal(sched, "name"), utils.ToISO(until)),
		"schedule_id": scheduleID,
		"override":    override,
	}, nil
}

// findUserByUsername resolves the name a person would type in a chat. Ids are
// what the API takes; nobody types grp_a1b2c3 at a colleague.
func findUserByUsername(state *store.State, username string) map[string]any {
	for _, user := range state.Users {
		if strings.EqualFold(utils.StrVal(user, "username"), username) {
			return user
		}
	}
	return nil
}

// userTeamID returns any one team the user belongs to, or "" when they belong
// to none. Membership in a single shared team is enough to make them visible;
// somebody in no team is unassigned, which the scope rule treats as visible to
// everyone.
func userTeamID(state *store.State, userID string) string {
	for _, team := range state.Teams {
		for _, member := range anyToStringSlice(team["member_ids"]) {
			if member == userID {
				return utils.StrVal(team, "id")
			}
		}
	}
	return ""
}

// notifyRelieved tells the people a takeover just removed from call that it
// happened.
//
// Being taken off duty without being told is the same failure as being put on
// it without being told: the person keeps behaving as though the pager is
// theirs, or stops watching without knowing anyone else started. The person
// taking over is skipped — they are the one who typed the command.
func notifyRelieved(state *store.State, relieved []string, taker authz.Actor, sched map[string]any, until time.Time, ts string) {
	seen := notificationIdemSet(state)
	for _, userID := range relieved {
		if userID == taker.ID {
			continue
		}
		user := state.Users[userID]
		if user == nil {
			continue
		}
		text := fmt.Sprintf("%s took over on-call for %s until %s",
			taker.DisplayName, strDefault(utils.StrVal(sched, "name"), "your schedule"), utils.ToISO(until))
		for _, target := range shiftNotificationTargets(user) {
			channel := utils.StrVal(target, "type")
			// One notice per takeover, so a repeated command does not page the
			// person being relieved again with the same news.
			idemKey := fmt.Sprintf("%s:%s:%s:relieved:%s", utils.StrVal(sched, "id"), userID, channel, ts)
			// Built directly rather than through buildNotification: this notice
			// belongs to no alert group, and that helper reads the group's id.
			ntf := model.NewNotification("", "", userID, channel,
				utils.StrVal(target, "target"), "duty taken over", ts, idemKey)
			ntf.ScheduleDelivery(map[string]any{
				"title":       "On-call handover",
				"message":     text,
				"severity":    "info",
				"schedule_id": utils.StrVal(sched, "id"),
			})
			addNotification(state, ntf, seen)
		}
	}
}

// reachableSchedules lists the schedules this caller may act on from this chat.
func reachableSchedules(state *store.State, channel map[string]any, principal authz.Actor) []string {
	var ids []string
	for id, sched := range state.Schedules {
		if scheduleReachable(sched, channel, principal) {
			ids = append(ids, id)
		}
	}
	return ids
}

// scheduleReachable applies the same two boundaries as a group command: the
// caller's teams and the team this chat is bound to.
func scheduleReachable(sched map[string]any, channel map[string]any, principal authz.Actor) bool {
	teamID := utils.StrVal(sched, "team_id")
	if !principal.MayAccessTeam(teamID) {
		return false
	}
	if channelTeam := utils.StrVal(channel, "team_id"); channelTeam != "" && teamID != "" && teamID != channelTeam {
		return false
	}
	return true
}

// dutyCheckinFallback bounds a check-in made by someone no schedule has on call
// right now — a stand-in during an incident, most often. There is no shift end
// to expire it at, and a flag nobody ever clears is the failure mode this whole
// mechanism exists to avoid: the next escalation would page whoever last
// remembered to type "duty on".
const dutyCheckinFallback = 12 * time.Hour

// dutyCheckinExpiry decides when a check-in lapses: at the end of the shift the
// person is actually covering, or after the fallback when they are covering none.
//
// The earliest end wins when several schedules name them, so the flag never
// outlives the first shift it was meant for.
func dutyCheckinExpiry(state *store.State, userID string, now time.Time) time.Time {
	expiry := now.Add(dutyCheckinFallback)
	found := false
	for _, schedule := range state.Schedules {
		end, onCall := ShiftEndFor(schedule, userID, now)
		if !onCall {
			continue
		}
		if !found || end.Before(expiry) {
			expiry, found = end, true
		}
	}
	return expiry
}

// redactUserForChat trims a user record down to what a chat reply may echo.
// Notification targets are contact details, and a chat is a wider audience than
// the person who typed the command.
func redactUserForChat(user map[string]any) map[string]any {
	return map[string]any{
		"id":                 utils.StrVal(user, "id"),
		"username":           utils.StrVal(user, "username"),
		"on_duty":            utils.BoolVal(user, "on_duty", false),
		"duty_checkin_until": utils.StrVal(user, "duty_checkin_until"),
	}
}

// alertsPageSize is how many alert groups one page of the list shows. Telegram
// renders one button per group plus a navigation row, and a keyboard taller than
// this stops being something a person can scan at 4am.
const alertsPageSize = 5

// pageArg reads the 1-based page number a command was given, defaulting to the
// first page for anything that is not a page number.
func pageArg(args []string) int {
	if len(args) == 0 {
		return 1
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// alertsPage cuts an ordered group list into the requested page.
//
// The page is clamped rather than rejected: pages shrink under the reader as
// colleagues acknowledge and resolve, and a stale "next" tap should show the
// last page rather than an error about a page that existed a minute ago.
func alertsPage(groups []model.AlertGroup, page int) map[string]any {
	pages := (len(groups) + alertsPageSize - 1) / alertsPageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * alertsPageSize
	end := start + alertsPageSize
	if end > len(groups) {
		end = len(groups)
	}
	items := make([]map[string]any, 0, end-start)
	for _, g := range groups[start:end] {
		items = append(items, map[string]any{
			"id":       g.ID(),
			"title":    g.Title(),
			"severity": g.Severity(),
			"status":   string(g.Status()),
		})
	}
	text := fmt.Sprintf("Open alert groups: %d", len(groups))
	if len(groups) == 0 {
		text = "No open alert groups"
	} else if pages > 1 {
		text = fmt.Sprintf("%s — page %d of %d", text, page, pages)
	}
	return map[string]any{
		"text":        text,
		"alerts_page": items,
		"page":        page,
		"pages":       pages,
	}
}

// guardChatopsGroupCommand refuses a group command this principal may not run:
// first the team boundary, then the role.
//
// The role check exists because the inbound ChatOps path used to authenticate
// as a fixed responder service principal. Once a real person stands behind the
// command, their own role decides — and a viewer must not acknowledge merely
// because the request arrived through a chat platform.
func guardChatopsGroupCommand(state *store.State, channel map[string]any, principal authz.Actor, g model.AlertGroup) error {
	if !chatopsGroupAccess(state, channel, principal, g) {
		return errForbiddenTeam("alert_groups")
	}
	if !principal.Can(authz.ActionRespond) {
		return errForbidden("acting on alerts requires the responder role")
	}
	return nil
}

func (e *Engine) executeChatopsCommand(ctx context.Context, state *store.State, channel map[string]any, commandLine string, principal authz.Actor, chatHandle string) (map[string]any, error) {
	parts := strings.Fields(commandLine)
	if len(parts) == 0 {
		return nil, errValidation("empty chatops command")
	}
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "report", "/report":
		var latest map[string]any
		for _, rpt := range state.Reports {
			if !chatopsReportAccess(channel, principal, utils.StrVal(rpt, "team_id")) {
				continue
			}
			if latest == nil || utils.StrVal(rpt, "generated_at") > utils.StrVal(latest, "generated_at") {
				latest = rpt
			}
		}
		if latest == nil {
			return map[string]any{"text": "No on-call quality report has been generated yet for your teams."}, nil
		}
		return map[string]any{
			"text":   reportChatSummary(latest),
			"report": latest,
		}, nil

	case "status", "/status":
		var open []map[string]any
		for _, rec := range state.AlertGroups {
			g, ok := groupAG(rec)
			if !ok {
				continue
			}
			if !chatopsGroupAccess(state, channel, principal, g) {
				continue
			}
			if g.Status() != model.StatusResolved {
				open = append(open, g.Raw())
			}
		}
		return map[string]any{
			"text":              fmt.Sprintf("Open alert groups: %d", len(open)),
			"open_alert_groups": open,
		}, nil

	case "alerts", "/alerts":
		// status answers "how many"; alerts answers "which ones, and let me act
		// on them" — the same set, ordered and cut into pages a phone can show.
		var open []model.AlertGroup
		for _, rec := range state.AlertGroups {
			g, ok := groupAG(rec)
			if !ok || g.Status() == model.StatusResolved {
				continue
			}
			if !chatopsGroupAccess(state, channel, principal, g) {
				continue
			}
			open = append(open, g)
		}
		// Newest first: the group that just woke someone is the one they came to
		// act on. Ties break on id so paging is stable — without that a repeated
		// tap on "next" can show the same group twice and skip another.
		sort.Slice(open, func(i, j int) bool {
			if a, b := open[i].LastReceivedAt(), open[j].LastReceivedAt(); a != b {
				return a > b
			}
			return open[i].ID() < open[j].ID()
		})
		return alertsPage(open, pageArg(args)), nil

	case "duty", "/duty":
		if len(args) == 0 {
			return nil, errValidation("duty command requires on or off")
		}
		// Checking in is a statement about a person, so it needs one. The service
		// principal a shared chat falls back to is not somebody who can be on
		// duty, and letting it set the flag would put the whole chat on call.
		if principal.Kind != authz.KindUser || principal.ID == "" {
			return nil, errForbidden(
				"checking in needs a Telegram account linked to a user here; ask an administrator to set your telegram_id")
		}
		user := state.Users[principal.ID]
		if user == nil {
			return nil, errNotFound("your user record was not found")
		}
		ts := utils.ToISO(utils.UTCNow())
		switch strings.ToLower(args[0]) {
		case "take":
			// Taking over during an incident writes a schedule override, not the
			// check-in flag. The flag is invisible to the schedule engine, so
			// switching duty with it would leave NOTIFY_SCHEDULE, the coverage
			// report and the preview all still naming the person being replaced —
			// the two answers would disagree exactly when it matters.
			return dutyTake(ctx, state, channel, principal, args[1:], utils.UTCNow())
		case "on":
			until := dutyCheckinExpiry(state, principal.ID, utils.UTCNow())
			user["on_duty"] = true
			user["duty_checkin_at"] = ts
			user["duty_checkin_until"] = utils.ToISO(until)
			user["updated_at"] = ts
			return map[string]any{
				"text": fmt.Sprintf("You are on duty. Checked in until %s", utils.ToISO(until)),
				"user": redactUserForChat(user),
			}, nil
		case "off":
			user["on_duty"] = false
			user["duty_checkin_at"] = nil
			user["duty_checkin_until"] = nil
			user["updated_at"] = ts
			return map[string]any{"text": "You are off duty", "user": redactUserForChat(user)}, nil
		}
		return nil, errValidation("duty command requires on or off")

	case "priority", "/priority":
		// Priority orders who NOTIFY_DUTY_USERS reaches first among the people
		// who are on duty. Changing your own is arranging yourself in the queue;
		// changing someone else's decides when a colleague's phone rings, which
		// is an editor's call.
		if principal.Kind != authz.KindUser || principal.ID == "" {
			return nil, errForbidden(
				"changing priority needs a Telegram account linked to a user here")
		}
		if len(args) == 0 {
			return nil, errValidation("priority command requires " + joinStrings(priorityOrder, ", "))
		}
		targetID, level := principal.ID, strings.ToLower(args[0])
		if len(args) > 1 {
			level = strings.ToLower(args[1])
			target := findUserByUsername(state, args[0])
			if target == nil {
				return nil, errNotFound(fmt.Sprintf("user %s not found", args[0]))
			}
			targetID = utils.StrVal(target, "id")
		}
		if !supportedPriorities[level] {
			return nil, errValidation("priority must be one of " + joinStrings(priorityOrder, ", "))
		}
		if targetID != principal.ID && !principal.Can(authz.ActionEdit) {
			return nil, errForbidden("changing someone else's priority requires the editor role")
		}
		user := state.Users[targetID]
		if user == nil {
			return nil, errNotFound("user not found")
		}
		if !principal.MayAccessTeam(userTeamID(state, targetID)) {
			return nil, errForbiddenTeam("users")
		}
		user["priority"] = level
		user["updated_at"] = utils.ToISO(utils.UTCNow())
		return map[string]any{
			"text": fmt.Sprintf("%s is now %s priority", utils.StrVal(user, "username"), level),
			"user": redactUserForChat(user),
		}, nil

	case "ack", "/ack":
		if len(args) == 0 {
			return nil, errValidation("ack command requires alert group id")
		}
		g, err := getGroupOrError(state, args[0])
		if err != nil {
			return nil, err
		}
		if err := guardChatopsGroupCommand(state, channel, principal, g); err != nil {
			return nil, err
		}
		ts := utils.ToISO(utils.UTCNow())
		if err := g.Acknowledge(ts, "Alert group acknowledged from ChatOps by "+chatHandle, principal); err != nil {
			return nil, errValidation(err.Error())
		}
		return map[string]any{
			"text":        "Acknowledged " + groupLabel(g) + " — " + principal.Describe(),
			"alert_group": g.Raw(),
		}, nil

	case "resolve", "/resolve":
		if len(args) == 0 {
			return nil, errValidation("resolve command requires alert group id")
		}
		g, err := getGroupOrError(state, args[0])
		if err != nil {
			return nil, err
		}
		if err := guardChatopsGroupCommand(state, channel, principal, g); err != nil {
			return nil, err
		}
		ts := utils.ToISO(utils.UTCNow())
		g.Resolve(ts, "Resolved from ChatOps by "+chatHandle, principal)
		e.notifyGroupResolved(state, g, ts)
		return map[string]any{
			"text":        "Resolved " + groupLabel(g) + " — " + principal.Describe(),
			"alert_group": g.Raw(),
		}, nil

	case "oncall", "/oncall", "whoisoncall":
		if len(args) == 0 {
			return nil, errValidation("oncall command requires schedule id")
		}
		sched := state.Schedules[args[0]]
		if sched == nil {
			return nil, errNotFound(fmt.Sprintf("schedule %s not found", args[0]))
		}
		var usernames []string
		var users []map[string]any
		for _, uid := range scheduleUserIDsFromState(state, sched) {
			if u := state.Users[uid]; u != nil {
				users = append(users, u)
				usernames = append(usernames, utils.StrVal(u, "username"))
			}
		}
		who := strings.Join(usernames, ", ")
		if who == "" {
			who = "nobody"
		}
		return map[string]any{"text": "On-call now: " + who, "users": users}, nil

	case "show", "/show":
		// Read-only, and that is the point: the listing used to acknowledge a
		// group when someone tapped a row labelled with its title. Opening and
		// acting are now two different buttons, and this is the opening one.
		if len(args) == 0 {
			return nil, errValidation("show command requires alert group id")
		}
		g, err := getGroupOrError(state, args[0])
		if err != nil {
			return nil, err
		}
		if !chatopsGroupAccess(state, channel, principal, g) {
			return nil, errForbiddenTeam("alert_groups")
		}
		return map[string]any{"text": groupCardText(g), "alert_group_card": g.Raw()}, nil

	case "silence", "/silence":
		if len(args) == 0 {
			return nil, errValidation("silence command requires alert group id")
		}
		minutes := chatopsSilenceDefaultMinutes
		if len(args) > 1 {
			n, err := strconv.Atoi(args[1])
			if err != nil || n <= 0 || n > chatopsSilenceMaxMinutes {
				return nil, errValidation(fmt.Sprintf("silence accepts 1 to %d minutes", chatopsSilenceMaxMinutes))
			}
			minutes = n
		}
		g, err := getGroupOrError(state, args[0])
		if err != nil {
			return nil, err
		}
		if err := guardChatopsGroupCommand(state, channel, principal, g); err != nil {
			return nil, err
		}
		now := utils.UTCNow()
		ts := utils.ToISO(now)
		until := utils.ToISO(now.Add(time.Duration(minutes) * time.Minute))
		if err := g.Silence(ts, until, "Alert group silenced from ChatOps by "+chatHandle, minutes, principal); err != nil {
			return nil, errValidation(err.Error())
		}
		return map[string]any{
			"text":        fmt.Sprintf("Silenced %s for %s", groupLabel(g), silenceDurationText(minutes)),
			"alert_group": g.Raw(),
		}, nil

	case "unack", "/unack", "unacknowledge":
		if len(args) == 0 {
			return nil, errValidation("unack command requires alert group id")
		}
		g, err := getGroupOrError(state, args[0])
		if err != nil {
			return nil, err
		}
		if err := guardChatopsGroupCommand(state, channel, principal, g); err != nil {
			return nil, err
		}
		if err := g.Unacknowledge(utils.ToISO(utils.UTCNow()), principal); err != nil {
			return nil, errValidation(err.Error())
		}
		return map[string]any{
			"text":        "Acknowledgement taken back, escalation resumes: " + groupLabel(g),
			"alert_group": g.Raw(),
		}, nil

	case "unresolve", "/unresolve", "reopen":
		if len(args) == 0 {
			return nil, errValidation("unresolve command requires alert group id")
		}
		g, err := getGroupOrError(state, args[0])
		if err != nil {
			return nil, err
		}
		if err := guardChatopsGroupCommand(state, channel, principal, g); err != nil {
			return nil, err
		}
		if err := g.Unresolve(utils.ToISO(utils.UTCNow()), principal); err != nil {
			return nil, errValidation(err.Error())
		}
		return map[string]any{"text": "Reopened " + groupLabel(g), "alert_group": g.Raw()}, nil

	case "bulk", "/bulk":
		return e.chatopsBulk(state, channel, principal, chatHandle, args)

	case "start", "/start":
		// First contact with the bot. It used to answer "unsupported chatops
		// command", and — because an errored command produced no reply at all —
		// in practice it answered nothing.
		return map[string]any{
			"text":                 startText(principal),
			"offer_start_keyboard": true,
		}, nil

	case "help", "/help":
		return map[string]any{"text": "Commands: alerts [page], status, show <group_id>, " +
			"ack <group_id>, resolve <group_id>, silence <group_id> [minutes], " +
			"unack <group_id>, unresolve <group_id>, bulk ack|silence|resolve [minutes], " +
			"duty on|off, duty take [schedule_id] [hours], " +
			"priority [username] <high|medium|low>, oncall <schedule_id>, " +
			"report"}, nil
	}
	// A validation error, not a plain one: a mistyped command is the caller's
	// mistake, and a plain error became HTTP 500 plus an ERROR log line on the
	// API. Chat webhooks show the text to the person either way.
	return nil, errValidation(fmt.Sprintf("unsupported chatops command: %s; send \"help\" for the list", cmd))
}

// chatopsSilenceDefaultMinutes is what a silence button without a duration
// means, and matches the web UI's own default (the "s" shortcut on a group).
const chatopsSilenceDefaultMinutes = 60

// chatopsSilenceMaxMinutes bounds a typed duration. A day is already generous;
// anything longer is a maintenance window, which is a different object with its
// own audit trail — not something to reach by mistyping a number into a chat.
const chatopsSilenceMaxMinutes = 24 * 60

// groupLabel names a group the way a channel reads it. The id is what the next
// typed command needs, but nobody recognises an incident by it, so the title
// leads and the id is the fallback for a group that has none.
func groupLabel(g model.AlertGroup) string {
	if title := strings.TrimSpace(g.Title()); title != "" {
		return "«" + title + "»"
	}
	return g.ID()
}

// silenceDurationText renders a duration the way the person who tapped the
// button thinks of it.
func silenceDurationText(minutes int) string {
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}

// groupCardText is what "show" answers: enough of the group to decide whether to
// act on it, without opening the web UI.
func groupCardText(g model.AlertGroup) string {
	lines := []string{
		fmt.Sprintf("[%s] %s", strDefault(g.Severity(), "unknown"), strDefault(g.Title(), "alert")),
		fmt.Sprintf("status: %s · alerts: %d · escalation step: %d", g.Status(), g.AlertCount(), g.CurrentStep()),
		"last seen: " + strDefault(g.LastReceivedAt(), "unknown"),
	}
	if by := actorName(g.AcknowledgedBy()); by != "" {
		lines = append(lines, "acknowledged by: "+by)
	}
	if until := utils.StrVal(g.Raw(), "silenced_until"); until != "" {
		lines = append(lines, "silenced until: "+until)
	}
	lines = append(lines, "id: "+g.ID())
	return strings.Join(lines, "\n")
}

// actorName reads the compact attribution the group stores, so a card can say
// who acted rather than only that somebody did.
func actorName(ref any) string {
	m, ok := ref.(map[string]any)
	if !ok {
		return ""
	}
	return utils.StrVal(m, "name")
}

// startText greets a first-time user of the bot and, when the account is not
// linked, says so — because every command that needs a person will otherwise
// refuse with a message that reads like a bug.
func startText(principal authz.Actor) string {
	if principal.Kind == authz.KindUser && principal.ID != "" {
		return "nxs-anomaly is connected. You can list open alert groups, act on them, " +
			"and check in for your shift. Type help for every command."
	}
	return "nxs-anomaly is connected, but this chat account is not linked to a user here, " +
		"so anything about a person — duty, priority — will be refused. Ask an administrator " +
		"to add your chat account id to your profile. Type help for every command."
}

// chatopsBulk applies one verb to every open group the caller can act on.
//
// The storm case: forty groups from one cluster failure, and a keyboard that can
// only act on them one at a time. Scoped to what this principal may already
// reach, so it is a shortcut for repetition, not a way around the boundary.
func (e *Engine) chatopsBulk(state *store.State, channel map[string]any, principal authz.Actor, chatHandle string, args []string) (map[string]any, error) {
	if len(args) == 0 || !bulkChatActions[strings.ToLower(args[0])] {
		return nil, errValidation("bulk command requires ack, silence or resolve")
	}
	verb := strings.ToLower(args[0])
	minutes := chatopsSilenceDefaultMinutes
	if verb == "silence" && len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 || n > chatopsSilenceMaxMinutes {
			return nil, errValidation(fmt.Sprintf("silence accepts 1 to %d minutes", chatopsSilenceMaxMinutes))
		}
		minutes = n
	}
	if !principal.Can(authz.ActionRespond) {
		return nil, errForbidden("acting on alerts requires the responder role")
	}
	now := utils.UTCNow()
	ts := utils.ToISO(now)
	until := utils.ToISO(now.Add(time.Duration(minutes) * time.Minute))

	// Collected and sorted before anything is written: map iteration order would
	// make the reported count reproducible but the log order arbitrary, and this
	// is the one command whose whole answer is a count.
	var ids []string
	for id, rec := range state.AlertGroups {
		g, ok := groupAG(rec)
		if !ok || g.Status() == model.StatusResolved {
			continue
		}
		if !chatopsGroupAccess(state, channel, principal, g) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var changed int
	var resolved []model.AlertGroup
	for _, id := range ids {
		g, ok := groupAG(state.AlertGroups[id])
		if !ok {
			continue
		}
		var err error
		switch verb {
		case "ack":
			err = g.Acknowledge(ts, "Alert group acknowledged from ChatOps by "+chatHandle, principal)
		case "silence":
			err = g.Silence(ts, until, "Alert group silenced from ChatOps by "+chatHandle, minutes, principal)
		case "resolve":
			g.Resolve(ts, "Resolved from ChatOps by "+chatHandle, principal)
			resolved = append(resolved, g)
		}
		// A group that refused the transition is skipped, not fatal: one group
		// that cannot be acknowledged must not stop the other thirty-nine.
		if err != nil {
			continue
		}
		changed++
	}
	// Telling whoever was paged that it is over is part of resolving, and it
	// happens after the loop so the notifications describe the set that was
	// actually written.
	for _, g := range resolved {
		e.notifyGroupResolved(state, g, ts)
	}
	what := verb
	if verb == "silence" {
		what = "silenced for " + silenceDurationText(minutes)
	}
	return map[string]any{
		"text":        fmt.Sprintf("%d of %d open alert group(s): %s", changed, len(ids), what),
		"bulk_action": verb,
		"bulk_count":  changed,
	}, nil
}

func scheduleUserIDsFromState(_ *store.State, sched map[string]any) []string {
	return getScheduleUserIDsAt(sched, utils.UTCNow())
}

// FindUserByTelegramID resolves a Telegram account id to a configured user,
// returning nil when nobody claims it.
//
// This is what lets an acknowledge from Telegram be attributed to the engineer
// who tapped rather than to the bot. The webhook's secret token proves the
// platform sent the update; the account id inside it is the only thing in that
// update tied to a person this deployment already knows.
func (e *Engine) FindUserByTelegramID(ctx context.Context, telegramID string) (map[string]any, error) {
	return e.FindUserByChatAccount(ctx, "telegram", telegramID)
}

// ChatAccountField names the profile field that carries a person's account id on
// one chat platform, or "" for a platform this deployment does not link.
//
// Identity used to be a Telegram-only question: Slack and Mattermost taps ran as
// the platform service principal, so the group's log and the audit trail said a
// bot had acknowledged, and every command that needs a person refused with a
// message telling a Mattermost user to link their Telegram account.
func ChatAccountField(platform string) string {
	switch strings.ToLower(platform) {
	case "telegram":
		return "telegram_id"
	case "slack":
		return "slack_id"
	case "mattermost":
		return "mattermost_id"
	}
	return ""
}

// FindUserByChatAccount resolves a platform account id to a configured user,
// returning nil when nobody claims it.
//
// This is what lets an acknowledge from a chat be attributed to the engineer who
// tapped rather than to the bot. The platform's own credential proves the update
// is genuine; the account id inside it is the only thing tied to a person this
// deployment already knows.
func (e *Engine) FindUserByChatAccount(ctx context.Context, platform, accountID string) (map[string]any, error) {
	field := ChatAccountField(platform)
	if field == "" || accountID == "" {
		return nil, nil
	}
	users, err := e.refCollection(ctx, "users")
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if utils.StrVal(u, field) == accountID {
			return u, nil
		}
	}
	return nil, nil
}

// FindChatopsChannelByExternalID resolves a platform channel (Slack channel id,
// Telegram chat id) to a configured ChatOps channel, returning "" when none is
// bound.
//
// Telegram channels historically stored the chat id in "name" — that is what
// the delivery adapter still sends to — so the name is accepted as a fallback
// duplicateChatopsBinding refuses a second channel bound to the same chat.
//
// The external id is what an inbound command names, so two rows carrying it are
// two answers to "which channel is this" — and the lookup below simply returns
// whichever it meets first. Unbinding the chat by deleting one row then does
// nothing, because the other still matches.
func duplicateChatopsBinding(state *store.State, selfID, platform, externalID string) error {
	if strings.TrimSpace(externalID) == "" {
		return nil
	}
	for id, ch := range state.ChatopsChannels {
		if id == selfID || !strings.EqualFold(utils.StrVal(ch, "platform"), platform) {
			continue
		}
		if utils.StrVal(ch, "external_id") == externalID {
			return &conflictError{fmt.Sprintf(
				"%s channel %s is already bound to %q", platform, externalID, utils.StrVal(ch, "name"))}
		}
	}
	return nil
}

// rather than forcing every existing installation to re-enter it.
func (e *Engine) FindChatopsChannelByExternalID(ctx context.Context, platform, externalID string) (string, error) {
	if externalID == "" {
		return "", nil
	}
	channels, err := e.refCollection(ctx, "chatops_channels")
	if err != nil {
		return "", err
	}
	var fallback string
	for id, ch := range channels {
		if !strings.EqualFold(utils.StrVal(ch, "platform"), platform) {
			continue
		}
		if utils.StrVal(ch, "external_id") == externalID {
			return id, nil
		}
		if utils.StrVal(ch, "name") == externalID {
			fallback = id
		}
	}
	return fallback, nil
}
