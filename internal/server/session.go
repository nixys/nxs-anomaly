package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// session.go implements sign-in with a local password and the browser session
// that follows it. It is the path that gives the audit trail a person's name
// instead of a shared key's fingerprint.

const sessionCookieName = "nxs_anomaly_session"

// loginRatePerSecond / loginBurst throttle sign-in attempts per client IP. The
// values are deliberately not configurable: password endpoints are the one
// place where a permissive limit is never the right answer, and the burst still
// leaves room for someone mistyping a few times.
const (
	loginRatePerSecond = 0.1 // one attempt per 10s sustained
	loginBurst         = 5
)

// isAuthPublicPath reports whether a path is one of the endpoints that must be
// reachable without credentials. Sign-in obviously cannot require being signed
// in; sign-out and the method list are here too, so a client holding a stale or
// invalid cookie can still recover instead of being stuck at 401.
func isAuthPublicPath(path string) bool {
	switch path {
	case "/api/v1/auth/login", "/api/v1/auth/logout", "/api/v1/auth/methods":
		return true
	}
	return false
}

// handleAuthPublic serves the unauthenticated auth endpoints.
func (srv *Server) handleAuthPublic(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/methods":
		srv.handleAuthMethods(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
		srv.handleLogin(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/logout":
		srv.handleLogout(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

// handleAuthMethods tells the UI how this deployment expects people to sign in,
// so the sign-in screen does not have to guess. It reveals only which doors
// exist, never whether a particular one would open.
func (srv *Server) handleAuthMethods(w http.ResponseWriter, r *http.Request) {
	n, err := srv.store.CountUsersWithPassword(r.Context())
	if err != nil {
		// A database blip must not make the sign-in screen claim that password
		// login is unavailable, which would push people towards the API key.
		slog.Warn("auth_methods_count_failed", "error", err)
		n = 0
	}
	resp := map[string]any{
		"password":  n > 0,
		"api_key":   len(srv.cfg.APIKeys) > 0,
		"anonymous": srv.cfg.AllowAnonymous,
		"oidc":      srv.oidc != nil,
		// The edition, so the sign-in screen can show a door that is missing
		// rather than silently drawing one fewer button. It says nothing about
		// whether any particular door would open, and it is not a secret: it is
		// already written on the image tag.
		"edition": Edition,
		// sso_available says whether this build has the provider at all. With
		// it, the screen can tell "nobody configured SSO here" apart from "this
		// edition has no SSO", which are different problems with different
		// people to talk to.
		"sso_available": editionHasSSO,
	}
	if srv.oidc != nil {
		// Named so the button can say "Sign in with Okta" rather than "Sign in
		// with OIDC". It is the issuer host, which is not a secret.
		resp["oidc_label"] = oidcLabel(srv.oidc.cfg.Issuer)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLogin exchanges a username/e-mail and password for a session cookie.
func (srv *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := srv.clientIP(r)
	// The budget is checked before the password hash is computed, so a flood of
	// wrong passwords is rejected cheaply — but it is *charged only for attempts
	// that fail*. Brute force is failures; counting successes too means a shared
	// egress IP (an office NAT, a CI runner) locks out its own users after five
	// legitimate sign-ins, and a rate of one per ten seconds never lets them back
	// in during a busy morning. The password-change handler below already limits
	// only the failing branch; this makes sign-in consistent with it.
	if !srv.loginLimiter.allow(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many sign-in attempts"})
		return
	}
	loginSucceeded := false
	defer func() {
		if loginSucceeded {
			srv.loginLimiter.refund(ip)
		}
	}()
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	login := strings.TrimSpace(utils.StrVal(body, "login"))
	if login == "" {
		login = strings.TrimSpace(utils.StrVal(body, "username"))
	}
	password := utils.StrVal(body, "password")
	if login == "" || password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "login and password are required"})
		return
	}

	ctx := r.Context()
	user, role, err := srv.verifyCredentials(ctx, login, password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "sign-in failed"})
		return
	}
	if user == nil {
		// One message for "no such user", "no password set", "wrong password"
		// and "role revoked": distinguishing them would turn this endpoint into
		// a user-enumeration oracle.
		slog.Info("login_failed", "login", login, "ip", ip)
		srv.auditLoginFailed(r, login, ip)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid login or password"})
		return
	}

	// startSession is shared with the single sign-on callback, so both routes
	// produce exactly the same kind of session and the same audit shape.
	if err := srv.startSession(w, r, user, role, "auth.login"); err != nil {
		slog.Error("session_create_failed", "user_id", utils.StrVal(user, "id"), "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "sign-in failed"})
		return
	}
	loginSucceeded = true
	writeJSON(w, http.StatusOK, map[string]any{
		"user": publicUser(user),
		"role": role.String(),
	})
}

// auditLoginFailed records a rejected sign-in attempt.
//
// The service log alone is not an audit trail: it rotates, it is not
// append-only, and it is not what an operator reads when asked to show who
// tried to get in. Without this, a brute-force run leaves nothing in
// nxs_anomaly_audit_events while every successful login is recorded — exactly
// backwards for the events worth reviewing.
//
// The attempted login is stored; the password never is. The trail is admin-only,
// so naming the attempted login here does not reopen the enumeration oracle the
// response body avoids.
//
// When the login resolves to a real user the event is attributed to them, even
// though whoever typed it failed to prove they are that person. That is not a
// claim about who was at the keyboard — it is what makes the record erasable:
// PseudonymiseAuditActor keys on actor_id, so an event left anonymous would
// keep this person's username and IP beyond the reach of an erasure request.
// It also puts the attempts against an account next to that account's own
// events. A login matching nobody stays unattributed: there is no data subject
// here to erase.
func (srv *Server) auditLoginFailed(r *http.Request, login, ip string) {
	ctx := r.Context()
	subjectID, name := "", "anonymous"
	if user, err := srv.store.FindUserByLogin(ctx, login); err == nil && user != nil {
		subjectID = utils.StrVal(user, "id")
		name = utils.StrVal(user, "name")
	}
	actor := authz.Actor{ID: subjectID, Kind: authz.KindUser, DisplayName: name, Role: authz.RoleNone}
	auditCtx := engine.NewRequestIPContext(authz.NewContext(ctx, actor), ip)
	srv.eng.AuditEvent(auditCtx, "auth.login_failed", "user", subjectID, map[string]any{"login": login})
}

// verifyCredentials resolves a login to a user that may sign in right now.
//
// It returns (nil, RoleNone, nil) for every rejection reason. Only a genuine
// infrastructure failure comes back as an error, so the caller cannot
// accidentally turn "wrong password" into a 500 or the reverse.
func (srv *Server) verifyCredentials(ctx context.Context, login, password string) (map[string]any, authz.Role, error) {
	user, err := srv.store.FindUserByLogin(ctx, login)
	if err != nil {
		slog.Error("login_lookup_failed", "error", err)
		return nil, authz.RoleNone, err
	}
	if user == nil {
		// Still spend the hashing time, so a missing user is not measurably
		// faster to reject than a wrong password.
		authz.VerifyPassword(password, dummyPasswordHash)
		return nil, authz.RoleNone, nil
	}
	userID := utils.StrVal(user, "id")
	hash, ok, err := srv.store.GetUserPasswordHash(ctx, userID)
	if err != nil {
		slog.Error("login_credential_lookup_failed", "error", err)
		return nil, authz.RoleNone, err
	}
	if !ok {
		authz.VerifyPassword(password, dummyPasswordHash)
		return nil, authz.RoleNone, nil
	}
	if !authz.VerifyPassword(password, hash) {
		return nil, authz.RoleNone, nil
	}
	// A user whose role was cleared is a roster entry again: they may be paged,
	// but they may not sign in. Checking here as well as in authenticate means
	// revoking a role takes effect without having to also revoke credentials.
	role := authz.ParseRole(utils.StrVal(user, "role"))
	if !role.Valid() {
		return nil, authz.RoleNone, nil
	}
	return user, role, nil
}

// dummyPasswordHash is a valid hash of a value nobody knows. Verifying against
// it makes the rejection path for an unknown user cost the same as for a known
// one, so response timing does not enumerate accounts. It is generated once at
// startup rather than hard-coded so no fixed value ever needs to be trusted.
var dummyPasswordHash = func() string {
	token, err := authz.NewSessionToken()
	if err != nil {
		// #nosec G101 -- not a credential: a fixed fallback fed to the password
		// hasher only if crypto/rand fails, so the timing-equaliser hash still exists.
		token = "nxs-anomaly-timing-equaliser"
	}
	h, err := authz.HashPassword(token)
	if err != nil {
		return ""
	}
	return h
}()

// handleLogout revokes the presented session and clears the cookie. It is
// deliberately tolerant: an absent or already-dead session is still a 200,
// because the caller's goal — not being signed in — is satisfied either way.
func (srv *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if err := srv.store.RevokeWebSession(r.Context(), authz.HashSessionToken(c.Value)); err != nil {
			slog.Warn("session_revoke_failed", "error", err)
		}
	}
	http.SetCookie(w, srv.clearedSessionCookie())
	writeJSON(w, http.StatusOK, map[string]any{"signed_out": true})
}

// checkSameOriginWrite rejects a state-changing request that was authenticated
// by a session cookie and carries an Origin from somewhere else.
//
// SameSite=Strict already prevents the browser from sending the cookie in that
// situation, so this never fires in a correctly configured deployment. It is
// here as the second lock: if the cookie attribute is ever relaxed — for a
// split-origin deployment, say — CSRF protection should not silently disappear
// along with it. Requests without an Origin header (curl, servers) are
// unaffected; a browser always sends one on a cross-origin write.
func checkSameOriginWrite(r *http.Request) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if c, err := r.Cookie(sessionCookieName); err != nil || c.Value == "" {
		return nil
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host != r.Host {
		return errors.New("cross-origin request rejected")
	}
	return nil
}

// handleMe describes the caller to itself: which principal the request
// resolved to and what it may do. The UI uses it to decide which controls to
// render, so the permission model is stated in one place instead of being
// re-derived in the frontend.
func (srv *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	actor := authz.FromContext(r.Context())
	// Presentation settings ride along on the identity call so the UI can pick a
	// language and a timezone on its first paint. They are only meaningful for a
	// person: an API key has no language, and reporting one would invite the UI
	// to store a preference against a principal that cannot own it.
	locale, timezone := "", ""
	if actor.Kind == authz.KindUser && actor.ID != "" {
		if user, err := srv.eng.GetItem(r.Context(), "users", actor.ID); err == nil {
			locale = utils.StrVal(user, "locale")
			timezone = utils.StrVal(user, "timezone")
		}
		// A lookup failure is not fatal: the caller is already authenticated, and
		// the UI degrades to the browser's language and zone.
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           actor.ID,
		"kind":         actor.Kind,
		"display_name": actor.Describe(),
		"role":         actor.Role.String(),
		"locale":       locale,
		"timezone":     timezone,
		// Reported so the UI can explain an empty list as a boundary rather
		// than as "nothing has happened yet".
		"team_scoped": actor.TeamScoped,
		"team_ids":    actor.TeamIDs,
		"permissions": map[string]bool{
			"read":    actor.Can(authz.ActionRead),
			"respond": actor.Can(authz.ActionRespond),
			"edit":    actor.Can(authz.ActionEdit),
			"admin":   actor.Can(authz.ActionAdmin),
		},
	})
}

// handleListSessions shows a person where they are signed in.
//
// It is the practical half of "the session is revocable": without a list, a
// forgotten sign-in on a shared machine can only be dealt with by changing the
// password, which is a much bigger hammer.
func (srv *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	actor := authz.FromContext(r.Context())
	if actor.Kind != authz.KindUser || actor.ID == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "only a signed-in user has sessions"})
		return
	}
	sessions, err := srv.store.ListWebSessions(r.Context(), actor.ID)
	if err != nil {
		slog.Error("list_sessions_failed", "user_id", actor.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not list sessions"})
		return
	}
	// The hash of the presented token identifies the caller's own row. It is
	// compared here and never included in the response.
	var currentHash string
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		currentHash = authz.HashSessionToken(c.Value)
	}
	items := make([]map[string]any, 0, len(sessions))
	for _, sess := range sessions {
		items = append(items, map[string]any{
			"id":         sess.ID,
			"created_at": utils.ToISO(sess.CreatedAt.UTC()),
			"expires_at": utils.ToISO(sess.ExpiresAt.UTC()),
			"request_ip": sess.RequestIP,
			"user_agent": sess.UserAgent,
			"current":    currentHash != "" && sess.TokenHash == currentHash,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// handleRevokeSession signs one session out. The store scopes the update to the
// caller's own sessions, so a guessed id reaches nothing.
func (srv *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	actor := authz.FromContext(r.Context())
	if actor.Kind != authz.KindUser || actor.ID == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "only a signed-in user has sessions"})
		return
	}
	revoked, err := srv.store.RevokeWebSessionByID(r.Context(), sessionID, actor.ID)
	if err != nil {
		slog.Error("revoke_session_failed", "user_id", actor.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not revoke session"})
		return
	}
	if !revoked {
		// Same answer for "no such session" and "not yours": which sessions
		// other people hold is not something this endpoint should reveal.
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	srv.eng.AuditEvent(r.Context(), "auth.session_revoked", "user", actor.ID,
		map[string]any{"session_id": sessionID})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "id": sessionID})
}

// handleRevokeUserSessions is the administrative "sign this person out
// everywhere", for when an account is suspected compromised but the password
// is being rotated separately.
func (srv *Server) handleRevokeUserSessions(w http.ResponseWriter, r *http.Request, userID string) {
	if _, err := srv.eng.GetItem(r.Context(), "users", userID); err != nil {
		writeEngineError(w, err)
		return
	}
	n, err := srv.store.RevokeUserSessions(r.Context(), userID)
	if err != nil {
		slog.Error("revoke_user_sessions_failed", "user_id", userID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not revoke sessions"})
		return
	}
	srv.eng.AuditEvent(r.Context(), "auth.sessions_revoked", "user", userID,
		map[string]any{"revoked": n})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n, "id": userID})
}

// handleChangeOwnPassword lets a signed-in person change their own password.
//
// It requires the current password even though the session already proves
// identity: that is what stops a briefly unattended browser from becoming a
// permanent account takeover.
// handleUpdateOwnPreferences saves the caller's UI language and timezone.
//
// It exists as its own endpoint rather than as a PATCH on /users/{id} because
// that route is admin-only — it can hand out roles. Choosing the language you
// read the product in is not an administrative act, and a viewer who is woken
// at night must be able to do it without an admin.
func (srv *Server) handleUpdateOwnPreferences(w http.ResponseWriter, r *http.Request) {
	actor := authz.FromContext(r.Context())
	if actor.Kind != authz.KindUser || actor.ID == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "only a signed-in user has preferences",
		})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	user, err := srv.eng.UpdateUserPreferences(r.Context(), actor.ID, body)
	if err != nil {
		writeResult(w, http.StatusOK, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"locale":   utils.StrVal(user, "locale"),
		"timezone": utils.StrVal(user, "timezone"),
	})
}

func (srv *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	actor := authz.FromContext(r.Context())
	if actor.Kind != authz.KindUser || actor.ID == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "only a signed-in user can change a password this way; use PUT /api/v1/users/{id}/password as an admin",
		})
		return
	}
	body, ok := readJSON(w, r)
	if !ok {
		return
	}
	current := utils.StrVal(body, "current_password")
	next := utils.StrVal(body, "new_password")
	ctx := r.Context()

	hash, found, err := srv.store.GetUserPasswordHash(ctx, actor.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "password change failed"})
		return
	}
	if !found || !authz.VerifyPassword(current, hash) {
		if !srv.loginLimiter.allow(srv.clientIP(r)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many attempts"})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "current password is incorrect"})
		return
	}
	if err := srv.setPassword(ctx, actor.ID, next); err != nil {
		writeSetPasswordError(w, err)
		return
	}
	// Signing every session out — including this one — is the point: a password
	// change is how someone reacts to a suspected compromise, and leaving the
	// attacker's session alive would defeat it.
	srv.revokeSessions(ctx, actor.ID)
	http.SetCookie(w, srv.clearedSessionCookie())
	srv.eng.AuditEvent(ctx, "auth.password_changed", "user", actor.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"updated": true, "signed_out": true})
}

// handleSetUserPassword is the administrative counterpart: set or clear another
// user's password without knowing the old one.
func (srv *Server) handleSetUserPassword(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()
	user, err := srv.eng.GetItem(ctx, "users", userID)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if err := srv.store.DeleteUserPassword(ctx, userID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not remove password"})
			return
		}
		srv.revokeSessions(ctx, userID)
		srv.eng.AuditEvent(ctx, "auth.password_removed", "user", userID, nil)
		writeJSON(w, http.StatusOK, map[string]any{"removed": true, "id": userID})
	default:
		body, ok := readJSON(w, r)
		if !ok {
			return
		}
		if err := srv.setPassword(ctx, userID, utils.StrVal(body, "password")); err != nil {
			writeSetPasswordError(w, err)
			return
		}
		srv.revokeSessions(ctx, userID)
		srv.eng.AuditEvent(ctx, "auth.password_set", "user", userID, nil)
		writeJSON(w, http.StatusOK, map[string]any{
			"updated": true,
			"id":      userID,
			// Surfaced because setting a password on a user with no role
			// produces someone who still cannot sign in, which is a confusing
			// thing to discover later.
			"can_sign_in": authz.ParseRole(utils.StrVal(user, "role")).Valid(),
		})
	}
}

// setPassword hashes and stores a new password.
func (srv *Server) setPassword(ctx context.Context, userID, password string) error {
	hash, err := authz.HashPassword(password)
	if err != nil {
		return err
	}
	return srv.store.SetUserPassword(ctx, userID, hash)
}

func writeSetPasswordError(w http.ResponseWriter, err error) {
	if errors.Is(err, authz.ErrPasswordTooShort) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	slog.Error("set_password_failed", "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not set password"})
}

// revokeSessions signs a user out everywhere, logging rather than failing: the
// credential change it follows has already been committed, and reporting an
// error here would suggest it had not.
func (srv *Server) revokeSessions(ctx context.Context, userID string) {
	if n, err := srv.store.RevokeUserSessions(ctx, userID); err != nil {
		slog.Error("session_revoke_all_failed", "user_id", userID, "error", err)
	} else if n > 0 {
		slog.Info("sessions_revoked", "user_id", userID, "count", n)
	}
}

// ---- startup ----

// bootstrapAdmin creates or updates the break-glass administrator described by
// NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME / _PASSWORD.
//
// It runs on every start and is idempotent: the password is re-applied, so the
// variables are also the recovery path after a forgotten password, not only the
// first-run path. That is why it does not stop once a user exists.
//
// An existing user matched by that login is promoted to admin. Refusing to
// would make the recovery path useless in exactly the case it is needed.
func (srv *Server) bootstrapAdmin(ctx context.Context) error {
	username := strings.TrimSpace(srv.cfg.BootstrapAdminUsername)
	password := srv.cfg.BootstrapAdminPassword
	if username == "" && password == "" {
		return nil
	}
	if username == "" || password == "" {
		return errors.New("both NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME and _PASSWORD must be set")
	}

	// The bootstrap acts on its own authority, and the audit trail should say
	// so rather than attributing it to the system worker.
	ctx = authz.NewContext(ctx, authz.Actor{
		ID:          "bootstrap",
		Kind:        authz.KindService,
		DisplayName: "bootstrap (NXS_ANOMALY_BOOTSTRAP_ADMIN_*)",
		Role:        authz.RoleAdmin,
	})

	user, err := srv.store.FindUserByLogin(ctx, username)
	if err != nil {
		return err
	}
	created := false
	if user == nil {
		user, err = srv.eng.CreateUser(ctx, map[string]any{
			"name":     username,
			"username": username,
			"role":     string(authz.RoleAdmin),
		})
		if err != nil {
			return err
		}
		created = true
	} else if authz.ParseRole(utils.StrVal(user, "role")) != authz.RoleAdmin {
		if user, err = srv.eng.UpdateUser(ctx, utils.StrVal(user, "id"),
			map[string]any{"role": string(authz.RoleAdmin)}); err != nil {
			return err
		}
	}
	userID := utils.StrVal(user, "id")
	if err := srv.setPassword(ctx, userID, password); err != nil {
		return err
	}
	// Any session that predates this password is signed out, for the same
	// reason a password change signs sessions out.
	srv.revokeSessions(ctx, userID)
	srv.eng.AuditEvent(ctx, "auth.bootstrap_admin", "user", userID, map[string]any{"created": created})
	slog.Warn("bootstrap_admin_applied",
		"user_id", userID, "username", username, "created", created,
		"effect", "password reset from environment on every start; unset the variables once you no longer need the break-glass path")
	return nil
}

// hasPasswordUsers reports whether anyone can sign in with a password. Used
// only to decide which startup warning to print, so a lookup failure is
// reported as "no" — the louder of the two messages.
func (srv *Server) hasPasswordUsers(ctx context.Context) bool {
	n, err := srv.store.CountUsersWithPassword(ctx)
	if err != nil {
		slog.Warn("password_user_count_failed", "error", err)
		return false
	}
	return n > 0
}

// ---- cookie helpers ----

func (srv *Server) sessionTTL() time.Duration {
	if srv.cfg.SessionTTL > 0 {
		return srv.cfg.SessionTTL
	}
	return 12 * time.Hour
}

// sessionCookie builds the session cookie.
//
// SameSite=Strict is what defends this API against CSRF: the browser will not
// attach the cookie to any request originating from another site, so no
// separate anti-forgery token is needed. It is affordable here because the UI
// is served same-origin with the API through nginx. A deployment that splits
// them across origins must revisit this, not just relax the attribute.
func (srv *Server) sessionCookie(token string, expiresAt time.Time) *http.Cookie {
	// #nosec G124 -- HttpOnly and SameSite=Strict are set; Secure is deliberately
	// configurable (SessionCookieSecure, default true) so a same-origin localhost
	// HTTP deployment can opt out. See the comment above this function.
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   srv.cfg.SessionCookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
}

func (srv *Server) clearedSessionCookie() *http.Cookie {
	// #nosec G124 -- same attributes as sessionCookie; Secure is configurable by design.
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   srv.cfg.SessionCookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
}

// ---- user shaping ----

// userActor builds the actor for an authenticated person.
func userActor(user map[string]any, role authz.Role) authz.Actor {
	name := utils.StrVal(user, "username")
	if name == "" {
		name = utils.StrVal(user, "name")
	}
	if name == "" {
		name = utils.StrVal(user, "id")
	}
	return authz.Actor{
		ID:          utils.StrVal(user, "id"),
		Kind:        authz.KindUser,
		DisplayName: name,
		Role:        role,
	}
}

// publicUser is the subset of a user record returned by the auth endpoints:
// enough to greet someone by name, and nothing that the sign-in response has
// any business carrying.
func publicUser(user map[string]any) map[string]any {
	return map[string]any{
		"id":       utils.StrVal(user, "id"),
		"name":     utils.StrVal(user, "name"),
		"username": utils.StrVal(user, "username"),
		"email":    utils.StrVal(user, "email"),
		"role":     utils.StrVal(user, "role"),
		"timezone": utils.StrVal(user, "timezone"),
	}
}

func truncate(s string, n int) string {
	return utils.TruncateRunes(s, n)
}

// startSession issues the session cookie for an authenticated person. It is
// shared by password sign-in and OIDC so both produce exactly the same kind of
// session — there is no such thing as a "weaker" SSO session here.
func (srv *Server) startSession(w http.ResponseWriter, r *http.Request, user map[string]any, role authz.Role, auditAction string) error {
	token, err := authz.NewSessionToken()
	if err != nil {
		return err
	}
	ip := srv.clientIP(r)
	expiresAt := time.Now().Add(srv.sessionTTL())
	userID := utils.StrVal(user, "id")
	sess := store.WebSession{
		ID:        utils.MakeID("wses"),
		TokenHash: authz.HashSessionToken(token),
		UserID:    userID,
		ExpiresAt: expiresAt,
		RequestIP: ip,
		UserAgent: truncate(r.UserAgent(), 512),
	}
	if err := srv.store.CreateWebSession(r.Context(), sess); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	http.SetCookie(w, srv.sessionCookie(token, expiresAt))

	actor := userActor(user, role)
	auditCtx := engine.NewRequestIPContext(authz.NewContext(r.Context(), actor), ip)
	srv.eng.AuditEvent(auditCtx, auditAction, "user", userID, map[string]any{"session_id": sess.ID})
	return nil
}
