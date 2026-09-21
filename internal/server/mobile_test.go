package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/storetest"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// mobile_test.go drives mobile sessions through handleAPI, because what is
// under test is authentication itself: a phone must be able to call the API
// with nothing but its session, and get exactly its owner's rights, capped.

func withBearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func withHeader(k, v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

// pairPhone signs username in on the web, issues a pairing code and redeems it
// the way the app does, returning the mobile session token.
func pairPhone(t *testing.T, srv *Server, username string) string {
	t.Helper()
	w := login(t, srv, username, testPassword)
	if w.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", username, w.Code, w.Body.String())
	}
	cookie := sessionCookieFrom(t, w)
	w = srv.call(http.MethodPost, "/api/v1/mobile/pairing", "", withCookie(cookie))
	if w.Code != http.StatusCreated {
		t.Fatalf("pairing: %d %s", w.Code, w.Body.String())
	}
	code := decode(t, w.Body.Bytes())["code"].(string)
	w = srv.call(http.MethodPost, "/api/v1/mobile/pairing/redeem",
		`{"code":"`+code+`","platform":"android","device_name":"Pixel"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("redeem: %d %s", w.Code, w.Body.String())
	}
	return decode(t, w.Body.Bytes())["token"].(string)
}

func me(t *testing.T, srv *Server, mutate ...func(*http.Request)) (int, map[string]any) {
	t.Helper()
	w := srv.call(http.MethodGet, "/api/v1/auth/me", "", mutate...)
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	return w.Code, decode(t, w.Body.Bytes())
}

// TestMobilePairingIssuesUsableSession is the core of the change: before it, a
// mobile session could not authenticate a request on its own at all.
func TestMobilePairingIssuesUsableSession(t *testing.T) {
	srv, st := newSessionServer(t)
	token := pairPhone(t, srv, "ada")

	if !strings.HasPrefix(token, "nxm_") {
		t.Errorf("token %q lacks the mobile prefix authentication dispatches on", token)
	}
	code, got := me(t, srv, withBearer(token))
	if code != http.StatusOK {
		t.Fatalf("/auth/me with mobile bearer: %d", code)
	}
	if got["id"] != "usr-admin" || got["kind"] != authz.KindUser {
		t.Errorf("mobile session must resolve to its person, got %+v", got)
	}
	// ada is an admin; her phone is not.
	if got["role"] != string(authz.RoleResponder) {
		t.Errorf("role = %v, want admin capped to responder", got["role"])
	}
	// The legacy header is the same credential.
	if code, _ := me(t, srv, withHeader("X-Mobile-Session", token)); code != http.StatusOK {
		t.Errorf("/auth/me with X-Mobile-Session: %d", code)
	}

	// Only the hash is stored.
	for _, ev := range st.AuditEvents() {
		if strings.Contains(ev.EntityID+ev.ActorID, token) {
			t.Errorf("token leaked into audit: %+v", ev)
		}
	}
	sessions := findRows(st, "mobile_sessions", "user_id", "usr-admin")
	if len(sessions) != 1 {
		t.Fatalf("want one mobile session, got %d", len(sessions))
	}
	if sessions[0]["token"] != authz.HashSessionToken(token) {
		t.Errorf("stored token = %v, want the hash of the issued token", sessions[0]["token"])
	}
}

func findRows(st *storetest.Store, collection, key, value string) []map[string]any {
	items, _ := st.ListItemsIn(context.Background(), collection, key, []any{value})
	return items
}

func TestMobilePairingCodeIsOneTime(t *testing.T) {
	srv, _ := newSessionServer(t)
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))
	w := srv.call(http.MethodPost, "/api/v1/mobile/pairing", "", withCookie(cookie))
	code := decode(t, w.Body.Bytes())["code"].(string)

	// Typed by hand: lower case, no dash.
	typed := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	body := `{"code":"` + typed + `","platform":"android"}`
	if w := srv.call(http.MethodPost, "/api/v1/mobile/pairing/redeem", body); w.Code != http.StatusCreated {
		t.Fatalf("first redeem: %d %s", w.Code, w.Body.String())
	}
	if w := srv.call(http.MethodPost, "/api/v1/mobile/pairing/redeem", body); w.Code != http.StatusUnauthorized {
		t.Errorf("second redeem: %d, want 401", w.Code)
	}
	if w := srv.call(http.MethodPost, "/api/v1/mobile/pairing/redeem", `{"code":"AAAAA-AAAAA","platform":"android"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown code: %d, want 401", w.Code)
	}
}

// TestMobilePairingNeedsAPerson: an API key has nobody to sign a phone in as.
func TestMobilePairingNeedsAPerson(t *testing.T) {
	srv, _ := newSessionServer(t)
	srv.cfg.APIKeys = map[string]string{"k": string(authz.RoleAdmin)}
	w := srv.call(http.MethodPost, "/api/v1/mobile/pairing", "", withHeader("X-API-Key", "k"))
	if w.Code != http.StatusForbidden {
		t.Errorf("pairing with an API key: %d, want 403", w.Code)
	}
}

// TestMobileTokenDoesNotFallThrough: an invalid mobile token next to a valid
// cookie is a failed sign-in, not a request made with the cookie.
func TestMobileTokenDoesNotFallThrough(t *testing.T) {
	srv, _ := newSessionServer(t)
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))
	if code, _ := me(t, srv, withCookie(cookie), withHeader("X-Mobile-Session", "nxm_bogus")); code != http.StatusUnauthorized {
		t.Errorf("bogus mobile token with a valid cookie: %d, want 401", code)
	}
}

func TestMobileSessionSignOut(t *testing.T) {
	srv, _ := newSessionServer(t)
	token := pairPhone(t, srv, "ada")
	if w := srv.call(http.MethodDelete, "/api/v1/mobile/sessions/current", "", withBearer(token)); w.Code != http.StatusOK {
		t.Fatalf("sign out: %d %s", w.Code, w.Body.String())
	}
	if code, _ := me(t, srv, withBearer(token)); code != http.StatusUnauthorized {
		t.Errorf("revoked session still authenticates: %d", code)
	}
}

func TestMobileSessionExpires(t *testing.T) {
	srv, st := newSessionServer(t)
	token := pairPhone(t, srv, "ada")
	sess := findRows(st, "mobile_sessions", "user_id", "usr-admin")[0]
	sess["expires_at"] = utils.ToISO(time.Now().Add(-time.Minute))
	st.Seed("mobile_sessions", sess)
	if code, _ := me(t, srv, withBearer(token)); code != http.StatusUnauthorized {
		t.Errorf("expired session authenticates: %d", code)
	}
}

// TestMobileSessionIsExtendedByUse: a session in use slides forward; the
// extension happens at most daily, so a fresh one is left alone.
func TestMobileSessionIsExtendedByUse(t *testing.T) {
	srv, st := newSessionServer(t)
	token := pairPhone(t, srv, "ada")
	sess := findRows(st, "mobile_sessions", "user_id", "usr-admin")[0]
	soon := utils.ToISO(time.Now().Add(48 * time.Hour))
	sess["expires_at"] = soon
	st.Seed("mobile_sessions", sess)

	if code, _ := me(t, srv, withBearer(token)); code != http.StatusOK {
		t.Fatalf("session near expiry must still work: %d", code)
	}
	got := st.Row("mobile_sessions", utils.StrVal(sess, "id"))["expires_at"]
	exp, err := time.Parse(time.RFC3339, utils.StrVal(map[string]any{"v": got}, "v"))
	if err != nil || exp.Before(time.Now().Add(29*24*time.Hour)) {
		t.Errorf("expires_at = %v, want extended to ~30 days", got)
	}
}

// TestMobileViewerStaysReadOnly: the cap only lowers a role.
func TestMobileViewerStaysReadOnly(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-vic", "vic", string(authz.RoleViewer), testPassword)
	st.Seed("alert_groups", map[string]any{"id": "g1", "status": "open", "logs": []any{}})
	token := pairPhone(t, srv, "vic")
	w := srv.call(http.MethodPost, "/api/v1/alert-groups/g1/acknowledge", "", withBearer(token))
	if w.Code != http.StatusForbidden {
		t.Errorf("viewer's phone acknowledged: %d", w.Code)
	}
}

// TestMobileSessionIsTeamScoped replaces the old guarantee that a mobile
// action is attributed to the session's user, and adds the one that was
// missing: the session could act on any team's group.
func TestMobileSessionIsTeamScoped(t *testing.T) {
	srv, st := scopedFixture(t)
	token := pairPhone(t, srv, "ada")

	for _, path := range []string{
		"/api/v1/alert-groups/grp-b/acknowledge",
		"/api/v1/mobile/alert-groups/grp-b/acknowledge",
	} {
		w := srv.call(http.MethodPost, path, "", withHeader("X-Mobile-Session", token))
		if w.Code == http.StatusOK {
			t.Errorf("%s: ada's phone acted on team-b's group", path)
		}
	}
	if st.Row("alert_groups", "grp-b")["status"] != "open" {
		t.Fatal("team-b's group changed")
	}

	w := srv.call(http.MethodPost, "/api/v1/mobile/alert-groups/grp-a/acknowledge", "", withHeader("X-Mobile-Session", token))
	if w.Code != http.StatusOK {
		t.Fatalf("own team's group: %d %s", w.Code, w.Body.String())
	}
	ref, _ := st.Row("alert_groups", "grp-a")["acknowledged_by"].(map[string]any)
	if ref["id"] != "usr-ada" {
		t.Errorf("acknowledged_by = %v, want usr-ada", ref)
	}
}

// TestMobileLegacyEndpoints keeps the first mobile API working for a session
// issued by an administrator, and answering 400 to a caller with no session.
func TestMobileLegacyEndpoints(t *testing.T) {
	srv, st := newSessionServer(t)
	ctx := context.Background()
	dev, err := srv.eng.RegisterMobileDevice(ctx, map[string]any{"user_id": "usr-admin", "platform": "ios", "push_token": "tok"})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	sess, err := srv.eng.CreateMobileSession(ctx, map[string]any{"user_id": "usr-admin", "device_id": dev["id"]})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token := sess["token"].(string)
	st.Seed("alert_groups", map[string]any{"id": "mg1", "status": "open", "logs": []any{}})

	hdr := withHeader("X-Mobile-Session", token)
	w := srv.call(http.MethodGet, "/api/v1/mobile/dashboard", "", hdr)
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), token) {
		t.Error("dashboard echoes the session token")
	}
	if w := srv.call(http.MethodPost, "/api/v1/mobile/alert-groups/mg1/acknowledge", "", hdr); w.Code != http.StatusOK {
		t.Fatalf("mobile ack: %d %s", w.Code, w.Body.String())
	}
	if st.Row("alert_groups", "mg1")["status"] != "acknowledged" {
		t.Error("group not acknowledged via mobile")
	}

	srv.cfg.APIKeys = map[string]string{"k": string(authz.RoleAdmin)}
	if w := srv.call(http.MethodGet, "/api/v1/mobile/dashboard", "", withHeader("X-API-Key", "k")); w.Code != http.StatusBadRequest {
		t.Errorf("dashboard with an API key: %d, want 400", w.Code)
	}
}

// TestLostPhoneCanBeSignedOutFromTheWeb: a lost phone cannot sign itself out,
// so its owner must be able to, and nobody else may.
func TestLostPhoneCanBeSignedOutFromTheWeb(t *testing.T) {
	srv, st := newSessionServer(t)
	seedUser(t, srv, st, "usr-bob", "bob", string(authz.RoleResponder), testPassword)
	adaPhone := pairPhone(t, srv, "ada")
	bobPhone := pairPhone(t, srv, "bob")
	bobSession := findRows(st, "mobile_sessions", "user_id", "usr-bob")[0]["id"].(string)

	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))
	w := srv.call(http.MethodGet, "/api/v1/mobile/sessions", "", withCookie(ada))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	items := decode(t, w.Body.Bytes())["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["device_name"] != "Pixel" {
		t.Fatalf("ada's phones = %v, want her one Pixel", items)
	}
	if strings.Contains(w.Body.String(), "token") {
		t.Errorf("session list carries a token field: %s", w.Body.String())
	}

	if w := srv.call(http.MethodDelete, "/api/v1/mobile/sessions/"+bobSession, "", withCookie(ada)); w.Code != http.StatusNotFound {
		t.Errorf("ada revoking bob's phone: %d, want 404", w.Code)
	}
	if code, _ := me(t, srv, withBearer(bobPhone)); code != http.StatusOK {
		t.Errorf("bob's phone was signed out by ada: %d", code)
	}

	adaSession := items[0].(map[string]any)["id"].(string)
	if w := srv.call(http.MethodDelete, "/api/v1/mobile/sessions/"+adaSession, "", withCookie(ada)); w.Code != http.StatusOK {
		t.Fatalf("revoke own phone: %d %s", w.Code, w.Body.String())
	}
	if code, _ := me(t, srv, withBearer(adaPhone)); code != http.StatusUnauthorized {
		t.Errorf("revoked phone still authenticates: %d", code)
	}
}
