package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// testPassword is long enough to clear authz.MinPasswordLength.
const testPassword = "correct-horse-battery"

// TestMain lowers the password work factor for this package only.
//
// The production factor is deliberately expensive (≈200 ms per derivation, and
// several seconds under the race detector), and these tests sign in dozens of
// times. The value being exercised here is the sign-in *logic*; that the work
// factor is 600 000 is asserted in internal/authz, whose own tests never touch
// this knob.
func TestMain(m *testing.M) {
	authz.SetPasswordIterations(1000)
	os.Exit(m.Run())
}

// call drives the full authenticated entry point (handleAPI), so these tests
// exercise the same order of operations a real request does: public auth paths,
// authentication, the CSRF check, then routing.
func (srv *Server) call(method, path, body string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	for _, f := range mutate {
		f(r)
	}
	w := httptest.NewRecorder()
	srv.handleAPI(w, r)
	return w
}

func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(c) }
}

// sessionCookieFrom extracts the session cookie from a response, failing the
// test when there is none.
func sessionCookieFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie in response: %s", w.Body.String())
	return nil
}

// seedUser creates a user with a role and, when password is non-empty, a
// credential to sign in with.
func seedUser(t *testing.T, srv *Server, st *storetest.Store, id, username, role, password string) {
	t.Helper()
	st.Seed("users", map[string]any{
		"id": id, "name": username, "username": username,
		"email": username + "@example.test", "role": role,
	})
	if password != "" {
		if err := srv.setPassword(context.Background(), id, password); err != nil {
			t.Fatalf("set password: %v", err)
		}
	}
}

func newSessionServer(t *testing.T) (*Server, *storetest.Store) {
	t.Helper()
	srv, st := newTestServer()
	seedUser(t, srv, st, "usr-admin", "ada", string(authz.RoleAdmin), testPassword)
	return srv, st
}

func login(t *testing.T, srv *Server, login, password string) *httptest.ResponseRecorder {
	t.Helper()
	return srv.call(http.MethodPost, "/api/v1/auth/login",
		`{"login":"`+login+`","password":"`+password+`"}`)
}

// TestLoginIssuesUsableSession is the core of the backlog item: a person signs
// in as themselves and the API recognises them, without any shared API key
// being involved.
func TestLoginIssuesUsableSession(t *testing.T) {
	srv, _ := newSessionServer(t)

	w := login(t, srv, "ada", testPassword)
	if w.Code != http.StatusOK {
		t.Fatalf("login: got %d, body %s", w.Code, w.Body.String())
	}
	cookie := sessionCookieFrom(t, w)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie must be HttpOnly and SameSite=Strict, got %+v", cookie)
	}
	if cookie.Value == "" {
		t.Fatal("empty session token")
	}

	w = srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("/auth/me with session: got %d, body %s", w.Code, w.Body.String())
	}
	var me map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode /auth/me: %v", err)
	}
	if me["id"] != "usr-admin" || me["kind"] != authz.KindUser || me["role"] != string(authz.RoleAdmin) {
		t.Errorf("session must resolve to the person, got %+v", me)
	}
}

// TestLoginResponseHidesCredentials guards the sign-in response against
// carrying anything that is not needed to greet someone by name.
func TestLoginResponseHidesCredentials(t *testing.T) {
	srv, _ := newSessionServer(t)
	body := login(t, srv, "ada", testPassword).Body.String()
	for _, forbidden := range []string{"password", "pbkdf2", "token_hash"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("sign-in response leaks %q: %s", forbidden, body)
		}
	}
}

// TestLoginRejections covers every way a sign-in must fail, and asserts they
// are indistinguishable to the caller: a different status or message for
// "no such user" would turn this endpoint into a user-enumeration oracle.
func TestLoginRejections(t *testing.T) {
	srv, st := newSessionServer(t)
	// Has a password but no role: a roster entry that can be paged, not an
	// account.
	seedUser(t, srv, st, "usr-roster", "roscoe", "", testPassword)
	// Has a role but no password: cannot sign in this way at all.
	seedUser(t, srv, st, "usr-nopass", "nora", string(authz.RoleEditor), "")

	cases := []struct{ name, login, password string }{
		{"unknown user", "nobody", testPassword},
		{"wrong password", "ada", "wrong-password-here"},
		{"no role", "roscoe", testPassword},
		{"no password set", "nora", testPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh limiter per case: the point here is the rejection, not
			// the throttle.
			srv.loginLimiter = newRateLimiter(loginRatePerSecond, loginBurst)
			srv.loginAccountLimiter = newRateLimiter(loginAccountRatePerSecond, loginAccountBurst)
			w := login(t, srv, tc.login, tc.password)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401; body %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "invalid login or password") {
				t.Errorf("rejection reason must not be distinguishable: %s", w.Body.String())
			}
			for _, c := range w.Result().Cookies() {
				if c.Name == sessionCookieName && c.Value != "" {
					t.Error("failed sign-in must not set a session cookie")
				}
			}
		})
	}
}

// TestLoginRateLimited: password endpoints without a throttle are brute-force
// targets, so the limiter is not optional.
func TestLoginRateLimited(t *testing.T) {
	srv, _ := newSessionServer(t)
	limited := false
	for i := 0; i < loginBurst+3; i++ {
		if login(t, srv, "ada", "wrong-password-here").Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("expected a 429 within %d attempts", loginBurst+3)
	}
}

// TestSessionRoleIsReadLive pins the reason the session lookup joins the users
// table on every request: revoking a role must take effect immediately, not
// when the session eventually expires.
func TestSessionRoleIsReadLive(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-ed", "eddie", string(authz.RoleEditor), testPassword)
	cookie := sessionCookieFrom(t, login(t, srv, "eddie", testPassword))

	if w := srv.call(http.MethodGet, "/api/v1/users", "", withCookie(cookie)); w.Code != http.StatusOK {
		t.Fatalf("editor should read users: got %d", w.Code)
	}
	// Demote to viewer: the same session must now be refused a write.
	st.Seed("users", map[string]any{
		"id": "usr-ed", "name": "eddie", "username": "eddie", "role": string(authz.RoleViewer),
	})
	if w := srv.call(http.MethodPost, "/api/v1/teams", `{"name":"x"}`, withCookie(cookie)); w.Code != http.StatusForbidden {
		t.Fatalf("demoted session should be forbidden a write: got %d", w.Code)
	}
	// Clear the role entirely: the session stops authenticating at all.
	st.Seed("users", map[string]any{
		"id": "usr-ed", "name": "eddie", "username": "eddie", "role": "",
	})
	if w := srv.call(http.MethodGet, "/api/v1/users", "", withCookie(cookie)); w.Code != http.StatusUnauthorized {
		t.Fatalf("role-less session should not authenticate: got %d", w.Code)
	}
}

// TestSessionPreferredOverAPIKey: when both are presented the person wins, so
// the audit trail names them rather than a shared key.
func TestSessionPreferredOverAPIKey(t *testing.T) {
	srv, _ := newSessionServer(t)
	srv.cfg.APIKeys = map[string]string{"shared-admin-key": string(authz.RoleAdmin)}
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-API-Key", "shared-admin-key") })
	var me map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if me["kind"] != authz.KindUser || me["id"] != "usr-admin" {
		t.Errorf("session must win over API key, got %+v", me)
	}
}

// TestStaleCookieFallsBackToAPIKey: a leftover cookie must not lock out a
// caller who also presents a valid key.
func TestStaleCookieFallsBackToAPIKey(t *testing.T) {
	srv, _ := newSessionServer(t)
	srv.cfg.APIKeys = map[string]string{"shared-admin-key": string(authz.RoleAdmin)}

	w := srv.call(http.MethodGet, "/api/v1/auth/me", "",
		withCookie(&http.Cookie{Name: sessionCookieName, Value: "long-dead-token"}),
		func(r *http.Request) { r.Header.Set("X-API-Key", "shared-admin-key") })
	if w.Code != http.StatusOK {
		t.Fatalf("stale cookie must fall through to the key: got %d, body %s", w.Code, w.Body.String())
	}
}

// TestLogoutRevokesSession.
func TestLogoutRevokesSession(t *testing.T) {
	srv, _ := newSessionServer(t)
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	if w := srv.call(http.MethodPost, "/api/v1/auth/logout", "", withCookie(cookie)); w.Code != http.StatusOK {
		t.Fatalf("logout: got %d", w.Code)
	}
	if w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(cookie)); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session still authenticates: got %d", w.Code)
	}
}

// TestChangeOwnPasswordSignsOutEverywhere. Changing a password is how someone
// reacts to a suspected compromise; leaving sessions alive would defeat it.
func TestChangeOwnPasswordSignsOutEverywhere(t *testing.T) {
	srv, _ := newSessionServer(t)
	first := sessionCookieFrom(t, login(t, srv, "ada", testPassword))
	second := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	body := `{"current_password":"` + testPassword + `","new_password":"a-brand-new-secret"}`
	if w := srv.call(http.MethodPost, "/api/v1/auth/password", body, withCookie(first)); w.Code != http.StatusOK {
		t.Fatalf("change password: got %d, body %s", w.Code, w.Body.String())
	}
	for name, c := range map[string]*http.Cookie{"acting": first, "other": second} {
		if w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(c)); w.Code != http.StatusUnauthorized {
			t.Errorf("%s session survived a password change: got %d", name, w.Code)
		}
	}
	srv.loginLimiter = newRateLimiter(loginRatePerSecond, loginBurst)
	srv.loginAccountLimiter = newRateLimiter(loginAccountRatePerSecond, loginAccountBurst)
	if w := login(t, srv, "ada", "a-brand-new-secret"); w.Code != http.StatusOK {
		t.Fatalf("new password does not work: got %d, body %s", w.Code, w.Body.String())
	}
}

// TestChangeOwnPasswordRequiresCurrent: the session proves identity, but an
// unattended browser must not become a permanent takeover.
func TestChangeOwnPasswordRequiresCurrent(t *testing.T) {
	srv, _ := newSessionServer(t)
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	w := srv.call(http.MethodPost, "/api/v1/auth/password",
		`{"current_password":"nope","new_password":"a-brand-new-secret"}`, withCookie(cookie))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401; body %s", w.Code, w.Body.String())
	}
	if w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(cookie)); w.Code != http.StatusOK {
		t.Error("a failed password change must not invalidate the session")
	}
}

// TestChangeOwnPasswordRejectsShort.
func TestChangeOwnPasswordRejectsShort(t *testing.T) {
	srv, _ := newSessionServer(t)
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))
	w := srv.call(http.MethodPost, "/api/v1/auth/password",
		`{"current_password":"`+testPassword+`","new_password":"short"}`, withCookie(cookie))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400; body %s", w.Code, w.Body.String())
	}
}

// TestViewerMayChangeOwnPasswordButNotAnyoneElses is the permission boundary
// around identity: /auth/password is open to every authenticated principal,
// /users/{id}/password is administration.
func TestViewerMayChangeOwnPasswordButNotAnyoneElses(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-vi", "vera", string(authz.RoleViewer), testPassword)
	cookie := sessionCookieFrom(t, login(t, srv, "vera", testPassword))

	own := `{"current_password":"` + testPassword + `","new_password":"vera-new-secret"}`
	if w := srv.call(http.MethodPost, "/api/v1/auth/password", own, withCookie(cookie)); w.Code != http.StatusOK {
		t.Fatalf("viewer changing own password: got %d, body %s", w.Code, w.Body.String())
	}
	srv.loginLimiter = newRateLimiter(loginRatePerSecond, loginBurst)
	srv.loginAccountLimiter = newRateLimiter(loginAccountRatePerSecond, loginAccountBurst)
	cookie = sessionCookieFrom(t, login(t, srv, "vera", "vera-new-secret"))
	w := srv.call(http.MethodPut, "/api/v1/users/usr-admin/password",
		`{"password":"hijacked-password"}`, withCookie(cookie))
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer setting another user's password: got %d, want 403", w.Code)
	}
}

// TestAdminSetsAndRemovesPassword covers the administrative half, including
// that both operations sign the target out.
func TestAdminSetsAndRemovesPassword(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-ed", "eddie", string(authz.RoleEditor), testPassword)
	eddie := sessionCookieFrom(t, login(t, srv, "eddie", testPassword))
	admin := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	w := srv.call(http.MethodPut, "/api/v1/users/usr-ed/password",
		`{"password":"reset-by-the-admin"}`, withCookie(admin))
	if w.Code != http.StatusOK {
		t.Fatalf("admin set password: got %d, body %s", w.Code, w.Body.String())
	}
	if w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(eddie)); w.Code != http.StatusUnauthorized {
		t.Error("an admin password reset must sign the target out")
	}
	srv.loginLimiter = newRateLimiter(loginRatePerSecond, loginBurst)
	srv.loginAccountLimiter = newRateLimiter(loginAccountRatePerSecond, loginAccountBurst)
	if w := login(t, srv, "eddie", "reset-by-the-admin"); w.Code != http.StatusOK {
		t.Fatalf("reset password does not work: got %d", w.Code)
	}

	if w := srv.call(http.MethodDelete, "/api/v1/users/usr-ed/password", "", withCookie(admin)); w.Code != http.StatusOK {
		t.Fatalf("admin remove password: got %d, body %s", w.Code, w.Body.String())
	}
	srv.loginLimiter = newRateLimiter(loginRatePerSecond, loginBurst)
	srv.loginAccountLimiter = newRateLimiter(loginAccountRatePerSecond, loginAccountBurst)
	if w := login(t, srv, "eddie", "reset-by-the-admin"); w.Code != http.StatusUnauthorized {
		t.Fatal("a removed password must stop working")
	}
}

// TestDeletingUserRevokesAccess: "this person is gone" must mean the session
// dies too, not merely that the roster row disappears.
func TestDeletingUserRevokesAccess(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-ed", "eddie", string(authz.RoleEditor), testPassword)
	eddie := sessionCookieFrom(t, login(t, srv, "eddie", testPassword))
	admin := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	if w := srv.call(http.MethodDelete, "/api/v1/users/usr-ed", "", withCookie(admin)); w.Code != http.StatusOK {
		t.Fatalf("delete user: got %d, body %s", w.Code, w.Body.String())
	}
	if w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(eddie)); w.Code != http.StatusUnauthorized {
		t.Error("deleted user's session still authenticates")
	}
	if _, ok, _ := st.GetUserPasswordHash(context.Background(), "usr-ed"); ok {
		t.Error("deleted user's credential was left behind")
	}
}

// TestCrossOriginWriteRejected covers the second lock behind SameSite=Strict:
// if the cookie attribute is ever relaxed, CSRF protection must not vanish
// with it.
func TestCrossOriginWriteRejected(t *testing.T) {
	srv, _ := newSessionServer(t)
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	w := srv.call(http.MethodPost, "/api/v1/teams", `{"name":"x"}`, withCookie(cookie),
		func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write: got %d, want 403", w.Code)
	}
	// A read is unaffected, and so is a write from our own origin.
	if w := srv.call(http.MethodGet, "/api/v1/users", "", withCookie(cookie),
		func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); w.Code != http.StatusOK {
		t.Errorf("cross-origin read should be allowed: got %d", w.Code)
	}
	if w := srv.call(http.MethodPost, "/api/v1/teams", `{"name":"x"}`, withCookie(cookie),
		func(r *http.Request) { r.Header.Set("Origin", "http://"+r.Host) }); w.Code != http.StatusCreated {
		t.Errorf("same-origin write should be allowed: got %d", w.Code)
	}
}

// TestAuthMethodsDescribesDeployment: the sign-in screen should not have to
// guess how this instance expects people to authenticate.
func TestAuthMethodsDescribesDeployment(t *testing.T) {
	srv, _ := newSessionServer(t)
	srv.cfg.APIKeys = map[string]string{"k": string(authz.RoleAdmin)}

	w := srv.call(http.MethodGet, "/api/v1/auth/methods", "")
	var methods map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &methods); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if methods["password"] != true || methods["api_key"] != true || methods["anonymous"] != false {
		t.Errorf("unexpected methods: %+v", methods)
	}
	// The edition travels with the doors, so a sign-in screen can explain a
	// door it cannot draw. Asserted against the constant rather than a literal:
	// this test runs in both editions and the answer is supposed to differ.
	if methods["edition"] != Edition {
		t.Errorf("edition = %v, want %q", methods["edition"], Edition)
	}
	if methods["sso_available"] != editionHasSSO {
		t.Errorf("sso_available = %v, want %v", methods["sso_available"], editionHasSSO)
	}
}

// TestLoginIsAudited: the whole point of user identity is that the trail names
// a person.
func TestLoginIsAudited(t *testing.T) {
	srv, st := newSessionServer(t)
	login(t, srv, "ada", testPassword)

	events := st.AuditEvents()
	if len(events) == 0 {
		t.Fatal("sign-in was not recorded")
	}
	last := events[len(events)-1]
	if last.Action != "auth.login" || last.ActorID != "usr-admin" || last.ActorKind != authz.KindUser {
		t.Errorf("sign-in must be attributed to the person, got %+v", last)
	}
	if last.ActorName != "ada" {
		t.Errorf("actor name = %q, want the username", last.ActorName)
	}
}

// TestFailedLoginIsAudited: a brute-force run must leave a trail. Without this
// only successful sign-ins were recorded, so the attempts worth reviewing were
// the ones the audit table did not hold.
func TestFailedLoginIsAudited(t *testing.T) {
	srv, st := newSessionServer(t)

	before := len(st.AuditEvents())
	w := login(t, srv, "ada", "wrong-password-here")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}

	events := st.AuditEvents()
	if len(events) != before+1 {
		t.Fatalf("failed sign-in left %d new events, want 1", len(events)-before)
	}
	ev := events[len(events)-1]
	if ev.Action != "auth.login_failed" {
		t.Fatalf("action = %q, want auth.login_failed", ev.Action)
	}
	// Attributed to the account that was targeted, so the record is reachable by
	// PseudonymiseAuditActor, which keys on actor_id.
	if ev.ActorID != "usr-admin" || ev.EntityID != "usr-admin" {
		t.Errorf("attempt on a real account must name it: %+v", ev)
	}
	if got := ev.Data["login"]; got != "ada" {
		t.Errorf("data.login = %v, want the attempted login", got)
	}
	raw, _ := json.Marshal(ev.Data)
	if strings.Contains(string(raw), "wrong-password-here") {
		t.Errorf("the attempted password must never be recorded: %s", raw)
	}
}

// TestFailedLoginForUnknownUserIsAuditedUnattributed: a login matching nobody
// still gets recorded, but with no actor — there is no data subject here, and
// inventing one would put a stranger's typo on a real person's trail.
func TestFailedLoginForUnknownUserIsAuditedUnattributed(t *testing.T) {
	srv, st := newSessionServer(t)

	if w := login(t, srv, "nobody", testPassword); w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}

	events := st.AuditEvents()
	ev := events[len(events)-1]
	if ev.Action != "auth.login_failed" {
		t.Fatalf("action = %q, want auth.login_failed", ev.Action)
	}
	if ev.ActorID != "" || ev.EntityID != "" {
		t.Errorf("unknown login must stay unattributed: %+v", ev)
	}
	if got := ev.Data["login"]; got != "nobody" {
		t.Errorf("data.login = %v, want the attempted login", got)
	}
}

// TestConfigCRUDIsAudited covers the second half of this iteration: mutating
// configuration now leaves a record, and that record carries field names
// rather than values.
func TestConfigCRUDIsAudited(t *testing.T) {
	srv, st := newSessionServer(t)
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	w := srv.call(http.MethodPost, "/api/v1/integrations",
		`{"name":"prod","type":"webhook","webhook_secret":"s3cr3t-value"}`, withCookie(cookie))
	if w.Code != http.StatusCreated {
		t.Fatalf("create integration: got %d, body %s", w.Code, w.Body.String())
	}

	var found bool
	for _, ev := range st.AuditEvents() {
		if ev.EntityType != "integration" {
			continue
		}
		found = true
		if ev.ActorID != "usr-admin" {
			t.Errorf("configuration change not attributed: %+v", ev)
		}
		raw, _ := json.Marshal(ev.Data)
		if strings.Contains(string(raw), "s3cr3t-value") {
			t.Errorf("audit data must record field names, not values: %s", raw)
		}
		if !strings.Contains(string(raw), "webhook_secret") {
			t.Errorf("audit data should name the changed field: %s", raw)
		}
	}
	if !found {
		t.Error("creating an integration left no audit record")
	}
}

// TestBootstrapAdminCreatesUsableAccount covers the break-glass path: a fresh
// database with no users must still be reachable.
func TestBootstrapAdminCreatesUsableAccount(t *testing.T) {
	srv, _ := newTestServer()
	srv.cfg.BootstrapAdminUsername = "breakglass"
	srv.cfg.BootstrapAdminPassword = testPassword
	if err := srv.bootstrapAdmin(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	w := login(t, srv, "breakglass", testPassword)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap admin cannot sign in: got %d, body %s", w.Code, w.Body.String())
	}
	cookie := sessionCookieFrom(t, w)
	if w := srv.call(http.MethodGet, "/api/v1/audit", "", withCookie(cookie)); w.Code != http.StatusOK {
		t.Errorf("bootstrap account is not an admin: got %d", w.Code)
	}
}

// TestBootstrapAdminPromotesExistingUser: the variables are also the recovery
// path after a forgotten password, which is useless if it refuses to act on an
// account that already exists.
func TestBootstrapAdminPromotesExistingUser(t *testing.T) {
	srv, st := newTestServer()
	seedUser(t, srv, st, "usr-vi", "vera", string(authz.RoleViewer), "")
	srv.cfg.BootstrapAdminUsername = "vera"
	srv.cfg.BootstrapAdminPassword = testPassword
	if err := srv.bootstrapAdmin(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if got := st.Row("users", "usr-vi")["role"]; got != string(authz.RoleAdmin) {
		t.Fatalf("role = %v, want admin", got)
	}
	if st.Count("users") != 1 {
		t.Errorf("bootstrap created a duplicate account: %d users", st.Count("users"))
	}
}

// TestBootstrapAdminRequiresBothVariables: a half-configured break-glass path
// is worse than none, because it looks configured.
func TestBootstrapAdminRequiresBothVariables(t *testing.T) {
	srv, _ := newTestServer()
	srv.cfg.BootstrapAdminUsername = "breakglass"
	if err := srv.bootstrapAdmin(context.Background()); err == nil {
		t.Fatal("username without password must be an error")
	}
	srv2, _ := newTestServer()
	if err := srv2.bootstrapAdmin(context.Background()); err != nil {
		t.Fatalf("neither set must be a no-op, got %v", err)
	}
}

// TestListAndRevokeOwnSessions covers the practical half of "a session is
// revocable": a forgotten sign-in on a shared machine can be dealt with without
// resorting to a password change.
func TestListAndRevokeOwnSessions(t *testing.T) {
	srv, _ := newSessionServer(t)
	first := sessionCookieFrom(t, login(t, srv, "ada", testPassword))
	second := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	list := func(cookie *http.Cookie) []map[string]any {
		t.Helper()
		w := srv.call(http.MethodGet, "/api/v1/auth/sessions", "", withCookie(cookie))
		if w.Code != http.StatusOK {
			t.Fatalf("list sessions: got %d, body %s", w.Code, w.Body.String())
		}
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.Items
	}

	items := list(second)
	if len(items) != 2 {
		t.Fatalf("got %d sessions, want 2", len(items))
	}
	// Exactly one row must be marked as the caller's own, or "revoke the others"
	// becomes guesswork.
	current, other := "", ""
	for _, item := range items {
		if item["current"] == true {
			current = item["id"].(string)
		} else {
			other = item["id"].(string)
		}
		if _, leaked := item["token_hash"]; leaked {
			t.Error("session listing leaks the token hash")
		}
	}
	if current == "" || other == "" {
		t.Fatalf("expected exactly one current session, got %v", items)
	}

	if w := srv.call(http.MethodDelete, "/api/v1/auth/sessions/"+other, "", withCookie(second)); w.Code != http.StatusOK {
		t.Fatalf("revoke other session: got %d, body %s", w.Code, w.Body.String())
	}
	if w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(first)); w.Code != http.StatusUnauthorized {
		t.Error("the revoked session still authenticates")
	}
	if w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(second)); w.Code != http.StatusOK {
		t.Error("revoking another session must not affect the caller's own")
	}
}

// TestCannotRevokeAnotherUsersSession: the update is scoped to the owner in
// SQL, so a guessed id reaches nothing — and the refusal does not distinguish
// "no such session" from "not yours".
func TestCannotRevokeAnotherUsersSession(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-ed", "eddie", string(authz.RoleEditor), testPassword)
	eddieCookie := sessionCookieFrom(t, login(t, srv, "eddie", testPassword))
	adaCookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	w := srv.call(http.MethodGet, "/api/v1/auth/sessions", "", withCookie(eddieCookie))
	var body struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Items) != 1 {
		t.Fatalf("eddie should see only his own session, got %d", len(body.Items))
	}
	eddieSession := body.Items[0]["id"].(string)

	if w := srv.call(http.MethodDelete, "/api/v1/auth/sessions/"+eddieSession, "", withCookie(adaCookie)); w.Code != http.StatusNotFound {
		t.Fatalf("cross-user revoke: got %d, want 404", w.Code)
	}
	if w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(eddieCookie)); w.Code != http.StatusOK {
		t.Error("another user's session was revoked")
	}
}

// TestViewerCanManageOwnSessions: session management is about the caller
// themselves, so it must not require a configuration role.
func TestViewerCanManageOwnSessions(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-vi", "vera", string(authz.RoleViewer), testPassword)
	vera := sessionCookieFrom(t, login(t, srv, "vera", testPassword))

	if w := srv.call(http.MethodGet, "/api/v1/auth/sessions", "", withCookie(vera)); w.Code != http.StatusOK {
		t.Fatalf("viewer listing own sessions: got %d, body %s", w.Code, w.Body.String())
	}
}

// TestAdminRevokesUserSessions is the "sign this person out everywhere" lever,
// for when an account is suspected compromised but the password is rotated
// separately.
func TestAdminRevokesUserSessions(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-ed", "eddie", string(authz.RoleEditor), testPassword)
	eddie := sessionCookieFrom(t, login(t, srv, "eddie", testPassword))
	admin := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	if w := srv.call(http.MethodDelete, "/api/v1/users/usr-ed/sessions", "", withCookie(admin)); w.Code != http.StatusOK {
		t.Fatalf("admin revoke: got %d, body %s", w.Code, w.Body.String())
	}
	if w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(eddie)); w.Code != http.StatusUnauthorized {
		t.Error("the target still has a live session")
	}
	// The password is untouched: this lever and a password reset are different
	// responses to different situations.
	srv.loginLimiter = newRateLimiter(loginRatePerSecond, loginBurst)
	srv.loginAccountLimiter = newRateLimiter(loginAccountRatePerSecond, loginAccountBurst)
	if w := login(t, srv, "eddie", testPassword); w.Code != http.StatusOK {
		t.Error("revoking sessions must not remove the ability to sign in again")
	}
}

// TestNonAdminCannotRevokeAnotherUsersSessions.
func TestNonAdminCannotRevokeAnotherUsersSessions(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-ed", "eddie", string(authz.RoleEditor), testPassword)
	eddie := sessionCookieFrom(t, login(t, srv, "eddie", testPassword))

	if w := srv.call(http.MethodDelete, "/api/v1/users/usr-admin/sessions", "", withCookie(eddie)); w.Code != http.StatusForbidden {
		t.Fatalf("editor revoking an admin's sessions: got %d, want 403", w.Code)
	}
}

// TestAuditCarriesRequestID: every event a single request produces must be
// findable together, and matchable against the access log line and the error
// response that carry the same id. Timestamps do not do that under load.
func TestAuditCarriesRequestID(t *testing.T) {
	srv, st := newSessionServer(t)
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	// Driven through the middleware, because that is what mints the id.
	handler := srv.withRequestID(http.HandlerFunc(srv.handleAPI))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/teams", strings.NewReader(`{"name":"audited"}`))
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create team: got %d, body %s", w.Code, w.Body.String())
	}
	rid := w.Header().Get("X-Request-ID")
	if rid == "" {
		t.Fatal("no X-Request-ID on the response")
	}

	var found bool
	for _, ev := range st.AuditEvents() {
		if ev.EntityType != "team" {
			continue
		}
		found = true
		if ev.RequestID != rid {
			t.Errorf("audit request_id = %q, want the response's %q", ev.RequestID, rid)
		}
	}
	if !found {
		t.Fatal("creating a team left no audit record")
	}

	// A caller-supplied id is propagated rather than replaced, so a trace that
	// starts upstream stays one trace.
	r = httptest.NewRequest(http.MethodPost, "/api/v1/teams", strings.NewReader(`{"name":"traced"}`))
	r.AddCookie(cookie)
	r.Header.Set("X-Request-ID", "upstream-trace-1")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	events := st.AuditEvents()
	if last := events[len(events)-1]; last.RequestID != "upstream-trace-1" {
		t.Errorf("caller-supplied request id was not propagated: %q", last.RequestID)
	}

	// And it is filterable, which is the query an incident review starts from.
	res, err := srv.eng.ListAuditEvents(t.Context(),
		map[string]any{"request_id": "upstream-trace-1"}, 50, 0)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if res["total"].(int) != 1 {
		t.Errorf("filter by request_id returned %v events, want 1", res["total"])
	}
}

// TestWorkerAuditHasNoRequestID: the worker has no request behind it, and an
// invented correlation id would be worse than none.
func TestWorkerAuditHasNoRequestID(t *testing.T) {
	srv, st := newSessionServer(t)
	if _, err := srv.eng.CreateTeam(t.Context(), map[string]any{"name": "unattended"}); err != nil {
		t.Fatalf("create team: %v", err)
	}
	events := st.AuditEvents()
	if last := events[len(events)-1]; last.RequestID != "" {
		t.Errorf("unattended event carries request_id %q, want empty", last.RequestID)
	}
}

// The sign-in limiter must throttle *failed* attempts, not successful ones.
//
// It used to charge every attempt, which made the control fire at the wrong
// target: five legitimate sign-ins from one egress IP — an office NAT, a CI
// runner, the e2e suite — exhausted the burst, and the sustained rate of one per
// ten seconds kept locking honest users out (this is exactly how the browser e2e
// suite went flaky: the sixth spec to sign in got a 429). Brute force is
// failures, so only failures are charged.
func TestLoginLimiterChargesFailuresNotSuccesses(t *testing.T) {
	srv, _ := newSessionServer(t)

	// Far more successful sign-ins than loginBurst: none of them may be refused.
	for i := 0; i < loginBurst*4; i++ {
		w := login(t, srv, "ada", testPassword)
		if w.Code != http.StatusOK {
			t.Fatalf("successful sign-in #%d was refused: %d %s", i+1, w.Code, w.Body.String())
		}
	}

	// Wrong passwords still exhaust the budget, and once it is gone the endpoint
	// stops even looking at the credentials.
	refused := false
	for i := 0; i < loginBurst+2; i++ {
		w := login(t, srv, "ada", "wrong-password")
		if w.Code == http.StatusTooManyRequests {
			refused = true
			break
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("failed sign-in #%d: got %d, want 401 or 429 (%s)", i+1, w.Code, w.Body.String())
		}
	}
	if !refused {
		t.Fatalf("%d wrong passwords in a row were never rate limited", loginBurst+2)
	}

	// And the lockout is real: a correct password does not slip through a spent
	// budget either, so the throttle cannot be washed away by guessing.
	if w := login(t, srv, "ada", testPassword); w.Code != http.StatusTooManyRequests {
		t.Errorf("after the budget was spent by failures, sign-in returned %d, want 429", w.Code)
	}
}

// TestLoginLimitedPerAccountAcrossAddresses covers the escape hatch the per-IP
// budget leaves open: a client inside a trusted proxy range picks its own
// X-Forwarded-For, so a fresh address per attempt means a fresh budget per
// attempt. The account being guessed is the one thing it cannot vary.
func TestLoginLimitedPerAccountAcrossAddresses(t *testing.T) {
	srv, _ := newSessionServer(t)
	srv.loginLimiter = newRateLimiter(loginRatePerSecond, loginBurst)
	srv.loginAccountLimiter = newRateLimiter(loginAccountRatePerSecond, loginAccountBurst)
	srv.trustedProxies = trustedRange(t, "10.0.0.0/8")

	fromNewAddress := func(n int) func(*http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr = "10.1.2.3:4444"
			r.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", n))
		}
	}
	throttled := false
	for i := 1; i <= loginAccountBurst+2; i++ {
		w := srv.call(http.MethodPost, "/api/v1/auth/login",
			`{"login":"ada","password":"wrong-password-here"}`, fromNewAddress(i))
		if w.Code == http.StatusTooManyRequests {
			throttled = true
			break
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, body %s", i, w.Code, w.Body.String())
		}
	}
	if !throttled {
		t.Fatal("guessing one account from a new address each time was never throttled")
	}
	// A different account still has its own budget: the limit must slow down
	// guessing, not turn one attacker into a service-wide outage.
	if w := srv.call(http.MethodPost, "/api/v1/auth/login",
		`{"login":"roscoe","password":"wrong-password-here"}`, fromNewAddress(99)); w.Code == http.StatusTooManyRequests {
		t.Error("another account was locked out by attempts against ada")
	}
}

// TestLoginAccountBudgetIsCaseInsensitive: logins are matched without regard to
// case, so the budget must be too — otherwise "Ada" and "ada" are two budgets.
func TestLoginAccountBudgetIsCaseInsensitive(t *testing.T) {
	if got := loginAccountKey("  Ada "); got != "ada" {
		t.Fatalf("got %q, want %q", got, "ada")
	}
}

// trustedRange parses one CIDR for tests that need clientIP to believe a
// forwarded address.
func trustedRange(t *testing.T, cidr string) []*net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("parse %s: %v", cidr, err)
	}
	return []*net.IPNet{n}
}
