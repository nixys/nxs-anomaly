package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// authenticate resolves the principal behind a request.
//
// Unlike the resolveScope it replaces, an empty key set no longer grants admin:
// a deployment with no credentials configured now rejects every management
// call. NXS_ANOMALY_ALLOW_ANONYMOUS restores the old behaviour for local
// development, and the server logs a warning at startup when it is on.
//
// A session cookie is tried first, so that a signed-in person acting in a
// browser is recorded as themselves even in a deployment that also has API
// keys configured. An invalid cookie falls through rather than rejecting: a
// stale cookie left over from a previous deployment must not lock out a caller
// who also presents a valid key.
//
// A mobile session is its own credential and is checked first when presented:
// the X-Mobile-Session header, or a bearer token carrying the mobile prefix. It
// is not a fallback — an invalid mobile token is a failed authentication, not
// a reason to try the cookie next to it.
func (srv *Server) authenticate(r *http.Request) (authz.Actor, bool) {
	if token := mobileToken(r); token != "" {
		return srv.mobileActor(r, token)
	}
	if actor, ok := srv.sessionActor(r); ok {
		return actor, true
	}
	candidate := presentedToken(r)
	if candidate == "" {
		if srv.cfg.AllowAnonymous {
			return anonymousActor, true
		}
		return authz.Actor{}, false
	}
	// Constant-time compare against every key to avoid leaking which key matched.
	// The loop runs to completion for the same reason.
	matchedRole, matchedID, matched := authz.RoleNone, "", false
	for key, role := range srv.cfg.APIKeys {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			matchedRole, matchedID, matched = authz.ParseRole(role), apiKeyID(key), true
		}
	}
	if !matched || !matchedRole.Valid() {
		return authz.Actor{}, false
	}
	return authz.Actor{
		ID:          matchedID,
		Kind:        authz.KindService,
		DisplayName: "api-key " + matchedID,
		Role:        matchedRole,
	}, true
}

// sessionActor resolves the session cookie to the person it belongs to.
//
// The user record is re-read on every request (one joined query, see
// store.FindSessionUser), so a role change or a deletion takes effect
// immediately instead of when the session happens to expire. That is the
// property that makes "revoke this person's access" mean something.
func (srv *Server) sessionActor(r *http.Request) (authz.Actor, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return authz.Actor{}, false
	}
	user, sessionID, err := srv.store.FindSessionUser(r.Context(), authz.HashSessionToken(cookie.Value))
	if err != nil {
		slog.Error("session_lookup_failed", "error", err)
		return authz.Actor{}, false
	}
	if user == nil || sessionID == "" {
		return authz.Actor{}, false
	}
	role := authz.ParseRole(utils.StrVal(user, "role"))
	if !role.Valid() {
		// The role was cleared while the session was alive. The session is not
		// revoked here — an administrator restoring the role should restore
		// access without forcing a fresh sign-in — it simply stops
		// authenticating anything.
		return authz.Actor{}, false
	}
	actor := userActor(user, role)
	if err := srv.applyTeamScope(r.Context(), &actor); err != nil {
		// Fail closed: a membership lookup that failed cannot be read as "this
		// person is in no team", which would silently narrow their view, nor
		// as "in every team", which would widen it.
		slog.Error("team_scope_lookup_failed", "user_id", actor.ID, "error", err)
		return authz.Actor{}, false
	}
	return actor, true
}

// mobileSessionHeader is how the first mobile API carried the session. The app
// sends a bearer token instead; the header keeps older callers working.
const mobileSessionHeader = "X-Mobile-Session"

// mobileToken returns the mobile session token the request presents, if any.
func mobileToken(r *http.Request) string {
	if v := r.Header.Get(mobileSessionHeader); v != "" {
		return v
	}
	if v := presentedToken(r); strings.HasPrefix(v, engine.MobileTokenPrefix) {
		return v
	}
	return ""
}

// mobileActor resolves a mobile session to the person it belongs to.
//
// The person keeps their own role and team scope, capped at responder: a phone
// is for answering pages, and a lost one should not be able to rewrite
// escalation chains. A viewer's phone stays read-only.
func (srv *Server) mobileActor(r *http.Request, token string) (authz.Actor, bool) {
	if srv.eng == nil {
		return authz.Actor{}, false
	}
	_, user, err := srv.eng.AuthenticateMobileSession(r.Context(), token)
	if err != nil {
		slog.Error("mobile_session_lookup_failed", "error", err)
		return authz.Actor{}, false
	}
	if user == nil {
		return authz.Actor{}, false
	}
	role := authz.ParseRole(utils.StrVal(user, "role"))
	if !role.Valid() {
		return authz.Actor{}, false
	}
	if authz.RoleRank(role) > authz.RoleRank(authz.RoleResponder) {
		role = authz.RoleResponder
	}
	actor := userActor(user, role)
	if err := srv.applyTeamScope(r.Context(), &actor); err != nil {
		slog.Error("team_scope_lookup_failed", "user_id", actor.ID, "error", err)
		return authz.Actor{}, false
	}
	return actor, true
}

// applyTeamScope attaches the actor's team membership when scoping is enabled.
//
// Admins are exempt: someone who can hand out roles and read the audit trail is
// not meaningfully contained by a team boundary, and exempting them keeps a way
// in when every team's membership is wrong. API keys and the worker are exempt
// for the same reason they always were — they are not people and have no teams.
func (srv *Server) applyTeamScope(ctx context.Context, actor *authz.Actor) error {
	if !srv.cfg.TeamScoping || actor.Kind != authz.KindUser || actor.Role == authz.RoleAdmin {
		return nil
	}
	teamIDs, err := srv.store.ListTeamIDsForUser(ctx, actor.ID)
	if err != nil {
		return err
	}
	actor.TeamIDs = teamIDs
	actor.TeamScoped = true
	return nil
}

// anonymousActor is what an unauthenticated request becomes when
// AllowAnonymous is on. It is a service actor with admin rights (that is the
// point of the escape hatch), but it is labelled so audit records make the
// unauthenticated origin obvious rather than blending in with real keys.
var anonymousActor = authz.Actor{
	ID:          "anonymous",
	Kind:        authz.KindService,
	DisplayName: "anonymous (auth disabled)",
	Role:        authz.RoleAdmin,
}

// provisionerHeader lets any caller declare which infrastructure-as-code tool it
// is acting as. It exists for two reasons: a provisioner other than Terraform,
// and the escape hatch — an object whose Terraform definition no longer exists
// still has to be removable, and this is how somebody says "I am that pipeline,
// clean it up".
const provisionerHeader = "X-Nxs-Anomaly-Provisioner"

// detectProvisioner names the tool behind a request, or "" for a person.
//
// The User-Agent is enough on its own because the Terraform provider is built
// on terraform-plugin-sdk, which always sends "Terraform/<version> ...". This is
// not a credential and is not treated as one: the request still has to
// authenticate and still has to hold the role for what it is doing. All the
// name decides is which objects it may touch — and claiming to be Terraform
// only ever *restricts* the caller to Terraform's own objects while stamping
// what it creates as somebody else's to manage.
func detectProvisioner(r *http.Request) string {
	if v := strings.ToLower(strings.TrimSpace(r.Header.Get(provisionerHeader))); v != "" {
		return v
	}
	if strings.Contains(strings.ToLower(r.Header.Get("User-Agent")), "terraform") {
		return "terraform"
	}
	return ""
}

// presentedToken extracts the credential from either supported header.
func presentedToken(r *http.Request) string {
	if v := r.Header.Get("X-API-Key"); v != "" {
		return v
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return auth[7:]
	}
	return ""
}

// apiKeyID derives a short, stable, non-reversible label for a key so audit
// records can distinguish keys without ever storing or printing the secret.
func apiKeyID(key string) string {
	h := fnv1a(key)
	const hex = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = hex[h&0xf]
		h >>= 4
	}
	return string(out)
}

// fnv1a is the 32-bit FNV-1a hash. It is used for labelling only — never for
// authentication — so a non-cryptographic hash is adequate and keeps this file
// dependency-free.
func fnv1a(s string) uint32 {
	const (
		offset uint32 = 2166136261
		prime  uint32 = 16777619
	)
	h := offset
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return h
}

// requiredAction maps a management API request to the capability it needs.
//
// The mapping is path-based rather than method-based: a responder must be able
// to POST /acknowledge but not POST /integrations, and those are the same verb.
// Anything unrecognised falls back to read for GET and edit for writes, so a
// newly added endpoint is never accidentally open to a viewer.
func requiredAction(method, path string) authz.Action {
	switch {
	// The audit trail names who did what; reading it is an administrative act.
	case hasPrefixPath(path, "/api/v1/audit"):
		return authz.ActionAdmin
	// Describing the caller to itself, and changing one's own password, are
	// available to every authenticated principal regardless of role — including
	// a viewer, who otherwise may not POST anything. Read is the floor here,
	// not a claim that these are read operations.
	// Managing your own sessions belongs here for the same reason as the
	// password: it is about the caller themselves, not about anything a role
	// governs. The store scopes both to the caller's own rows.
	// Choosing the language and timezone you read the product in belongs here
	// too: it changes nothing anybody else can see, and a viewer paged at 3am
	// should not need an admin to get the UI into their own language.
	// Pairing a phone and signing it out are the same kind of thing: the
	// caller's own sessions. The phone gets the caller's role, capped, so a
	// viewer pairing one gains nothing.
	case path == "/api/v1/auth/me", path == "/api/v1/auth/password",
		path == "/api/v1/auth/preferences",
		hasPrefixPath(path, "/api/v1/auth/sessions"),
		path == "/api/v1/mobile/pairing",
		method == http.MethodDelete && hasPrefixPath(path, "/api/v1/mobile/sessions"):
		return authz.ActionRead
	// Exporting a person's data is a GET, and the read floor below would let any
	// authenticated principal pull somebody's addresses, paging history and
	// audit trail. Named before the general users rule so the method does not
	// decide it.
	case hasPrefixPath(path, "/api/v1/users") &&
		(strings.HasSuffix(path, "/export") || strings.HasSuffix(path, "/erase")):
		return authz.ActionAdmin
	// Identity is admin-only to change, but readable by anyone who can read:
	// schedules and alert timelines are meaningless without user names.
	case hasPrefixPath(path, "/api/v1/users") && method != http.MethodGet:
		return authz.ActionAdmin
	// Forcing escalation processing and dumping route resolution both reach
	// beyond a single configuration object.
	case hasPrefixPath(path, "/api/v1/escalations"), hasPrefixPath(path, "/api/v1/routes/debug"):
		return authz.ActionAdmin
	// Accepting readiness blockers, and asserting that a backup exists, are both
	// claims about the installation that the readiness gate then trusts. Reading
	// the report needs only read access — the GET falls through to the rule
	// below — but writing either of these is administrative.
	case hasPrefixPath(path, "/api/v1/backups"),
		path == "/api/v1/readiness/acknowledge":
		return authz.ActionAdmin
	case method == http.MethodGet:
		return authz.ActionRead
	case isRespondPath(path):
		return authz.ActionRespond
	default:
		return authz.ActionEdit
	}
}

// hasPrefixPath reports whether path is prefix itself or a child of it, so
// "/api/v1/usersearch" does not match the "/api/v1/users" rule.
func hasPrefixPath(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// respondSuffixes are the alert-group state transitions a responder may perform.
var respondSuffixes = []string{
	"/acknowledge", "/unacknowledge", "/resolve", "/unresolve", "/silence",
}

func isRespondPath(path string) bool {
	if strings.HasPrefix(path, "/api/v1/alert-groups/bulk-") ||
		strings.HasPrefix(path, "/api/v1/mobile/alert-groups/") {
		return true
	}
	if !strings.HasPrefix(path, "/api/v1/alert-groups/") {
		return false
	}
	for _, s := range respondSuffixes {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}
