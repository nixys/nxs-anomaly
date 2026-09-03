package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// scope_test.go covers team scoping end to end through handleAPI, because the
// property under test spans three layers: the server resolves membership, the
// engine narrows the query, and the store applies the filter.

// scopedFixture builds a deployment with two teams, one integration each, one
// unassigned integration, and an alert group per integration.
//
//	team-a: ada (responder)      → int-a → grp-a
//	team-b: bob (responder)      → int-b → grp-b
//	(none): int-free             → grp-free
func scopedFixture(t *testing.T) (*Server, *storetest.Store) {
	t.Helper()
	srv, st := newTestServer()
	srv.cfg.TeamScoping = true

	seedUser(t, srv, st, "usr-ada", "ada", string(authz.RoleResponder), testPassword)
	seedUser(t, srv, st, "usr-bob", "bob", string(authz.RoleResponder), testPassword)
	seedUser(t, srv, st, "usr-nomad", "nomad", string(authz.RoleResponder), testPassword)
	seedUser(t, srv, st, "usr-root", "root", string(authz.RoleAdmin), testPassword)

	st.Seed("teams",
		map[string]any{"id": "team-a", "name": "team-a", "member_ids": []any{"usr-ada"}},
		map[string]any{"id": "team-b", "name": "team-b", "member_ids": []any{"usr-bob"}},
	)
	st.Seed("integrations",
		map[string]any{"id": "int-a", "name": "a", "key": "ka", "team_id": "team-a"},
		map[string]any{"id": "int-b", "name": "b", "key": "kb", "team_id": "team-b"},
		map[string]any{"id": "int-free", "name": "free", "key": "kf"},
	)
	st.Seed("alert_groups",
		map[string]any{"id": "grp-a", "integration_id": "int-a", "status": "open", "logs": []any{}},
		map[string]any{"id": "grp-b", "integration_id": "int-b", "status": "open", "logs": []any{}},
		map[string]any{"id": "grp-free", "integration_id": "int-free", "status": "open", "logs": []any{}},
	)
	// Notifications carry the integration denormalised from their group, which
	// is what makes delivery records scopeable at all.
	st.Seed("notifications",
		map[string]any{"id": "ntf-a", "alert_group_id": "grp-a", "integration_id": "int-a", "channel": "webhook", "status": "delivered"},
		map[string]any{"id": "ntf-b", "alert_group_id": "grp-b", "integration_id": "int-b", "channel": "webhook", "status": "delivered"},
		map[string]any{"id": "ntf-free", "alert_group_id": "grp-free", "integration_id": "int-free", "channel": "webhook", "status": "delivered"},
	)
	st.Seed("notification_delivery_attempts",
		map[string]any{"id": "att-a", "notification_id": "ntf-a", "attempt": 1, "status": "delivered"},
		map[string]any{"id": "att-b", "notification_id": "ntf-b", "attempt": 1, "status": "delivered"},
	)
	return srv, st
}

// TestScopedNotificationsAreHidden closes the gap left open when scoping was
// first added: delivery records carry notification message text, and were
// readable by any authenticated user regardless of team.
func TestScopedNotificationsAreHidden(t *testing.T) {
	srv, _ := scopedFixture(t)
	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	assertIDs(t, "notifications", listIDs(t, srv, "/api/v1/notifications", ada),
		[]string{"ntf-a", "ntf-free"})

	// A filter naming another team's group is intersected with the scope, not
	// substituted for it.
	assertIDs(t, "filtered by a foreign group",
		listIDs(t, srv, "/api/v1/notifications?alert_group_id=grp-b", ada), nil)
}

// TestScopedDeliveryAttemptsFollowTheirNotification: attempts have no team of
// their own, so they inherit the notification's, and the unfiltered feed is
// refused rather than served.
func TestScopedDeliveryAttemptsFollowTheirNotification(t *testing.T) {
	srv, _ := scopedFixture(t)
	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	if w := srv.call(http.MethodGet, "/api/v1/delivery-attempts?notification_id=ntf-a", "", withCookie(ada)); w.Code != http.StatusOK {
		t.Fatalf("own team attempts: got %d, body %s", w.Code, w.Body.String())
	}
	if w := srv.call(http.MethodGet, "/api/v1/delivery-attempts?notification_id=ntf-b", "", withCookie(ada)); w.Code != http.StatusForbidden {
		t.Errorf("foreign attempts: got %d, want 403 (%s)", w.Code, w.Body.String())
	}
	// Without a notification_id the query would be every attempt in the
	// deployment — refused for a scoped caller.
	if w := srv.call(http.MethodGet, "/api/v1/delivery-attempts", "", withCookie(ada)); w.Code != http.StatusBadRequest {
		t.Errorf("unfiltered attempts feed: got %d, want 400 (%s)", w.Code, w.Body.String())
	}
}

// TestUnscopedDeliveryAttemptsFeedStillWorks: the whole-deployment feed is what
// an operator uses to debug delivery, and must survive for unscoped callers.
func TestUnscopedDeliveryAttemptsFeedStillWorks(t *testing.T) {
	srv, _ := scopedFixture(t)
	root := sessionCookieFrom(t, login(t, srv, "root", testPassword))
	if w := srv.call(http.MethodGet, "/api/v1/delivery-attempts", "", withCookie(root)); w.Code != http.StatusOK {
		t.Fatalf("admin attempts feed: got %d, body %s", w.Code, w.Body.String())
	}
	assertIDs(t, "notifications", listIDs(t, srv, "/api/v1/notifications", root),
		[]string{"ntf-a", "ntf-b", "ntf-free"})
}

// listIDs returns the ids in a list response.
func listIDs(t *testing.T, srv *Server, path string, cookie *http.Cookie) []string {
	t.Helper()
	w := srv.call(http.MethodGet, path, "", withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: got %d, body %s", path, w.Code, w.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	ids := make([]string, 0, len(body.Items))
	for _, item := range body.Items {
		ids = append(ids, item["id"].(string))
	}
	return ids
}

func assertIDs(t *testing.T, what string, got, want []string) {
	t.Helper()
	set := map[string]bool{}
	for _, id := range got {
		set[id] = true
	}
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
	for _, id := range want {
		if !set[id] {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
	}
}

// TestScopedListsHideOtherTeams is the core read boundary, including the rule
// that makes the whole feature safe to enable: unassigned objects stay visible.
func TestScopedListsHideOtherTeams(t *testing.T) {
	srv, _ := scopedFixture(t)
	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	assertIDs(t, "integrations", listIDs(t, srv, "/api/v1/integrations", ada),
		[]string{"int-a", "int-free"})
	assertIDs(t, "alert groups", listIDs(t, srv, "/api/v1/alert-groups", ada),
		[]string{"grp-a", "grp-free"})
}

// TestScopingOffChangesNothing: the flag is the whole switch, and with it off
// the previous behaviour must be exactly preserved.
func TestScopingOffChangesNothing(t *testing.T) {
	srv, _ := scopedFixture(t)
	srv.cfg.TeamScoping = false
	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	assertIDs(t, "integrations", listIDs(t, srv, "/api/v1/integrations", ada),
		[]string{"int-a", "int-b", "int-free"})
	assertIDs(t, "alert groups", listIDs(t, srv, "/api/v1/alert-groups", ada),
		[]string{"grp-a", "grp-b", "grp-free"})
}

// TestAdminIsNotScoped: an admin hands out roles and reads the audit trail, so
// a team boundary would be theatre — and exempting them keeps a way in when
// every team's membership is wrong.
func TestAdminIsNotScoped(t *testing.T) {
	srv, _ := scopedFixture(t)
	root := sessionCookieFrom(t, login(t, srv, "root", testPassword))
	assertIDs(t, "integrations", listIDs(t, srv, "/api/v1/integrations", root),
		[]string{"int-a", "int-b", "int-free"})
}

// TestUserWithNoTeamsSeesOnlyUnassigned pins the direction of the rule:
// belonging to no team narrows the view to unassigned objects. It never widens
// it, so removing someone from a team cannot grant access.
func TestUserWithNoTeamsSeesOnlyUnassigned(t *testing.T) {
	srv, _ := scopedFixture(t)
	nomad := sessionCookieFrom(t, login(t, srv, "nomad", testPassword))

	assertIDs(t, "integrations", listIDs(t, srv, "/api/v1/integrations", nomad),
		[]string{"int-free"})
	assertIDs(t, "alert groups", listIDs(t, srv, "/api/v1/alert-groups", nomad),
		[]string{"grp-free"})
}

// TestScopedFilterCannotWidenTheScope: an explicit integration_id filter is
// intersected with the boundary, not substituted for it.
func TestScopedFilterCannotWidenTheScope(t *testing.T) {
	srv, _ := scopedFixture(t)
	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	assertIDs(t, "own integration filter",
		listIDs(t, srv, "/api/v1/alert-groups?integration_id=int-a", ada), []string{"grp-a"})
	assertIDs(t, "foreign integration filter",
		listIDs(t, srv, "/api/v1/alert-groups?integration_id=int-b", ada), nil)
}

// TestScopedGetIsRefused covers direct addressing by id, where a list filter
// cannot help.
func TestScopedGetIsRefused(t *testing.T) {
	srv, _ := scopedFixture(t)
	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/integrations/int-a", http.StatusOK},
		{"/api/v1/integrations/int-free", http.StatusOK},
		{"/api/v1/integrations/int-b", http.StatusForbidden},
		{"/api/v1/alert-groups/grp-a", http.StatusOK},
		{"/api/v1/alert-groups/grp-b", http.StatusForbidden},
	} {
		if w := srv.call(http.MethodGet, tc.path, "", withCookie(ada)); w.Code != tc.want {
			t.Errorf("GET %s: got %d, want %d (%s)", tc.path, w.Code, tc.want, w.Body.String())
		}
	}
}

// TestScopedResponderCannotActOnOtherTeams is the acceptance criterion from the
// backlog: a responder may acknowledge their own team's alerts and nobody
// else's.
func TestScopedResponderCannotActOnOtherTeams(t *testing.T) {
	srv, _ := scopedFixture(t)
	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	if w := srv.call(http.MethodPost, "/api/v1/alert-groups/grp-a/acknowledge", "", withCookie(ada)); w.Code != http.StatusOK {
		t.Fatalf("own team acknowledge: got %d, body %s", w.Code, w.Body.String())
	}
	for _, path := range []string{
		"/api/v1/alert-groups/grp-b/acknowledge",
		"/api/v1/alert-groups/grp-b/resolve",
		"/api/v1/alert-groups/grp-b/unresolve",
		"/api/v1/alert-groups/grp-b/unacknowledge",
		"/api/v1/alert-groups/grp-b/silence",
	} {
		if w := srv.call(http.MethodPost, path, "{}", withCookie(ada)); w.Code != http.StatusForbidden {
			t.Errorf("POST %s: got %d, want 403 (%s)", path, w.Code, w.Body.String())
		}
	}
	// The refusal must not reveal which team owns the group.
	w := srv.call(http.MethodPost, "/api/v1/alert-groups/grp-b/acknowledge", "", withCookie(ada))
	if strings.Contains(w.Body.String(), "team-b") {
		t.Errorf("refusal names the owning team: %s", w.Body.String())
	}
}

// TestScopedBulkFiltersRatherThanFails: refusing a whole batch because one id
// is out of reach would make a UI selection unusable, so the blocked ids are
// reported instead.
func TestScopedBulkFiltersRatherThanFails(t *testing.T) {
	srv, st := scopedFixture(t)
	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	w := srv.call(http.MethodPost, "/api/v1/alert-groups/bulk-acknowledge",
		`{"group_ids":["grp-a","grp-b","grp-free"]}`, withCookie(ada))
	if w.Code != http.StatusOK {
		t.Fatalf("bulk acknowledge: got %d, body %s", w.Code, w.Body.String())
	}
	var out struct {
		Acknowledged []string `json:"acknowledged"`
		Forbidden    []string `json:"forbidden"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertIDs(t, "acknowledged", out.Acknowledged, []string{"grp-a", "grp-free"})
	assertIDs(t, "forbidden", out.Forbidden, []string{"grp-b"})

	if st.Row("alert_groups", "grp-b")["acknowledged_at"] != nil {
		t.Error("a group from another team was acknowledged by the bulk path")
	}
}

// TestScopedEditorCannotReassignAcrossTeams: creating or moving an object into
// a team you do not belong to would be a way around the boundary.
func TestScopedEditorCannotReassignAcrossTeams(t *testing.T) {
	srv, st := scopedFixture(t)
	seedUser(t, srv, st, "usr-ed", "eddie", string(authz.RoleEditor), testPassword)
	st.Seed("teams", map[string]any{
		"id": "team-a", "name": "team-a", "member_ids": []any{"usr-ada", "usr-ed"},
	})
	eddie := sessionCookieFrom(t, login(t, srv, "eddie", testPassword))

	if w := srv.call(http.MethodPost, "/api/v1/integrations",
		`{"name":"mine","team_id":"team-a"}`, withCookie(eddie)); w.Code != http.StatusCreated {
		t.Fatalf("create in own team: got %d, body %s", w.Code, w.Body.String())
	}
	if w := srv.call(http.MethodPost, "/api/v1/integrations",
		`{"name":"theirs","team_id":"team-b"}`, withCookie(eddie)); w.Code != http.StatusForbidden {
		t.Errorf("create in foreign team: got %d, want 403", w.Code)
	}
	// Moving one of ours out of reach is refused from the other side.
	if w := srv.call(http.MethodPut, "/api/v1/integrations/int-a",
		`{"team_id":"team-b"}`, withCookie(eddie)); w.Code != http.StatusForbidden {
		t.Errorf("reassign to foreign team: got %d, want 403", w.Code)
	}
	// And editing a foreign integration at all is refused.
	if w := srv.call(http.MethodPut, "/api/v1/integrations/int-b",
		`{"name":"hijacked"}`, withCookie(eddie)); w.Code != http.StatusForbidden {
		t.Errorf("edit foreign integration: got %d, want 403", w.Code)
	}
	if w := srv.call(http.MethodDelete, "/api/v1/integrations/int-b", "", withCookie(eddie)); w.Code != http.StatusForbidden {
		t.Errorf("delete foreign integration: got %d, want 403", w.Code)
	}
}

// TestMeReportsScope: an empty list should read as a boundary, not as "nothing
// has happened yet", and the UI needs to be told which it is.
func TestMeReportsScope(t *testing.T) {
	srv, _ := scopedFixture(t)
	ada := sessionCookieFrom(t, login(t, srv, "ada", testPassword))

	w := srv.call(http.MethodGet, "/api/v1/auth/me", "", withCookie(ada))
	var me struct {
		TeamScoped bool     `json:"team_scoped"`
		TeamIDs    []string `json:"team_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !me.TeamScoped {
		t.Error("team_scoped must be reported")
	}
	assertIDs(t, "team_ids", me.TeamIDs, []string{"team-a"})
}

// TestAPIKeyIsNotScoped: keys are automation, they belong to no team, and the
// ingest and plugin paths depend on them reaching everything.
func TestAPIKeyIsNotScoped(t *testing.T) {
	srv, _ := scopedFixture(t)
	srv.cfg.APIKeys = map[string]string{"k": string(authz.RoleResponder)}

	w := srv.call(http.MethodGet, "/api/v1/integrations", "",
		func(r *http.Request) { r.Header.Set("X-API-Key", "k") })
	var body struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Items) != 3 {
		t.Errorf("API key actor saw %d integrations, want all 3", len(body.Items))
	}
}
