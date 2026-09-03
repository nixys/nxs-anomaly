package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// ── Mobile Devices & Sessions ─────────────────────────────────────────────────

func (e *Engine) RegisterMobileDevice(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"user_id", "platform", "push_token"}); err != nil {
		return nil, errValidation(err.Error())
	}
	if err := rejectInlineSecret("push_token", utils.StrVal(payload, "push_token")); err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	userID := utils.StrVal(payload, "user_id")
	if err := e.ensureUsersExist(ctx, []string{userID}); err != nil {
		return nil, err
	}
	result, err := e.store.UpdateCollections(ctx, nil, []string{"mobile_devices"},
		func(state *store.State) (any, error) {
			device := map[string]any{
				"id":          utils.MakeID("mdev"),
				"user_id":     userID,
				"device_id":   utils.MakeID("devid"),
				"platform":    strings.ToLower(utils.StrVal(payload, "platform")),
				"push_token":  utils.StrVal(payload, "push_token"),
				"device_name": utils.StrVal(payload, "device_name"),
				"active":      true,
				"created_at":  ts,
				"updated_at":  ts,
			}
			state.MobileDevices[device["id"].(string)] = device
			// The push token is a credential; only the fields it was registered
			// with and the owner are recorded.
			e.auditIn(state, ctx, AuditCreate, "mobile_device", utils.StrVal(device, "id"),
				map[string]any{"user_id": utils.StrVal(device, "user_id"), "platform": utils.StrVal(device, "platform")})
			return device, nil
		}, advisoryLock["register_mobile_device"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) CreateMobileSession(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"user_id", "device_id"}); err != nil {
		return nil, errValidation(err.Error())
	}
	ts := utils.ToISO(utils.UTCNow())
	userID := utils.StrVal(payload, "user_id")
	if err := e.ensureUsersExist(ctx, []string{userID}); err != nil {
		return nil, err
	}
	deviceID := utils.StrVal(payload, "device_id")
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("mobile_devices", deviceID), []string{"mobile_sessions"},
		func(state *store.State) (any, error) {
			device := state.MobileDevices[deviceID]
			if device == nil {
				return nil, errNotFound(fmt.Sprintf("mobile device %s not found", deviceID))
			}
			if utils.StrVal(device, "user_id") != userID {
				return nil, fmt.Errorf("device does not belong to user")
			}
			session := map[string]any{
				"id":         utils.MakeID("msess"),
				"token":      utils.MakeID("mtok"),
				"user_id":    userID,
				"device_id":  deviceID,
				"is_active":  true,
				"created_at": ts,
				"updated_at": ts,
				"revoked_at": nil,
			}
			state.MobileSessions[session["id"].(string)] = session
			return session, nil
		}, advisoryLock["create_mobile_session"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) GetMobileDashboard(ctx context.Context, sessionToken string) (map[string]any, error) {
	session, err := e.store.FindMobileSessionByToken(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errNotFound("mobile session not found")
	}
	userID := utils.StrVal(session, "user_id")

	// Point reads instead of full-collection loads: the user by id, the
	// user's notifications via the typed user_id index, unresolved groups via
	// the typed status index (status values are a closed set, so IN
	// (open, acknowledged, silenced) ≡ != resolved), and the reference
	// collections from the short-TTL cache.
	user, err := e.store.GetItem(ctx, "users", userID)
	if err != nil {
		return nil, err
	}
	userNotifs, err := e.store.ListItemsIn(ctx, "notifications", "user_id", []any{userID})
	if err != nil {
		return nil, err
	}
	unresolvedGroups, err := e.store.ListItemsIn(ctx, "alert_groups", "status",
		[]any{"open", "acknowledged", "silenced"})
	if err != nil {
		return nil, err
	}
	chainsMap, err := e.refCollection(ctx, "escalation_chains")
	if err != nil {
		return nil, err
	}
	teamsMap, err := e.refCollection(ctx, "teams")
	if err != nil {
		return nil, err
	}
	schedsMap, err := e.refCollection(ctx, "schedules")
	if err != nil {
		return nil, err
	}
	state := store.NewState()
	state.EscalationChains = chainsMap
	state.Teams = teamsMap
	state.Schedules = schedsMap

	// Pre-build set of group IDs that have a notification for this user (O(M) once).
	userGroupSet := make(map[string]bool, len(userNotifs))
	for _, n := range userNotifs {
		userGroupSet[utils.StrVal(n, "alert_group_id")] = true
	}

	now := utils.UTCNow()
	var activeGroups []map[string]any
	for _, group := range unresolvedGroups {
		if groupIsRelevantToUser(state, group, userID, userGroupSet, now) {
			activeGroups = append(activeGroups, group)
		}
	}
	var oncall []map[string]any
	for _, sched := range schedsMap {
		for _, uid := range e.getScheduleUserIDs(sched, now) {
			if uid == userID {
				oncall = append(oncall, map[string]any{
					"schedule_id":   utils.StrVal(sched, "id"),
					"schedule_name": utils.StrVal(sched, "name"),
					"at":            utils.ToISO(now),
				})
				break
			}
		}
	}
	return map[string]any{
		"session":               session,
		"user":                  user,
		"assigned_alert_groups": activeGroups,
		"on_call":               oncall,
	}, nil
}

// groupIsRelevantToUser returns true if this group is assigned to or affects userID.
// userGroupSet is a pre-built set of group IDs that have at least one notification for userID.
// now is when the schedule steps are evaluated: relevance follows who is on
// call, which is a question about an instant.
func groupIsRelevantToUser(state *store.State, group map[string]any, userID string, userGroupSet map[string]bool, now time.Time) bool {
	g := model.WrapAlertGroup(group)
	if userGroupSet[g.ID()] {
		return true
	}
	chainID := g.EscalationChainID()
	chain := state.EscalationChains[chainID]
	if chain == nil {
		return false
	}
	steps, _ := chain["steps"].([]any)
	for _, s := range steps {
		step, ok := s.(map[string]any)
		if !ok {
			continue
		}
		switch step["kind"] {
		case StepNotifyUser:
			for _, uid := range anyToStringSlice(step["user_ids"]) {
				if uid == userID {
					return true
				}
			}
		case StepNotifyTeam:
			team := state.Teams[utils.StrVal(step, "team_id")]
			if team != nil {
				for _, uid := range anyToStringSlice(team["member_ids"]) {
					if uid == userID {
						return true
					}
				}
			}
		case StepNotifySchedule:
			// Through the resolver, not the raw shift list: a user on call via
			// the rotation is just as much a recipient of this group.
			sched := state.Schedules[utils.StrVal(step, "schedule_id")]
			for _, uid := range getScheduleUserIDsAt(sched, now) {
				if uid == userID {
					return true
				}
			}
		}
	}
	return false
}

// MobileAcknowledgeGroup acknowledges a group via mobile session token.
//
// The session identifies a user, so the transition is attributed to them rather
// than to whatever principal authenticated the HTTP call.
func (e *Engine) MobileAcknowledgeGroup(ctx context.Context, sessionToken, groupID string) (map[string]any, error) {
	ctx, err := e.mobileActorContext(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	return e.AcknowledgeGroup(ctx, groupID)
}

// MobileResolveGroup resolves a group via mobile session token.
func (e *Engine) MobileResolveGroup(ctx context.Context, sessionToken, groupID string) (map[string]any, error) {
	ctx, err := e.mobileActorContext(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	return e.ResolveGroup(ctx, groupID)
}

// mobileActorContext validates the session and returns a context carrying the
// session's user as the actor.
//
// Before this existed the session was validated and its user_id thrown away, so
// mobile acknowledgements were indistinguishable from anonymous ones. A mobile
// session grants RoleResponder: it can act on alerts and nothing else.
func (e *Engine) mobileActorContext(ctx context.Context, sessionToken string) (context.Context, error) {
	session, err := e.validateMobileSession(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	userID := utils.StrVal(session, "user_id")
	name := userID
	if user, err := e.store.GetItem(ctx, "users", userID); err == nil && user != nil {
		if u := utils.StrVal(user, "username"); u != "" {
			name = u
		}
	}
	return authz.NewContext(ctx, authz.Actor{
		ID:          userID,
		Kind:        authz.KindUser,
		DisplayName: name,
		Role:        authz.RoleResponder,
	}), nil
}

func (e *Engine) validateMobileSession(ctx context.Context, token string) (map[string]any, error) {
	session, err := e.store.FindMobileSessionByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errNotFound("mobile session not found")
	}
	return session, nil
}

// SendTestPush pushes a test notification to every active device of a user and
// reports what actually happened, per device.
//
// It exists because the plugin's "send test push" button used to answer 200
// with an empty body no matter what — the same lie the delivery pipeline told
// before BETA-032, in the one place whose entire purpose is to prove that push
// works. This runs the real relay call and returns its verdict.
func (e *Engine) SendTestPush(ctx context.Context, userID string) (map[string]any, error) {
	user, err := e.store.GetItem(ctx, "users", userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errNotFound(fmt.Sprintf("user %s not found", userID))
	}
	devices, err := e.store.ListItemsIn(ctx, "mobile_devices", "user_id", []any{userID})
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"title":    "nxs-anomaly test push",
		"severity": "info",
		"reason":   "test push requested from the UI",
	}
	results := make([]any, 0, len(devices))
	deliveredCount := 0
	for _, device := range devices {
		if !utils.BoolVal(device, "active", true) {
			continue
		}
		deviceID := utils.StrVal(device, "id")
		res := e.deliverMobilePush(ctx, deviceID, payload)
		if res.Status == deliveryDelivered {
			deliveredCount++
		}
		entry := map[string]any{
			"device_id":       deviceID,
			"platform":        utils.StrVal(device, "platform"),
			"status":          res.Status,
			"provider_status": res.ProviderStatus,
		}
		if res.Code > 0 {
			entry["provider_code"] = res.Code
		}
		if res.Err != "" {
			entry["error"] = res.Err
		}
		results = append(results, entry)
	}
	return map[string]any{
		"user_id":   userID,
		"devices":   results,
		"delivered": deliveredCount,
		// Stated explicitly so a caller that ignores the per-device detail
		// still cannot read this as success.
		"push_configured": e.deliveryCfg.MobilePushURL != "",
	}, nil
}
