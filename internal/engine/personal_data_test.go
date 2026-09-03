package engine

import (
	"context"
	"strings"
	"testing"

	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/storetest"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// seedPerson builds one user with something in every personal-data category the
// inventory lists, so the erasure and export tests operate on a realistic row
// rather than on a name and an id.
func seedPerson(t *testing.T, st *storetest.Store) (*Engine, string) {
	t.Helper()
	ctx := context.Background()
	e := New(st)

	st.Seed("users", map[string]any{
		"id":          "usr-erin",
		"name":        "Erin Ivanova",
		"username":    "erin.ivanova",
		"email":       "erin@example.ru",
		"phone":       "+70000000001",
		"telegram_id": "551234567",
		"role":        "responder",
		"on_duty":     true,
		"notification_targets": []any{
			map[string]any{"type": "telegram", "target": "551234567"},
		},
	})
	st.Seed("notifications", map[string]any{
		"id": "ntf-1", "user_id": "usr-erin", "channel": "telegram",
		"target": "551234567", "status": "delivered",
		"updated_at": utils.ToISO(utils.UTCNow()),
	})
	st.Seed("notification_delivery_attempts", map[string]any{
		"id": "att-1", "notification_id": "ntf-1", "channel": "telegram", "target": "551234567",
	})
	st.Seed("mobile_devices", map[string]any{"id": "dev-1", "user_id": "usr-erin", "push_token": "tok"})
	st.Seed("chatops_channels", map[string]any{"id": "cha-1", "user_id": "usr-erin", "platform": "telegram"})

	if err := st.CreateWebSession(ctx, store.WebSession{
		ID: "sess-1", TokenHash: "hash-1", UserID: "usr-erin",
		CreatedAt: utils.UTCNow(), ExpiresAt: utils.UTCNow().Add(time.Hour),
		RequestIP: "10.1.2.3", UserAgent: "Firefox",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := st.SetUserPassword(ctx, "usr-erin", "argon2-hash"); err != nil {
		t.Fatalf("seed password: %v", err)
	}
	if err := st.InsertAuditEvent(ctx, store.AuditEvent{
		ID: "aud-1", OccurredAt: utils.ToISO(utils.UTCNow()),
		ActorID: "usr-erin", ActorKind: authz.KindUser, ActorName: "Erin Ivanova",
		RequestIP: "10.1.2.3", Action: AuditAcknowledge,
		EntityType: "alert_group", EntityID: "grp-9",
	}); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	return e, "usr-erin"
}

// TestExportUserDataCoversTheInventory checks the export answers the question a
// person actually asks — "what do you hold about me" — across categories, and
// that the one thing it must never hand over is absent.
func TestExportUserDataCoversTheInventory(t *testing.T) {
	st := storetest.New()
	e, id := seedPerson(t, st)

	out, err := e.ExportUserData(adminCtx(), id)
	if err != nil {
		t.Fatalf("ExportUserData: %v", err)
	}

	for _, section := range []string{
		"user", "web_sessions", "notifications", "notification_delivery_attempts",
		"mobile_devices", "chatops_channels", "audit_events_by_user", "audit_events_about_user",
	} {
		if _, ok := out[section]; !ok {
			t.Errorf("export is missing the %q section", section)
		}
	}
	if out["has_local_password"] != true {
		t.Error("a set password should be reported as present")
	}
	// The hash is a credential, not information about the person.
	if strings.Contains(renderExport(out), "argon2-hash") {
		t.Error("the password hash must never appear in an export")
	}
	if n := len(out["notifications"].([]any)); n != 1 {
		t.Errorf("notifications = %d, want 1", n)
	}
	if n := len(out["audit_events_by_user"].([]any)); n != 1 {
		t.Errorf("audit_events_by_user = %d, want 1", n)
	}
}

func TestExportUnknownUserIsNotFound(t *testing.T) {
	st := storetest.New()
	e := New(st)
	if _, err := e.ExportUserData(adminCtx(), "usr-nobody"); err == nil {
		t.Fatal("exporting a user that does not exist must fail")
	}
}

// TestEraseUserRemovesIdentifiersAndVerifies is the central test of the feature:
// after an erasure, the identifiers are gone from every place the inventory
// lists them, and the operation says so on its own evidence.
func TestEraseUserRemovesIdentifiersAndVerifies(t *testing.T) {
	ctx := adminCtx()
	st := storetest.New()
	e, id := seedPerson(t, st)

	report, err := e.EraseUser(ctx, id)
	if err != nil {
		t.Fatalf("EraseUser: %v", err)
	}
	if report["verified"] != true {
		t.Fatalf("erasure did not verify: %v", report["residue"])
	}

	user, err := st.GetItem(ctx, "users", id)
	if err != nil || user == nil {
		t.Fatalf("the roster entry must survive so every reference still resolves: %v %v", user, err)
	}
	for _, f := range []string{"name", "username", "email", "phone", "telegram_id"} {
		if v := utils.StrVal(user, f); strings.Contains(v, "Erin") || strings.Contains(v, "erin@") ||
			strings.Contains(v, "70000000001") || v == "551234567" {
			t.Errorf("users.%s still holds a personal value: %q", f, v)
		}
	}
	if utils.StrVal(user, "role") != "" {
		t.Error("an erased person must not be able to sign in")
	}
	if utils.BoolVal(user, "on_duty", false) {
		t.Error("an erased person must not appear as somebody the escalation could still try")
	}
	if utils.StrVal(user, "erased_at") == "" {
		t.Error("the record must say it was erased")
	}

	// Deleted outright.
	if _, ok, _ := st.GetUserPasswordHash(ctx, id); ok {
		t.Error("credentials must be gone")
	}
	if sessions, _ := st.ListWebSessions(ctx, id); len(sessions) != 0 {
		t.Error("session rows carry the client IP and must be deleted, not revoked")
	}
	for _, collection := range []string{"mobile_devices", "chatops_channels"} {
		if items, _ := st.ListItemsIn(ctx, collection, "user_id", []any{id}); len(items) != 0 {
			t.Errorf("%s must be deleted", collection)
		}
	}

	// Pseudonymised, not deleted: the paging history is the record of what
	// happened to somebody else's outage.
	notifications, _ := st.ListItemsIn(ctx, "notifications", "user_id", []any{id})
	if len(notifications) != 1 {
		t.Fatalf("the notification must survive as a fact, got %d rows", len(notifications))
	}
	if target := utils.StrVal(notifications[0], "target"); target == "551234567" {
		t.Error("the delivery address must be scrubbed")
	}
	attempts, _ := st.ListItemsIn(ctx, "notification_delivery_attempts", "notification_id", []any{"ntf-1"})
	if len(attempts) != 1 || utils.StrVal(attempts[0], "target") == "551234567" {
		t.Errorf("the attempt must survive with a scrubbed address, got %v", attempts)
	}
}

// TestEraseUserKeepsTheAuditTrailIntact is the other half, and the one that
// would be easiest to get wrong: the personal columns go, the trail does not.
func TestEraseUserKeepsTheAuditTrailIntact(t *testing.T) {
	ctx := adminCtx()
	st := storetest.New()
	e, id := seedPerson(t, st)

	if _, err := e.EraseUser(ctx, id); err != nil {
		t.Fatalf("EraseUser: %v", err)
	}

	var seen int
	for _, ev := range st.AuditEvents() {
		if ev.ActorID != id {
			continue
		}
		seen++
		if ev.ActorName == "Erin Ivanova" {
			t.Error("the actor's name is the personal datum and must be replaced")
		}
		if ev.RequestIP != "" {
			t.Error("the request IP identifies a person's device and must go")
		}
		if ev.Action != AuditAcknowledge || ev.EntityID != "grp-9" {
			t.Errorf("what happened must be untouched, got %s on %s", ev.Action, ev.EntityID)
		}
	}
	if seen != 1 {
		t.Fatalf("the event itself must survive; found %d events for the actor", seen)
	}

	// And the erasure leaves its own record. "We deleted the evidence that we
	// deleted the evidence" is not a defensible position.
	var recorded bool
	for _, ev := range st.AuditEvents() {
		if ev.Action == AuditErase && ev.EntityID == id {
			recorded = true
		}
	}
	if !recorded {
		t.Error("the erasure must be audited")
	}
}

// TestEraseUserIsIdempotent covers the retry: a second call must report the
// state rather than fail, or a caller cannot tell whether the first one worked.
func TestEraseUserIsIdempotent(t *testing.T) {
	ctx := adminCtx()
	st := storetest.New()
	e, id := seedPerson(t, st)

	if _, err := e.EraseUser(ctx, id); err != nil {
		t.Fatalf("first erase: %v", err)
	}
	second, err := e.EraseUser(ctx, id)
	if err != nil {
		t.Fatalf("second erase: %v", err)
	}
	if second["already_erased"] != true {
		t.Error("a repeat call should say the person was already erased")
	}
	if second["verified"] != true {
		t.Errorf("the repeat call must still verify: %v", second["residue"])
	}
}

// TestEraseReportNamesWhatItCannotReach pins the honesty requirement: an
// erasure report that quietly omits the copies outside this database would be
// the most misleading document in the product.
func TestEraseReportNamesWhatItCannotReach(t *testing.T) {
	st := storetest.New()
	e, id := seedPerson(t, st)

	report, err := e.EraseUser(adminCtx(), id)
	if err != nil {
		t.Fatalf("EraseUser: %v", err)
	}
	scope, _ := report["out_of_scope"].([]any)
	if len(scope) == 0 {
		t.Fatal("the report must name what it cannot reach")
	}
	joined := renderExport(map[string]any{"s": scope})
	for _, must := range []string{"backup", "log", "analytics", "trace"} {
		if !strings.Contains(strings.ToLower(joined), must) {
			t.Errorf("out_of_scope should mention %q: %s", must, joined)
		}
	}
}

// renderExport flattens a response for substring assertions. Deliberately crude:
// the assertions it backs are "this string must not appear anywhere", which is
// exactly the question a flattened rendering answers.
func renderExport(v map[string]any) string {
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			for _, item := range typed {
				walk(item)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		default:
			b.WriteString(strings.TrimSpace(utils.StrVal(map[string]any{"v": typed}, "v")))
			b.WriteString(" ")
		}
	}
	walk(v)
	return b.String()
}
