package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// ── Users ────────────────────────────────────────────────────────────────────

func (e *Engine) CreateUser(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"name"}); err != nil {
		return nil, errValidation(err.Error())
	}
	ts := utils.ToISO(utils.UTCNow())
	username := utils.StrVal(payload, "username")
	if username == "" {
		username = strings.ToLower(strings.ReplaceAll(fmt.Sprintf("%v", payload["name"]), " ", "."))
	}
	priority, err := sanitizePriority(payload["priority"])
	if err != nil {
		return nil, err
	}
	targets, err := sanitizeNotificationTargets(payload["notification_targets"], e.deliveryCfg.Channels)
	if err != nil {
		return nil, err
	}
	policies, err := sanitizeNotificationPolicies(payload["notification_policies"], e.deliveryCfg.Channels)
	if err != nil {
		return nil, err
	}
	role, err := sanitizeRole(payload["role"])
	if err != nil {
		return nil, err
	}
	locale, err := SanitizeLocale(payload["locale"])
	if err != nil {
		return nil, err
	}
	user := map[string]any{
		"id":          utils.MakeID("usr"),
		"name":        fmt.Sprintf("%v", payload["name"]),
		"username":    username,
		"email":       utils.StrVal(payload, "email"),
		"phone":       utils.StrVal(payload, "phone"),
		"telegram_id": utils.StrVal(payload, "telegram_id"),
		"timezone":    strDefault(utils.StrVal(payload, "timezone"), "UTC"),
		// Empty means "no choice recorded": the UI then follows the browser
		// rather than inventing a language for somebody who never picked one.
		// A default of ru-RU here would be a claim about a person, not about
		// the deployment, and it would overwrite what their browser asks for.
		"locale":               locale,
		"on_duty":              utils.BoolVal(payload, "on_duty", false),
		"priority":             priority,
		"notification_targets": targets,
		// Personal notification policies (BETA-031). Omitted from the map when
		// nil so a user without one stays on the legacy target-blast path.
		"notification_policies": policies,
		// Users are both on-call roster entries and (from this release) auth
		// principals. Someone who should be paged but must not log in is created
		// without a role, which is the default.
		"role":       role,
		"created_at": ts,
		"updated_at": ts,
	}
	result, err := e.store.UpdateCollections(ctx, nil, []string{"users"},
		func(state *store.State) (any, error) {
			stampProvisioner(ctx, user)
			state.Users[user["id"].(string)] = user
			e.auditIn(state, ctx, AuditCreate, "user", user["id"].(string), auditFields(payload, nil))
			return user, nil
		}, advisoryLock["create_user"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) UpdateUser(ctx context.Context, userID string, payload map[string]any) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	// oidc_subject links a local user to an identity-provider account. It is
	// written by the SSO callback, never by a person: the API accepts it here
	// only because that callback goes through UpdateUser like everything else.
	updatable := map[string]bool{"name": true, "username": true, "email": true, "phone": true, "timezone": true,
		"telegram_id": true, "role": true, "oidc_subject": true, "locale": true}

	if _, ok := payload["role"]; ok {
		if _, err := sanitizeRole(payload["role"]); err != nil {
			return nil, err
		}
	}
	if _, ok := payload["locale"]; ok {
		if _, err := SanitizeLocale(payload["locale"]); err != nil {
			return nil, err
		}
	}

	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("users", userID), []string{"users"},
		func(state *store.State) (any, error) {
			user := state.Users[userID]
			if user == nil {
				return nil, errNotFound(fmt.Sprintf("user %s not found", userID))
			}
			if err := guardProvisioned(ctx, "users", user); err != nil {
				return nil, err
			}
			for key := range updatable {
				if v, ok := payload[key]; ok {
					user[key] = fmt.Sprintf("%v", v)
				}
			}
			if v, ok := payload["priority"]; ok {
				p, err := sanitizePriority(v)
				if err != nil {
					return nil, err
				}
				user["priority"] = p
			}
			if v, ok := payload["notification_targets"]; ok {
				targets, err := sanitizeNotificationTargets(v, e.deliveryCfg.Channels)
				if err != nil {
					return nil, err
				}
				user["notification_targets"] = targets
			}
			if v, ok := payload["notification_policies"]; ok {
				policies, err := sanitizeNotificationPolicies(v, e.deliveryCfg.Channels)
				if err != nil {
					return nil, err
				}
				user["notification_policies"] = policies
			}
			if v, ok := payload["on_duty"]; ok {
				user["on_duty"] = utils.BoolVal(map[string]any{"v": v}, "v", false)
			}
			user["updated_at"] = ts
			e.auditIn(state, ctx, AuditUpdate, "user", userID, auditFields(payload, nil))
			return user, nil
		}, advisoryLock["update_user"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) ToggleUserDuty(ctx context.Context, userID string, onDuty bool) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("users", userID), []string{"users"},
		func(state *store.State) (any, error) {
			user := state.Users[userID]
			if user == nil {
				return nil, errNotFound(fmt.Sprintf("user %s not found", userID))
			}
			user["on_duty"] = onDuty
			user["updated_at"] = ts
			e.auditIn(state, ctx, AuditUpdate, "user", userID, map[string]any{"on_duty": onDuty})
			return user, nil
		}, advisoryLock["toggle_user_duty"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

// ── Teams ─────────────────────────────────────────────────────────────────────

func (e *Engine) CreateTeam(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"name"}); err != nil {
		return nil, errValidation(err.Error())
	}
	memberIDs, err := utils.CoerceStringList(payload["member_ids"])
	if err != nil {
		return nil, err
	}
	if err := e.ensureUsersExist(ctx, memberIDs); err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())

	result, err := e.store.UpdateCollections(ctx, nil, []string{"teams"},
		func(state *store.State) (any, error) {
			team := map[string]any{
				"id":         utils.MakeID("team"),
				"name":       fmt.Sprintf("%v", payload["name"]),
				"member_ids": toAnySlice(memberIDs),
				"created_at": ts,
				"updated_at": ts,
			}
			stampProvisioner(ctx, team)
			state.Teams[team["id"].(string)] = team
			e.auditIn(state, ctx, AuditCreate, "team", team["id"].(string), auditFields(payload, nil))
			return team, nil
		}, advisoryLock["create_team"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) UpdateTeam(ctx context.Context, teamID string, payload map[string]any) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	var memberIDs []string
	if v, ok := payload["member_ids"]; ok {
		ids, err := utils.CoerceStringList(v)
		if err != nil {
			return nil, err
		}
		memberIDs = ids
		if err := e.ensureUsersExist(ctx, memberIDs); err != nil {
			return nil, err
		}
	}

	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("teams", teamID), []string{"teams"},
		func(state *store.State) (any, error) {
			team := state.Teams[teamID]
			if team == nil {
				return nil, errNotFound(fmt.Sprintf("team %s not found", teamID))
			}
			if err := guardProvisioned(ctx, "teams", team); err != nil {
				return nil, err
			}
			if v, ok := payload["name"]; ok {
				team["name"] = fmt.Sprintf("%v", v)
			}
			if memberIDs != nil {
				team["member_ids"] = toAnySlice(memberIDs)
			}
			team["updated_at"] = ts
			e.auditIn(state, ctx, AuditUpdate, "team", teamID, auditFields(payload, nil))
			return team, nil
		}, advisoryLock["update_team"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

// ── Schedules ─────────────────────────────────────────────────────────────────

func (e *Engine) CreateSchedule(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"name"}); err != nil {
		return nil, errValidation(err.Error())
	}
	ts := utils.ToISO(utils.UTCNow())
	shifts, err := e.parseShifts(ctx, payload["shifts"])
	if err != nil {
		return nil, err
	}
	timezone, err := parseScheduleTimezone(payload, "UTC")
	if err != nil {
		return nil, err
	}
	rot, err := e.parseScheduleRotation(ctx, payload["rotation"])
	if err != nil {
		return nil, err
	}
	teamID := nilIfEmpty(utils.StrVal(payload, "team_id"))
	if teamID != nil {
		if err := e.ensureTeamsExist(ctx, []string{teamID.(string)}); err != nil {
			return nil, err
		}
		if !authz.FromContext(ctx).MayAccessTeam(teamID.(string)) {
			return nil, errForbiddenTeam("schedules")
		}
	}

	result, err := e.store.UpdateCollections(ctx, nil, []string{"schedules"},
		func(state *store.State) (any, error) {
			sched := map[string]any{
				"id":       utils.MakeID("sch"),
				"name":     fmt.Sprintf("%v", payload["name"]),
				"timezone": timezone,
				"enabled":  utils.BoolVal(payload, "enabled", true),
				// Opt-in: enabling shift notifications for an existing
				// deployment is a decision about messaging people, not a
				// default.
				"notify_on_shift_change": utils.BoolVal(payload, "notify_on_shift_change", false),
				"team_id":                teamID,
				"rotation":               rotationJSON(rot),
				"shifts":                 shifts,
				"overrides":              []any{},
				"created_at":             ts,
				"updated_at":             ts,
			}
			stampProvisioner(ctx, sched)
			state.Schedules[sched["id"].(string)] = sched
			e.auditIn(state, ctx, AuditCreate, "schedule", sched["id"].(string), auditFields(payload, nil))
			return sched, nil
		}, advisoryLock["create_schedule"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) UpdateSchedule(ctx context.Context, schedID string, payload map[string]any) (map[string]any, error) {
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
			if !authz.FromContext(ctx).MayAccessTeam(teamID.(string)) {
				return nil, errForbiddenTeam("schedules")
			}
		}
	}
	var shifts []any
	shiftsSet := false
	if v, ok := payload["shifts"]; ok {
		var err error
		shiftsSet = true
		shifts, err = e.parseShifts(ctx, v)
		if err != nil {
			return nil, err
		}
	}
	var timezone string
	if _, ok := payload["timezone"]; ok {
		var err error
		timezone, err = parseScheduleTimezone(payload, "UTC")
		if err != nil {
			return nil, err
		}
	}
	var rotationValue any
	rotationSet := false
	if v, ok := payload["rotation"]; ok {
		rotationSet = true
		if v == nil {
			rotationValue = nil
		} else {
			rot, err := e.parseScheduleRotation(ctx, v)
			if err != nil {
				return nil, err
			}
			rotationValue = rotationJSON(rot)
		}
	}

	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("schedules", schedID), []string{"schedules"},
		func(state *store.State) (any, error) {
			sched := state.Schedules[schedID]
			if sched == nil {
				return nil, errNotFound(fmt.Sprintf("schedule %s not found", schedID))
			}
			if err := e.authorizeItem(ctx, "schedules", sched); err != nil {
				return nil, err
			}
			// The schedule's definition is guarded; its overrides are not. An
			// override is somebody covering a shift tonight, which is what the
			// rota exists for and which Terraform does not describe.
			if err := guardProvisioned(ctx, "schedules", sched); err != nil {
				return nil, err
			}
			if v, ok := payload["name"]; ok {
				sched["name"] = fmt.Sprintf("%v", v)
			}
			if timezone != "" {
				sched["timezone"] = timezone
			}
			if v, ok := payload["enabled"]; ok {
				sched["enabled"] = utils.BoolVal(map[string]any{"enabled": v}, "enabled", true)
			}
			if v, ok := payload["notify_on_shift_change"]; ok {
				sched["notify_on_shift_change"] = utils.BoolVal(
					map[string]any{"v": v}, "v", false)
			}
			if teamIDSet {
				sched["team_id"] = teamID
			}
			if rotationSet {
				sched["rotation"] = rotationValue
			}
			if shiftsSet {
				sched["shifts"] = shifts
			}
			sched["updated_at"] = ts
			e.auditIn(state, ctx, AuditUpdate, "schedule", schedID, auditFields(payload, nil))
			return sched, nil
		}, advisoryLock["update_schedule"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

// appendScheduleOverride writes one override onto a schedule and returns it.
//
// Shared by the REST path and the chat one so an override taken during an
// incident is the same object, with the same actor trail, as one entered in the
// web interface — the two must not drift, because the schedule engine gives
// overrides the highest priority and cannot tell them apart.
func appendScheduleOverride(ctx context.Context, sched map[string]any, userID string, startAt, until time.Time, reason, ts string) map[string]any {
	override := map[string]any{
		"id":         utils.MakeID("ovr"),
		"user_id":    userID,
		"start_at":   utils.ToISO(startAt),
		"until":      utils.ToISO(until),
		"reason":     reason,
		"created_at": ts,
		"created_by": actorRef(ctx),
	}
	overrides, _ := sched["overrides"].([]any)
	sched["overrides"] = append(overrides, override)
	sched["updated_at"] = ts
	return override
}

func (e *Engine) CreateScheduleOverride(ctx context.Context, schedID string, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"user_id", "until"}); err != nil {
		return nil, errValidation(err.Error())
	}
	ts := utils.ToISO(utils.UTCNow())
	userID := utils.StrVal(payload, "user_id")
	if err := e.ensureUsersExist(ctx, []string{userID}); err != nil {
		return nil, err
	}

	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("schedules", schedID), []string{"schedules"},
		func(state *store.State) (any, error) {
			sched := state.Schedules[schedID]
			if sched == nil {
				return nil, errNotFound(fmt.Sprintf("schedule %s not found", schedID))
			}
			if err := e.authorizeItem(ctx, "schedules", sched); err != nil {
				return nil, err
			}
			until, err := utils.ParseDatetime(utils.StrVal(payload, "until"))
			if err != nil {
				return nil, fmt.Errorf("invalid until: %w", err)
			}
			startAt := utils.UTCNow()
			if v := utils.StrVal(payload, "start_at"); v != "" {
				startAt, err = utils.ParseDatetime(v)
				if err != nil {
					return nil, fmt.Errorf("invalid start_at: %w", err)
				}
			}
			if !until.After(startAt) {
				return nil, fmt.Errorf("until must be after start_at")
			}
			override := appendScheduleOverride(ctx, sched,
				userID, startAt, until, utils.StrVal(payload, "reason"), ts)
			// Recorded against the schedule, not the override: an override has no
			// independent life, and "what happened to this schedule" is the
			// question the trail is asked.
			e.auditIn(state, ctx, AuditUpdate, "schedule", schedID, map[string]any{
				"override_for_user": userID,
				"until":             utils.StrVal(payload, "until"),
				"reason":            utils.StrVal(payload, "reason"),
			})
			return map[string]any{"schedule": sched, "override": override}, nil
		}, advisoryLock["create_schedule_override"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) GetScheduleOncall(ctx context.Context, schedID string, atStr string) (map[string]any, error) {
	state, err := e.store.ReadCollections(ctx, []string{"schedules", "users"})
	if err != nil {
		return nil, err
	}
	sched := state.Schedules[schedID]
	if sched == nil {
		return nil, errNotFound(fmt.Sprintf("schedule %s not found", schedID))
	}
	// Reading who is on call is reading the schedule, so it obeys the same team
	// scoping as fetching the schedule itself.
	if err := e.authorizeItem(ctx, "schedules", sched); err != nil {
		return nil, err
	}
	at := utils.UTCNow()
	if atStr != "" {
		t, err := utils.ParseDatetime(atStr)
		if err == nil {
			at = t
		}
	}
	userIDs, source := resolveScheduleAt(sched, at)
	var users []map[string]any
	for _, uid := range userIDs {
		if u := state.Users[uid]; u != nil {
			users = append(users, u)
		}
	}
	out := map[string]any{
		"schedule_id": schedID,
		"at":          utils.ToISO(at),
		"user_ids":    userIDs,
		"users":       users,
		"source":      source,
	}
	// "who is next" is the other half of the question the UI asks, and the
	// timeline already has the answer: the first segment after now with a
	// different roster.
	for _, seg := range scheduleTimeline(sched, at, at.Add(defaultPreviewWindow)) {
		if seg.Start.After(at) && !sameIDs(seg.UserIDs, userIDs) {
			out["next"] = segmentJSON(seg)
			break
		}
	}
	return out, nil
}

// CurrentlyOnCall returns who is on call right now across every enabled
// schedule, resolved through Schedule v2 (rotation, overrides, restriction
// window) rather than the manual on_duty flag. A schedule slot naming a deleted
// user is reported with the raw id, so a coverage hole shows as one. Team-scoped
// like the schedule list: schedules the caller may not see are skipped.
func (e *Engine) CurrentlyOnCall(ctx context.Context) (map[string]any, error) {
	state, err := e.store.ReadCollections(ctx, []string{"schedules", "users"})
	if err != nil {
		return nil, err
	}
	at := utils.UTCNow()
	items := []map[string]any{}
	seen := map[string]bool{}
	for _, sched := range state.Schedules {
		if !utils.BoolVal(sched, "enabled", true) {
			continue
		}
		if err := e.authorizeItem(ctx, "schedules", sched); err != nil {
			continue // out of the caller's team scope
		}
		userIDs, source := resolveScheduleAt(sched, at)
		schedID := utils.StrVal(sched, "id")
		for _, uid := range userIDs {
			key := uid + ":" + schedID
			if seen[key] {
				continue
			}
			seen[key] = true
			u := state.Users[uid]
			items = append(items, map[string]any{
				"user_id":       uid,
				"name":          strDefault(utils.StrVal(u, "name"), uid),
				"username":      utils.StrVal(u, "username"),
				"exists":        u != nil,
				"schedule_id":   schedID,
				"schedule_name": utils.StrVal(sched, "name"),
				"source":        source,
			})
		}
	}
	return map[string]any{"items": items, "at": utils.ToISO(at)}, nil
}

func (e *Engine) parseShifts(ctx context.Context, rawShifts any) ([]any, error) {
	list, _ := rawShifts.([]any)
	var shifts []any
	for _, raw := range list {
		s, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("shift must be an object")
		}
		if err := utils.EnsureRequired(s, []string{"user_id", "start_at", "end_at"}); err != nil {
			return nil, err
		}
		userID := utils.StrVal(s, "user_id")
		if err := e.ensureUsersExist(ctx, []string{userID}); err != nil {
			return nil, err
		}
		startAt, err := utils.ParseDatetime(utils.StrVal(s, "start_at"))
		if err != nil {
			return nil, fmt.Errorf("invalid start_at: %w", err)
		}
		endAt, err := utils.ParseDatetime(utils.StrVal(s, "end_at"))
		if err != nil {
			return nil, fmt.Errorf("invalid end_at: %w", err)
		}
		if !endAt.After(startAt) {
			return nil, fmt.Errorf("shift end_at must be greater than start_at")
		}
		recurrence := strings.ToLower(utils.StrVal(s, "recurrence"))
		if recurrence == "" {
			recurrence = "none"
		}
		if !supportedShiftRecurrences[recurrence] {
			return nil, fmt.Errorf("unsupported shift recurrence: %s", recurrence)
		}
		shiftID := utils.StrVal(s, "id")
		if shiftID == "" {
			shiftID = utils.MakeID("shift")
		}
		shifts = append(shifts, map[string]any{
			"id":         shiftID,
			"user_id":    userID,
			"start_at":   utils.ToISO(startAt),
			"end_at":     utils.ToISO(endAt),
			"recurrence": recurrence,
		})
	}
	return shifts, nil
}

// sanitizeRole validates an optional user role. An absent or empty role means
// "no access": a roster entry that can be paged but cannot sign in. An
// unrecognised role is rejected rather than silently downgraded, because unlike
// a deployment-time API key this value is set through the API, where a typo
// should be reported to the caller.
func sanitizeRole(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	s := strings.TrimSpace(fmt.Sprintf("%v", raw))
	if s == "" {
		return "", nil
	}
	role := authz.ParseRole(s)
	if !role.Valid() {
		return "", errValidation(fmt.Sprintf("role must be one of admin, editor, responder, viewer (got %q)", s))
	}
	return role.String(), nil
}
