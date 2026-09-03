package tests

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// identity_integration_test.go exercises the credential and session tables
// against a real PostgreSQL. The unit tests use an in-memory model of these
// tables, which cannot catch what actually goes wrong here: SQL that does not
// match the migration, an index predicate that excludes rows the lookup needs,
// or a cascade that does not fire.

// seedIdentityUser creates the user row the credential and session rows point
// at, so the foreign keys from migration 0019 have something to reference.
func seedIdentityUser(t *testing.T, ctx context.Context, st store.PostgreSQLStore, username string) string {
	t.Helper()
	id := utils.MakeID("usr")
	ts := utils.ToISO(utils.UTCNow())
	err := st.UpsertItem(ctx, "users", map[string]any{
		"id": id, "name": username, "username": username,
		"email": username + "@example.test", "role": string(authz.RoleEditor),
		"priority": "medium", "on_duty": false,
		"created_at": ts, "updated_at": ts,
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func TestIdentityCredentialsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	userID := seedIdentityUser(t, ctx, st, "creduser")

	if _, ok, err := st.GetUserPasswordHash(ctx, userID); err != nil || ok {
		t.Fatalf("fresh user: ok=%v err=%v, want no credential", ok, err)
	}

	hash, err := authz.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := st.SetUserPassword(ctx, userID, hash); err != nil {
		t.Fatalf("set password: %v", err)
	}
	got, ok, err := st.GetUserPasswordHash(ctx, userID)
	if err != nil || !ok || got != hash {
		t.Fatalf("get hash: got=%q ok=%v err=%v", got, ok, err)
	}

	// Setting again must replace, not conflict: an admin resetting a password
	// twice is entirely ordinary.
	hash2, _ := authz.HashPassword("a-brand-new-secret")
	if err := st.SetUserPassword(ctx, userID, hash2); err != nil {
		t.Fatalf("replace password: %v", err)
	}
	if got, _, _ := st.GetUserPasswordHash(ctx, userID); got != hash2 {
		t.Errorf("password was not replaced")
	}

	if n, err := st.CountUsersWithPassword(ctx); err != nil || n != 1 {
		t.Errorf("count = %d, err = %v, want 1", n, err)
	}

	if err := st.DeleteUserPassword(ctx, userID); err != nil {
		t.Fatalf("delete password: %v", err)
	}
	if _, ok, _ := st.GetUserPasswordHash(ctx, userID); ok {
		t.Error("credential survived deletion")
	}
	// Deleting a credential that is not there is how "remove password" behaves
	// for a user who never had one, and must not be an error.
	if err := st.DeleteUserPassword(ctx, userID); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

func TestIdentityFindUserByLogin(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	userID := seedIdentityUser(t, ctx, st, "loginuser")

	for _, login := range []string{
		"loginuser", "LoginUser", // username, case-insensitively
		"loginuser@example.test", "LOGINUSER@EXAMPLE.TEST", // e-mail, likewise
	} {
		user, err := st.FindUserByLogin(ctx, login)
		if err != nil {
			t.Fatalf("%s: %v", login, err)
		}
		if user == nil || utils.StrVal(user, "id") != userID {
			t.Errorf("%s: got %v, want user %s", login, user, userID)
		}
	}
	if user, err := st.FindUserByLogin(ctx, "nobody"); err != nil || user != nil {
		t.Errorf("unknown login: got %v, err %v, want nil", user, err)
	}
}

func TestIdentitySessionLifecycle(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	userID := seedIdentityUser(t, ctx, st, "sessionuser")
	token, err := authz.NewSessionToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	hash := authz.HashSessionToken(token)
	sess := store.WebSession{
		ID: utils.MakeID("wses"), TokenHash: hash, UserID: userID,
		ExpiresAt: time.Now().Add(time.Hour), RequestIP: "192.0.2.10", UserAgent: "go-test",
	}
	if err := st.CreateWebSession(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	user, sessionID, err := st.FindSessionUser(ctx, hash)
	if err != nil {
		t.Fatalf("find session: %v", err)
	}
	if sessionID != sess.ID || user == nil || utils.StrVal(user, "id") != userID {
		t.Fatalf("session resolved to sessionID=%q user=%v", sessionID, user)
	}
	// The join must read the user row live: this is what makes a role change
	// take effect without waiting for the session to expire.
	if utils.StrVal(user, "username") != "sessionuser" {
		t.Errorf("joined user row is wrong: %v", user)
	}

	if _, id, _ := st.FindSessionUser(ctx, authz.HashSessionToken("some-other-token")); id != "" {
		t.Error("an unknown token resolved to a session")
	}

	if err := st.RevokeWebSession(ctx, hash); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, id, _ := st.FindSessionUser(ctx, hash); id != "" {
		t.Error("a revoked session still resolves")
	}
}

func TestIdentityExpiredSessionDoesNotResolve(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	userID := seedIdentityUser(t, ctx, st, "expireduser")
	hash := authz.HashSessionToken("expired-token")
	err := st.CreateWebSession(ctx, store.WebSession{
		ID: utils.MakeID("wses"), TokenHash: hash, UserID: userID,
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, id, _ := st.FindSessionUser(ctx, hash); id != "" {
		t.Fatal("an expired session resolved; the absolute lifetime is not enforced in SQL")
	}
}

func TestIdentityRevokeUserSessionsAndSweep(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	userID := seedIdentityUser(t, ctx, st, "multiuser")
	otherID := seedIdentityUser(t, ctx, st, "otheruser")

	hashes := []string{}
	for i, owner := range []string{userID, userID, otherID} {
		h := authz.HashSessionToken(utils.MakeID("tok") + string(rune('a'+i)))
		hashes = append(hashes, h)
		err := st.CreateWebSession(ctx, store.WebSession{
			ID: utils.MakeID("wses"), TokenHash: h, UserID: owner,
			ExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
	}

	n, err := st.RevokeUserSessions(ctx, userID)
	if err != nil || n != 2 {
		t.Fatalf("revoke all: n=%d err=%v, want 2", n, err)
	}
	// Revoking again reports zero: the update is filtered on still-live rows,
	// so a repeated call does not double-count.
	if n, _ := st.RevokeUserSessions(ctx, userID); n != 0 {
		t.Errorf("second revoke: n=%d, want 0", n)
	}
	for _, h := range hashes[:2] {
		if _, id, _ := st.FindSessionUser(ctx, h); id != "" {
			t.Error("a revoked session still resolves")
		}
	}
	// The other user's session is untouched.
	if _, id, _ := st.FindSessionUser(ctx, hashes[2]); id == "" {
		t.Error("revoking one user's sessions killed another's")
	}

	// The sweep keeps recently ended sessions (they are evidence during an
	// incident) and removes only long-dead ones.
	if _, err := st.DeleteExpiredWebSessions(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	oldHash := authz.HashSessionToken("ancient-token")
	err = st.CreateWebSession(ctx, store.WebSession{
		ID: utils.MakeID("wses"), TokenHash: oldHash, UserID: otherID,
		ExpiresAt: time.Now().Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("create ancient session: %v", err)
	}
	if n, err := st.DeleteExpiredWebSessions(ctx); err != nil || n != 1 {
		t.Fatalf("sweep: n=%d err=%v, want 1", n, err)
	}
}

// TestIdentityAuditTrailIsAppendOnly checks the guarantee migration 0018 makes
// with a trigger, now that configuration changes also land in that table.
func TestIdentityAuditTrailIsAppendOnly(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	actorCtx := authz.NewContext(ctx, authz.Actor{
		ID: "usr-test", Kind: authz.KindUser, DisplayName: "tester", Role: authz.RoleAdmin,
	})
	if _, err := eng.CreateTeam(actorCtx, map[string]any{"name": "audited-team"}); err != nil {
		t.Fatalf("create team: %v", err)
	}
	events, total, err := st.ListAuditEvents(ctx, map[string]any{"entity_type": "team"}, 10, 0)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if total == 0 {
		t.Fatal("creating a team left no audit record")
	}
	ev := events[0]
	if ev["actor_id"] != "usr-test" || ev["action"] != "create" {
		t.Errorf("unexpected audit event: %v", ev)
	}
}

// TestIdentityTeamMembershipLookup covers the containment query behind team
// scoping. It is SQL-specific — jsonb `@>` against member_ids — so the
// in-memory model cannot stand in for it.
func TestIdentityTeamMembershipLookup(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	ada := seedIdentityUser(t, ctx, st, "teamada")
	bob := seedIdentityUser(t, ctx, st, "teambob")
	ts := utils.ToISO(utils.UTCNow())
	for _, team := range []struct {
		id      string
		members []any
	}{
		{"team-a", []any{ada}},
		{"team-b", []any{bob}},
		{"team-both", []any{ada, bob}},
	} {
		if err := st.UpsertItem(ctx, "teams", map[string]any{
			"id": team.id, "name": team.id, "member_ids": team.members,
			"created_at": ts, "updated_at": ts,
		}); err != nil {
			t.Fatalf("seed %s: %v", team.id, err)
		}
	}

	got, err := st.ListTeamIDsForUser(ctx, ada)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	if len(got) != 2 || got[0] != "team-a" || got[1] != "team-both" {
		t.Fatalf("ada's teams = %v, want [team-a team-both]", got)
	}
	// Someone in no team must come back empty, not with everything: that is the
	// difference between "sees unassigned objects only" and "sees everything".
	loner := seedIdentityUser(t, ctx, st, "teamloner")
	if got, err := st.ListTeamIDsForUser(ctx, loner); err != nil || len(got) != 0 {
		t.Fatalf("loner's teams = %v, err = %v, want empty", got, err)
	}
}

// TestIdentityIntegrationTeamFilter covers the InOrNullFilter SQL added for
// scoping: assigned-to-my-team OR unassigned, in one conjunctive filter.
func TestIdentityIntegrationTeamFilter(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	ts := utils.ToISO(utils.UTCNow())
	for _, integ := range []struct{ id, key, team string }{
		{"int-a", "scoped-key-a", "team-a"},
		{"int-b", "scoped-key-b", "team-b"},
		{"int-free", "scoped-key-free", ""},
	} {
		row := map[string]any{
			"id": integ.id, "name": integ.id, "key": integ.key, "routing_key": integ.key,
			"type": "webhook", "source_type": "webhook",
			"created_at": ts, "updated_at": ts,
		}
		if integ.team != "" {
			row["team_id"] = integ.team
		}
		if err := st.UpsertItem(ctx, "integrations", row); err != nil {
			t.Fatalf("seed %s: %v", integ.id, err)
		}
	}

	items, total, err := st.ListCollectionPage(ctx, "integrations",
		map[string]any{"team_id": store.InOrNullFilter{Values: []any{"team-a"}}}, 100, 0, store.SortSpec{})
	if err != nil {
		t.Fatalf("scoped list: %v", err)
	}
	got := map[string]bool{}
	for _, item := range items {
		got[utils.StrVal(item, "id")] = true
	}
	if total != 2 || !got["int-a"] || !got["int-free"] {
		t.Fatalf("scoped list = %v (total %d), want int-a and int-free", got, total)
	}

	// No teams at all: only the unassigned rows, never everything.
	items, total, err = st.ListCollectionPage(ctx, "integrations",
		map[string]any{"team_id": store.InOrNullFilter{}}, 100, 0, store.SortSpec{})
	if err != nil {
		t.Fatalf("teamless list: %v", err)
	}
	if total != 1 || utils.StrVal(items[0], "id") != "int-free" {
		t.Fatalf("teamless list = %v (total %d), want only int-free", items, total)
	}
}

// TestIdentitySessionListAndScopedRevoke covers the two session-management
// queries. The ownership scoping in RevokeWebSessionByID is a SQL predicate, so
// only a real database proves it.
func TestIdentitySessionListAndScopedRevoke(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	ada := seedIdentityUser(t, ctx, st, "sessada")
	bob := seedIdentityUser(t, ctx, st, "sessbob")
	var adaSessions []string
	for i, owner := range []string{ada, ada, bob} {
		id := utils.MakeID("wses")
		if owner == ada {
			adaSessions = append(adaSessions, id)
		}
		err := st.CreateWebSession(ctx, store.WebSession{
			ID: id, TokenHash: authz.HashSessionToken(utils.MakeID("tok") + string(rune('a'+i))),
			UserID: owner, ExpiresAt: time.Now().Add(time.Hour),
			RequestIP: "192.0.2.1", UserAgent: "go-test",
		})
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
	}

	list, err := st.ListWebSessions(ctx, ada)
	if err != nil || len(list) != 2 {
		t.Fatalf("ada's sessions = %d, err = %v, want 2", len(list), err)
	}
	for _, sess := range list {
		if sess.CreatedAt.IsZero() || sess.UserAgent != "go-test" {
			t.Errorf("session row is incomplete: %+v", sess)
		}
	}

	// Another user's session id must reach nothing, and must not be reported as
	// revoked.
	bobList, _ := st.ListWebSessions(ctx, bob)
	if revoked, err := st.RevokeWebSessionByID(ctx, bobList[0].ID, ada); err != nil || revoked {
		t.Fatalf("cross-user revoke: revoked=%v err=%v, want false", revoked, err)
	}
	if revoked, err := st.RevokeWebSessionByID(ctx, adaSessions[0], ada); err != nil || !revoked {
		t.Fatalf("own revoke: revoked=%v err=%v, want true", revoked, err)
	}
	// Revoking twice reports false: the update is filtered on live rows.
	if revoked, _ := st.RevokeWebSessionByID(ctx, adaSessions[0], ada); revoked {
		t.Error("second revoke reported success")
	}
	if list, _ := st.ListWebSessions(ctx, ada); len(list) != 1 {
		t.Errorf("after revoke ada has %d sessions, want 1", len(list))
	}
}

// TestIdentityAuditPruneRespectsTheTrigger is the important half of retention:
// the append-only trigger must be back in force after the sweep, so a later
// DELETE still fails.
func TestIdentityAuditPruneRespectsTheTrigger(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	old := utils.ToISO(utils.UTCNow().AddDate(0, 0, -40))
	recent := utils.ToISO(utils.UTCNow().AddDate(0, 0, -1))
	for i, at := range []string{old, recent} {
		err := st.InsertAuditEvent(ctx, store.AuditEvent{
			ID: utils.MakeID("aud"), OccurredAt: at, ActorKind: authz.KindSystem,
			Action: "create", EntityType: "team", EntityID: "team-" + string(rune('a'+i)),
		})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	cutoff := utils.ToISO(utils.UTCNow().AddDate(0, 0, -30))
	n, err := st.PruneAuditEvents(ctx, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("prune: n=%d err=%v, want 1", n, err)
	}
	_, total, err := st.ListAuditEvents(ctx, map[string]any{}, 10, 0)
	if err != nil || total != 1 {
		t.Fatalf("after prune total=%d err=%v, want 1", total, err)
	}
	// A prune with nothing to remove is a no-op, not an error.
	if n, err := st.PruneAuditEvents(ctx, cutoff); err != nil || n != 0 {
		t.Fatalf("second prune: n=%d err=%v, want 0", n, err)
	}
	// And the trigger is enabled again: the trail is immutable once more.
	if err := st.InsertAuditEvent(ctx, store.AuditEvent{
		ID: "aud-immutable", OccurredAt: recent, ActorKind: authz.KindSystem,
		Action: "create", EntityType: "team",
	}); err != nil {
		t.Fatalf("insert after prune: %v", err)
	}
	if _, total, _ := st.ListAuditEvents(ctx, map[string]any{}, 10, 0); total != 2 {
		t.Errorf("expected 2 events after the post-prune insert, got %d", total)
	}
}

// TestIdentityAuditRequestIDRoundTrip covers the correlation id against a real
// database: it must survive the insert, come back on read, and be filterable.
func TestIdentityAuditRequestIDRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	const rid = "req-correlation-1"
	for i, id := range []string{rid, rid, ""} {
		err := st.InsertAuditEvent(ctx, store.AuditEvent{
			ID: utils.MakeID("aud"), OccurredAt: utils.ToISO(utils.UTCNow()),
			ActorKind: authz.KindUser, ActorID: "usr-1", Action: "create",
			EntityType: "team", EntityID: "team-" + string(rune('a'+i)),
			RequestIP: "192.0.2.1", RequestID: id,
		})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	events, total, err := st.ListAuditEvents(ctx, map[string]any{"request_id": rid}, 10, 0)
	if err != nil {
		t.Fatalf("list by request_id: %v", err)
	}
	if total != 2 {
		t.Fatalf("filter by request_id returned %d, want 2", total)
	}
	for _, ev := range events {
		if ev["request_id"] != rid {
			t.Errorf("request_id = %v, want %q", ev["request_id"], rid)
		}
	}
	// The unattended event is still there, just not under that id.
	if _, total, _ := st.ListAuditEvents(ctx, map[string]any{}, 10, 0); total != 3 {
		t.Errorf("total events = %d, want 3", total)
	}
}
