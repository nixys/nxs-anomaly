package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/storetest"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func operatorCtx() context.Context {
	return authz.NewContext(context.Background(), authz.Actor{
		ID:          "usr-alice",
		Kind:        authz.KindUser,
		DisplayName: "alice",
		Role:        authz.RoleResponder,
	})
}

func seedOpenGroup(ms *memStore, id string) {
	ms.seed("alert_groups", map[string]any{
		"id":     id,
		"status": "open",
		"title":  "disk full",
		"logs":   []any{},
	})
}

func onlyEvent(t *testing.T, events []store.AuditEvent) store.AuditEvent {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("want exactly 1 audit event, got %d: %+v", len(events), events)
	}
	return events[0]
}

// TestAcknowledgeWritesAuditEvent is the core of the backlog item: after an
// acknowledgement there is a durable record naming who did it.
func TestAcknowledgeWritesAuditEvent(t *testing.T) {
	ms := newMemStore()
	seedOpenGroup(ms, "grp-1")
	e := crudEngine(ms)

	if _, err := e.AcknowledgeGroup(operatorCtx(), "grp-1"); err != nil {
		t.Fatalf("AcknowledgeGroup: %v", err)
	}

	ev := onlyEvent(t, ms.audit)
	if ev.Action != AuditAcknowledge {
		t.Errorf("action = %q, want %q", ev.Action, AuditAcknowledge)
	}
	if ev.EntityType != "alert_group" || ev.EntityID != "grp-1" {
		t.Errorf("entity = %s/%s, want alert_group/grp-1", ev.EntityType, ev.EntityID)
	}
	if ev.ActorID != "usr-alice" || ev.ActorName != "alice" {
		t.Errorf("actor = %s/%s, want usr-alice/alice", ev.ActorID, ev.ActorName)
	}
	if ev.ActorRole != string(authz.RoleResponder) {
		t.Errorf("actor role = %q, want %q", ev.ActorRole, authz.RoleResponder)
	}
	if ev.ID == "" || ev.OccurredAt == "" {
		t.Errorf("event must carry an id and a timestamp, got %+v", ev)
	}
}

// TestAuditRecordsRequestIP: the trail should say where an action came from,
// not only who performed it.
func TestAuditRecordsRequestIP(t *testing.T) {
	ms := newMemStore()
	seedOpenGroup(ms, "grp-1")
	e := crudEngine(ms)

	ctx := NewRequestIPContext(operatorCtx(), "10.1.2.3")
	if _, err := e.ResolveGroup(ctx, "grp-1"); err != nil {
		t.Fatalf("ResolveGroup: %v", err)
	}
	if ev := onlyEvent(t, ms.audit); ev.RequestIP != "10.1.2.3" {
		t.Errorf("request_ip = %q, want 10.1.2.3", ev.RequestIP)
	}
}

// TestUnattendedActionsRecordSystemActor: the worker has no principal behind
// it, and the trail must say so rather than leaving the field blank in a way
// that could be mistaken for a person.
func TestUnattendedActionsRecordSystemActor(t *testing.T) {
	ms := newMemStore()
	seedOpenGroup(ms, "grp-1")
	e := crudEngine(ms)

	if _, err := e.AcknowledgeGroup(context.Background(), "grp-1"); err != nil {
		t.Fatalf("AcknowledgeGroup: %v", err)
	}
	if ev := onlyEvent(t, ms.audit); ev.ActorKind != authz.KindSystem {
		t.Errorf("actor kind = %q, want %q", ev.ActorKind, authz.KindSystem)
	}
}

// TestBulkOperationsAuditEachGroup: a bulk action must be answerable per group,
// otherwise "what happened to grp-2" cannot be looked up by entity id.
func TestBulkOperationsAuditEachGroup(t *testing.T) {
	ms := newMemStore()
	seedOpenGroup(ms, "grp-1")
	seedOpenGroup(ms, "grp-2")
	e := crudEngine(ms)

	if _, err := e.BulkAcknowledgeGroups(operatorCtx(), []string{"grp-1", "grp-2"}); err != nil {
		t.Fatalf("BulkAcknowledgeGroups: %v", err)
	}
	if len(ms.audit) != 2 {
		t.Fatalf("want one event per acknowledged group, got %d: %+v", len(ms.audit), ms.audit)
	}
	seen := map[string]bool{}
	for _, ev := range ms.audit {
		seen[ev.EntityID] = true
		if ev.ActorID != "usr-alice" {
			t.Errorf("bulk event actor = %q, want usr-alice", ev.ActorID)
		}
	}
	if !seen["grp-1"] || !seen["grp-2"] {
		t.Errorf("events cover %v, want both grp-1 and grp-2", seen)
	}
}

// TestBulkSkippedGroupsAreNotAudited: recording an event for a group the
// operation skipped would make the trail claim something that did not happen.
func TestBulkSkippedGroupsAreNotAudited(t *testing.T) {
	ms := newMemStore()
	seedOpenGroup(ms, "grp-1")
	ms.seed("alert_groups", map[string]any{
		"id": "grp-done", "status": "resolved", "title": "already done", "logs": []any{},
	})
	e := crudEngine(ms)

	if _, err := e.BulkAcknowledgeGroups(operatorCtx(), []string{"grp-1", "grp-done", "grp-missing"}); err != nil {
		t.Fatalf("BulkAcknowledgeGroups: %v", err)
	}
	ev := onlyEvent(t, ms.audit)
	if ev.EntityID != "grp-1" {
		t.Errorf("audited %q, want only the group that actually changed (grp-1)", ev.EntityID)
	}
}

// TestMobileSessionIsAttributedToItsUser is the regression guard for the bug
// this work uncovered: the mobile handlers validated the session and then threw
// the user_id away, so mobile acknowledgements were anonymous.
func TestMobileSessionIsAttributedToItsUser(t *testing.T) {
	ms := newMemStore()
	seedOpenGroup(ms, "grp-1")
	ms.seed("users", map[string]any{"id": "usr-bob", "username": "bob"})
	ms.seed("mobile_sessions", map[string]any{
		"id": "msess-1", "token": "mtok-1", "user_id": "usr-bob",
		"device_id": "dev-1", "is_active": true, "revoked_at": nil,
	})
	e := crudEngine(ms)

	if _, err := e.MobileAcknowledgeGroup(context.Background(), "mtok-1", "grp-1"); err != nil {
		t.Fatalf("MobileAcknowledgeGroup: %v", err)
	}

	ev := onlyEvent(t, ms.audit)
	if ev.ActorID != "usr-bob" {
		t.Errorf("actor id = %q, want usr-bob (the session's user)", ev.ActorID)
	}
	if ev.ActorName != "bob" {
		t.Errorf("actor name = %q, want the username bob", ev.ActorName)
	}
	if ev.ActorKind != authz.KindUser {
		t.Errorf("actor kind = %q, want %q", ev.ActorKind, authz.KindUser)
	}
	if ev.ActorRole != string(authz.RoleResponder) {
		t.Errorf("actor role = %q, want responder", ev.ActorRole)
	}

	group := ms.row("alert_groups", "grp-1")
	ref, ok := group["acknowledged_by"].(map[string]any)
	if !ok {
		t.Fatalf("group has no acknowledged_by: %#v", group)
	}
	if ref["id"] != "usr-bob" {
		t.Errorf("acknowledged_by id = %v, want usr-bob", ref["id"])
	}
}

// TestCreateUserRole covers the identity field added to the roster entity.
func TestCreateUserRole(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)

	// Default: a roster entry that can be paged but cannot sign in.
	res, err := e.CreateUser(context.Background(), map[string]any{"name": "Carol"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got := res["role"]; got != "" {
		t.Errorf("default role = %v, want empty (no access)", got)
	}

	res, err = e.CreateUser(context.Background(), map[string]any{"name": "Dave", "role": "editor"})
	if err != nil {
		t.Fatalf("CreateUser with role: %v", err)
	}
	if got := res["role"]; got != string(authz.RoleEditor) {
		t.Errorf("role = %v, want editor", got)
	}

	// A typo through the API is reported, not silently downgraded: unlike a
	// deployment-time key, there is a caller here who can fix it.
	if _, err := e.CreateUser(context.Background(), map[string]any{"name": "Eve", "role": "superuser"}); err == nil {
		t.Error("unknown role should be rejected")
	}
}

// TestAuditRetentionPrunesOnlyOldEvents covers the retention sweep. Rows leave
// an append-only table here, so the boundary is worth pinning: nothing newer
// than the cutoff may go, and nothing at all goes when retention is unset.
func TestAuditRetentionPrunesOnlyOldEvents(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	eng := New(st)

	old := utils.ToISO(utils.UTCNow().AddDate(0, 0, -40))
	recent := utils.ToISO(utils.UTCNow().AddDate(0, 0, -2))
	for i, at := range []string{old, old, recent} {
		if err := st.InsertAuditEvent(ctx, store.AuditEvent{
			ID: fmt.Sprintf("aud-%d", i), OccurredAt: at,
			ActorKind: authz.KindUser, Action: "create", EntityType: "team",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Retention unset: the sweep must not run at all.
	if _, err := eng.RunWorkerCycle(ctx); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if n := len(st.AuditEvents()); n != 3 {
		t.Fatalf("with retention unset %d events survived, want 3", n)
	}

	eng.deliveryCfg.Retention.AuditDays = 30
	if _, err := eng.RunWorkerCycle(ctx); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	events := st.AuditEvents()
	if len(events) != 1 || events[0].OccurredAt != recent {
		t.Fatalf("after pruning: %+v, want only the recent event", events)
	}
}
