package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
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

// MobileSessionTTL is how long a mobile session lives without being used. Use
// extends it (see AuthenticateMobileSession), so a phone that is opened at
// least once a month stays signed in; one left in a drawer does not.
const MobileSessionTTL = 30 * 24 * time.Hour

// MobileTokenPrefix marks a bearer token as a mobile session rather than an API
// key, so authentication knows which of the two to look up.
const MobileTokenPrefix = "nxm_"

// mobilePairingTTL bounds how long a pairing code shown on screen stays usable.
const mobilePairingTTL = 5 * time.Minute

// ErrPairingCodeInvalid is returned for a pairing code that is unknown, already
// used or expired. The three are not told apart: saying which would tell
// someone guessing codes that they found a real one.
var ErrPairingCodeInvalid = errors.New("pairing code is invalid or expired")

// pairingAlphabet is Crockford's base32: no I, L, O or U, so a code read off a
// screen and typed by hand cannot be mistyped into a different valid code.
const pairingAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newPairingCode returns 10 symbols (50 bits), shown as XXXXX-XXXXX. That is
// short enough to type, and with a five-minute life, one use and the sign-in
// rate limit on redemption it is not guessable.
func newPairingCode() (string, error) {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = pairingAlphabet[b&31]
	}
	return string(buf[:5]) + "-" + string(buf[5:]), nil
}

// normalizePairingCode maps what a person typed onto the canonical code:
// case, spaces and dashes do not matter, and the letters Crockford excludes
// are read as the digits they look like.
func normalizePairingCode(code string) string {
	code = strings.ToUpper(code)
	code = strings.NewReplacer("-", "", " ", "", "O", "0", "I", "1", "L", "1").Replace(code)
	if len(code) != 10 {
		return code
	}
	return code[:5] + "-" + code[5:]
}

func newMobileSessionToken() (string, error) {
	token, err := authz.NewSessionToken()
	if err != nil {
		return "", err
	}
	return MobileTokenPrefix + token, nil
}

// newMobileSession builds a session record and returns it with its token. Only
// the token's hash is stored; the token itself exists in the response to the
// caller who is issued it and nowhere else.
func newMobileSession(userID, deviceID string, now time.Time) (map[string]any, string, error) {
	token, err := newMobileSessionToken()
	if err != nil {
		return nil, "", err
	}
	ts := utils.ToISO(now)
	return map[string]any{
		"id":         utils.MakeID("msess"),
		"token":      authz.HashSessionToken(token),
		"user_id":    userID,
		"device_id":  deviceID,
		"is_active":  true,
		"created_at": ts,
		"updated_at": ts,
		"expires_at": utils.ToISO(now.Add(MobileSessionTTL)),
		"revoked_at": nil,
	}, token, nil
}

// issuedSession is what the caller who receives a session sees: the record
// with the plaintext token in place of the stored hash.
func issuedSession(session map[string]any, token string) map[string]any {
	out := make(map[string]any, len(session))
	for k, v := range session {
		out[k] = v
	}
	out["token"] = token
	return out
}

// CreateMobileSession issues a session for a registered device. It is the
// administrative path (and what seed-demo uses); a person pairs their own
// phone through CreateMobilePairing / RedeemMobilePairing instead.
func (e *Engine) CreateMobileSession(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"user_id", "device_id"}); err != nil {
		return nil, errValidation(err.Error())
	}
	userID := utils.StrVal(payload, "user_id")
	if err := e.ensureUsersExist(ctx, []string{userID}); err != nil {
		return nil, err
	}
	deviceID := utils.StrVal(payload, "device_id")
	session, token, err := newMobileSession(userID, deviceID, utils.UTCNow())
	if err != nil {
		return nil, err
	}
	_, err = e.store.UpdateCollectionsFiltered(ctx, loadItems("mobile_devices", deviceID), []string{"mobile_sessions"},
		func(state *store.State) (any, error) {
			device := state.MobileDevices[deviceID]
			if device == nil {
				return nil, errNotFound(fmt.Sprintf("mobile device %s not found", deviceID))
			}
			if utils.StrVal(device, "user_id") != userID {
				return nil, fmt.Errorf("device does not belong to user")
			}
			state.MobileSessions[session["id"].(string)] = session
			return nil, nil
		}, advisoryLock["create_mobile_session"])
	if err != nil {
		return nil, err
	}
	return issuedSession(session, token), nil
}

// CreateMobilePairing issues a one-time code with which the calling person
// signs a phone in as themselves. The web UI shows it as a QR code.
func (e *Engine) CreateMobilePairing(ctx context.Context) (map[string]any, error) {
	actor := authz.FromContext(ctx)
	if actor.Kind != authz.KindUser || actor.ID == "" {
		return nil, errForbidden("only a signed-in user can pair a phone")
	}
	code, err := newPairingCode()
	if err != nil {
		return nil, err
	}
	expiresAt := utils.UTCNow().Add(mobilePairingTTL)
	if err := e.store.CreateMobilePairingCode(ctx, authz.HashSessionToken(code), actor.ID, expiresAt); err != nil {
		return nil, err
	}
	return map[string]any{
		"code":       code,
		"expires_at": utils.ToISO(expiresAt),
		// Empty when NXS_ANOMALY_PUBLIC_URL is not set; the UI then uses the
		// address it was opened on, which is the one the phone needs anyway.
		"server_url": e.PublicURL(),
	}, nil
}

// RedeemMobilePairing exchanges a pairing code for a device and a session. It
// is called without authentication — the code is the credential — so the
// caller must rate-limit it.
func (e *Engine) RedeemMobilePairing(ctx context.Context, payload map[string]any) (map[string]any, error) {
	code := normalizePairingCode(utils.StrVal(payload, "code"))
	platform := strings.ToLower(strings.TrimSpace(utils.StrVal(payload, "platform")))
	if code == "" || platform == "" {
		return nil, errValidation("code and platform are required")
	}
	userID, err := e.store.RedeemMobilePairingCode(ctx, authz.HashSessionToken(code))
	if err != nil {
		return nil, err
	}
	if userID == "" {
		return nil, ErrPairingCodeInvalid
	}
	user, err := e.store.GetItem(ctx, "users", userID)
	if err != nil {
		return nil, err
	}
	// The person may have been deleted or lost their role in the five minutes
	// the code was on screen.
	if user == nil || !authz.ParseRole(utils.StrVal(user, "role")).Valid() {
		return nil, ErrPairingCodeInvalid
	}
	now := utils.UTCNow()
	ts := utils.ToISO(now)
	device := map[string]any{
		"id":        utils.MakeID("mdev"),
		"user_id":   userID,
		"device_id": utils.MakeID("devid"),
		"platform":  platform,
		// Optional: the app has no push channel yet, and a device without one
		// is still a place the person is signed in.
		"push_token":  utils.StrVal(payload, "push_token"),
		"device_name": utils.StrVal(payload, "device_name"),
		"active":      true,
		"created_at":  ts,
		"updated_at":  ts,
	}
	session, token, err := newMobileSession(userID, utils.StrVal(device, "id"), now)
	if err != nil {
		return nil, err
	}
	actorCtx := authz.NewContext(ctx, authz.Actor{
		ID: userID, Kind: authz.KindUser, DisplayName: utils.StrVal(user, "username"),
		Role: authz.ParseRole(utils.StrVal(user, "role")),
	})
	_, err = e.store.UpdateCollections(actorCtx, nil, []string{"mobile_devices", "mobile_sessions"},
		func(state *store.State) (any, error) {
			state.MobileDevices[utils.StrVal(device, "id")] = device
			state.MobileSessions[utils.StrVal(session, "id")] = session
			e.auditIn(state, actorCtx, AuditCreate, "mobile_device", utils.StrVal(device, "id"),
				map[string]any{"user_id": userID, "platform": platform, "via": "pairing"})
			return nil, nil
		}, advisoryLock["register_mobile_device"])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"token":      token,
		"expires_at": session["expires_at"],
		"session_id": session["id"],
		"device_id":  device["id"],
		"user": map[string]any{
			"id":       userID,
			"name":     utils.StrVal(user, "name"),
			"username": utils.StrVal(user, "username"),
		},
	}, nil
}

// AuthenticateMobileSession resolves a mobile session token to its session and
// the current user record, or (nil, nil) when the token is not a live session.
//
// The user is re-read on every request, as for web sessions, so a deleted user
// or a withdrawn role takes effect immediately. A session in use is extended,
// at most once a day, so the extension is not a write per request.
func (e *Engine) AuthenticateMobileSession(ctx context.Context, token string) (session, user map[string]any, err error) {
	session, err = e.store.FindMobileSessionByToken(ctx, authz.HashSessionToken(token))
	if err != nil || session == nil {
		return nil, nil, err
	}
	user, err = e.store.GetItem(ctx, "users", utils.StrVal(session, "user_id"))
	if err != nil || user == nil {
		return nil, nil, err
	}
	now := utils.UTCNow()
	if exp, perr := time.Parse(time.RFC3339, utils.StrVal(session, "expires_at")); perr == nil &&
		exp.Sub(now) < MobileSessionTTL-24*time.Hour {
		e.extendMobileSession(ctx, utils.StrVal(session, "id"), now)
	}
	return session, user, nil
}

// extendMobileSession is best effort: failing to extend costs the person a
// sign-in a month from now, which is no reason to fail the request they made.
func (e *Engine) extendMobileSession(ctx context.Context, sessionID string, now time.Time) {
	_, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("mobile_sessions", sessionID), []string{"mobile_sessions"},
		func(state *store.State) (any, error) {
			sess := state.MobileSessions[sessionID]
			if sess == nil || sess["revoked_at"] != nil {
				return nil, nil
			}
			sess["expires_at"] = utils.ToISO(now.Add(MobileSessionTTL))
			sess["updated_at"] = utils.ToISO(now)
			return nil, nil
		}, advisoryLock["create_mobile_session"])
	if err != nil {
		slog.Warn("mobile_session_extend_failed", "session_id", sessionID, "error", err)
	}
}

// RevokeMobileSession signs a phone out by its own token.
func (e *Engine) RevokeMobileSession(ctx context.Context, token string) error {
	session, err := e.store.FindMobileSessionByToken(ctx, authz.HashSessionToken(token))
	if err != nil {
		return err
	}
	if session == nil {
		return nil
	}
	return e.revokeMobileSession(ctx, utils.StrVal(session, "id"), utils.StrVal(session, "user_id"))
}

// RevokeOwnMobileSession signs one of the caller's phones out — the lost one,
// typically, which cannot sign itself out.
func (e *Engine) RevokeOwnMobileSession(ctx context.Context, sessionID string) error {
	actor := authz.FromContext(ctx)
	if actor.Kind != authz.KindUser || actor.ID == "" {
		return errForbidden("only a signed-in user has mobile sessions")
	}
	return e.revokeMobileSession(ctx, sessionID, actor.ID)
}

// revokeMobileSession revokes a session only if it belongs to userID, checked
// under the lock, so a guessed id belonging to someone else is simply not found.
// Revoking rather than deleting keeps the row, like web sessions.
func (e *Engine) revokeMobileSession(ctx context.Context, sessionID, userID string) error {
	_, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("mobile_sessions", sessionID), []string{"mobile_sessions"},
		func(state *store.State) (any, error) {
			sess := state.MobileSessions[sessionID]
			if sess == nil || utils.StrVal(sess, "user_id") != userID {
				return nil, errNotFound(fmt.Sprintf("mobile session %s not found", sessionID))
			}
			if sess["revoked_at"] != nil {
				return nil, nil
			}
			ts := utils.ToISO(utils.UTCNow())
			sess["revoked_at"] = ts
			sess["is_active"] = false
			sess["updated_at"] = ts
			return nil, nil
		}, advisoryLock["create_mobile_session"])
	return err
}

// ListOwnMobileSessions shows the caller where their phones are signed in.
// The token hash stays on the server.
func (e *Engine) ListOwnMobileSessions(ctx context.Context) (map[string]any, error) {
	actor := authz.FromContext(ctx)
	if actor.Kind != authz.KindUser || actor.ID == "" {
		return nil, errForbidden("only a signed-in user has mobile sessions")
	}
	sessions, err := e.store.ListItemsIn(ctx, "mobile_sessions", "user_id", []any{actor.ID})
	if err != nil {
		return nil, err
	}
	devices, err := e.store.ListItemsIn(ctx, "mobile_devices", "user_id", []any{actor.ID})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]map[string]any, len(devices))
	for _, d := range devices {
		byID[utils.StrVal(d, "id")] = d
	}
	now := utils.UTCNow()
	items := []any{}
	for _, sess := range sessions {
		exp, err := time.Parse(time.RFC3339, utils.StrVal(sess, "expires_at"))
		if sess["revoked_at"] != nil || err != nil || !exp.After(now) {
			continue
		}
		device := byID[utils.StrVal(sess, "device_id")]
		items = append(items, map[string]any{
			"id":          sess["id"],
			"device_name": utils.StrVal(device, "device_name"),
			"platform":    utils.StrVal(device, "platform"),
			"created_at":  sess["created_at"],
			"expires_at":  sess["expires_at"],
		})
	}
	return map[string]any{"items": items}, nil
}

// GetMobileDashboard is the phone's home screen for userID: the groups that
// concern them and the schedules they are on call in right now.
func (e *Engine) GetMobileDashboard(ctx context.Context, userID string) (map[string]any, error) {

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
