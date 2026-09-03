package engine

import (
	"context"
	"testing"
)

// These tests drive the engine CRUD mutators through the in-memory memStore
// (no PostgreSQL). They cover the create/update/validation logic that until now
// was exercised only by the PostgreSQL integration suite.

func crudEngine(ms *memStore) *Engine {
	return &Engine{store: ms}
}

func TestCreateUserDerivesUsernameAndDefaults(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)

	res, err := e.CreateUser(context.Background(), map[string]any{"name": "Alice Cooper"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if res["username"] != "alice.cooper" {
		t.Errorf("username = %v, want alice.cooper", res["username"])
	}
	if res["timezone"] != "UTC" {
		t.Errorf("timezone default = %v, want UTC", res["timezone"])
	}
	if res["on_duty"] != false {
		t.Errorf("on_duty default = %v, want false", res["on_duty"])
	}
	id, _ := res["id"].(string)
	if id == "" || ms.row("users", id) == nil {
		t.Errorf("user not persisted: id=%q", id)
	}
}

func TestCreateUserRequiresName(t *testing.T) {
	e := crudEngine(newMemStore())
	if _, err := e.CreateUser(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected validation error for missing name")
	}
}

func TestCreateUserExplicitUsernameKept(t *testing.T) {
	e := crudEngine(newMemStore())
	res, err := e.CreateUser(context.Background(), map[string]any{"name": "Bob", "username": "bobby", "email": "b@x.io"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if res["username"] != "bobby" {
		t.Errorf("username = %v, want bobby", res["username"])
	}
	if res["email"] != "b@x.io" {
		t.Errorf("email = %v, want b@x.io", res["email"])
	}
}

func TestUpdateUserNotFound(t *testing.T) {
	e := crudEngine(newMemStore())
	if _, err := e.UpdateUser(context.Background(), "usr-missing", map[string]any{"name": "X"}); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestUpdateUserMutatesFields(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	created, err := e.CreateUser(context.Background(), map[string]any{"name": "Carol"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	id := created["id"].(string)

	res, err := e.UpdateUser(context.Background(), id, map[string]any{"email": "carol@x.io", "phone": "+100"})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if res["email"] != "carol@x.io" || res["phone"] != "+100" {
		t.Errorf("fields not updated: %v", res)
	}
	if ms.row("users", id)["email"] != "carol@x.io" {
		t.Errorf("update not persisted")
	}
}

func TestUpdateUserMutatesTerraformLifecycleFields(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	created, err := e.CreateUser(context.Background(), map[string]any{"name": "Terraform User"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	id := created["id"].(string)

	updated, err := e.UpdateUser(context.Background(), id, map[string]any{
		"username": "terraform.user",
		"on_duty":  true,
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if updated["username"] != "terraform.user" || updated["on_duty"] != true {
		t.Fatalf("unexpected updated user: %#v", updated)
	}
	stored := ms.row("users", id)
	if stored["username"] != "terraform.user" || stored["on_duty"] != true {
		t.Fatalf("lifecycle fields were not persisted: %#v", stored)
	}
}

func TestUpdateChatopsChannelMutatesUserID(t *testing.T) {
	e := crudEngine(newMemStore())
	first, err := e.CreateUser(context.Background(), map[string]any{"name": "First"})
	if err != nil {
		t.Fatalf("CreateUser(first): %v", err)
	}
	second, err := e.CreateUser(context.Background(), map[string]any{"name": "Second"})
	if err != nil {
		t.Fatalf("CreateUser(second): %v", err)
	}
	channel, err := e.CreateChatopsChannel(context.Background(), map[string]any{
		"platform": "telegram", "name": "ops", "user_id": first["id"],
	})
	if err != nil {
		t.Fatalf("CreateChatopsChannel: %v", err)
	}

	updated, err := e.UpdateChatopsChannel(context.Background(), channel["id"].(string), map[string]any{
		"user_id": second["id"],
	})
	if err != nil {
		t.Fatalf("UpdateChatopsChannel: %v", err)
	}
	if updated["user_id"] != second["id"] {
		t.Fatalf("user_id was not updated: %#v", updated)
	}
}

func TestToggleUserDuty(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	created, _ := e.CreateUser(context.Background(), map[string]any{"name": "Dan"})
	id := created["id"].(string)

	res, err := e.ToggleUserDuty(context.Background(), id, true)
	if err != nil {
		t.Fatalf("ToggleUserDuty: %v", err)
	}
	if res["on_duty"] != true {
		t.Errorf("on_duty = %v, want true", res["on_duty"])
	}
	if ms.row("users", id)["on_duty"] != true {
		t.Errorf("toggle not persisted")
	}
}

func TestCreateTeamValidatesMembers(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)

	// Unknown member id must be rejected by ensureUsersExist.
	if _, err := e.CreateTeam(context.Background(), map[string]any{"name": "Ops", "member_ids": []any{"usr-nope"}}); err == nil {
		t.Fatal("expected error for unknown member")
	}

	created, _ := e.CreateUser(context.Background(), map[string]any{"name": "Eve"})
	uid := created["id"].(string)
	res, err := e.CreateTeam(context.Background(), map[string]any{"name": "Ops", "member_ids": []any{uid}})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if res["name"] != "Ops" {
		t.Errorf("name = %v", res["name"])
	}
	tid, _ := res["id"].(string)
	if ms.row("teams", tid) == nil {
		t.Errorf("team not persisted")
	}
}

func TestCreateScheduleValidatesTeam(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)

	if _, err := e.CreateSchedule(context.Background(), map[string]any{"name": "Primary", "team_id": "team-nope"}); err == nil {
		t.Fatal("expected error for unknown team")
	}

	res, err := e.CreateSchedule(context.Background(), map[string]any{"name": "Primary"})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if res["timezone"] != "UTC" {
		t.Errorf("timezone default = %v", res["timezone"])
	}
}

func TestCreateIntegrationDefaultsKeyAndType(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)

	res, err := e.CreateIntegration(context.Background(), map[string]any{"name": "Prometheus"})
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	if res["type"] != "webhook" {
		t.Errorf("type default = %v, want webhook", res["type"])
	}
	key, _ := res["key"].(string)
	if key == "" {
		t.Errorf("key not generated")
	}
	if res["routing_key"] != key {
		t.Errorf("routing_key should mirror key")
	}
	id := res["id"].(string)
	if ms.row("integrations", id) == nil {
		t.Errorf("integration not persisted")
	}
}

func TestCreateIntegrationRequiresName(t *testing.T) {
	e := crudEngine(newMemStore())
	if _, err := e.CreateIntegration(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected validation error for missing name")
	}
}

func TestRotateIntegrationKey(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	created, _ := e.CreateIntegration(context.Background(), map[string]any{"name": "Prom"})
	id := created["id"].(string)
	oldKey := created["key"].(string)

	res, err := e.RotateIntegrationKey(context.Background(), id)
	if err != nil {
		t.Fatalf("RotateIntegrationKey: %v", err)
	}
	newKey, _ := res["key"].(string)
	if newKey == "" || newKey == oldKey {
		t.Errorf("key not rotated: old=%q new=%q", oldKey, newKey)
	}
	if res["routing_key"] != newKey {
		t.Errorf("routing_key should mirror new key")
	}
}

func TestRotateIntegrationKeyNotFound(t *testing.T) {
	e := crudEngine(newMemStore())
	if _, err := e.RotateIntegrationKey(context.Background(), "int-missing"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestCreateEscalationChain(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)

	res, err := e.CreateEscalationChain(context.Background(), map[string]any{"name": "Default"})
	if err != nil {
		t.Fatalf("CreateEscalationChain: %v", err)
	}
	if res["name"] != "Default" {
		t.Errorf("name = %v", res["name"])
	}
	id := res["id"].(string)
	if ms.row("escalation_chains", id) == nil {
		t.Errorf("chain not persisted")
	}
}

func TestUpdateEscalationChainNotFound(t *testing.T) {
	e := crudEngine(newMemStore())
	if _, err := e.UpdateEscalationChain(context.Background(), "esc-missing", map[string]any{"name": "X"}); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestUpdateEscalationChainRenames(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	created, _ := e.CreateEscalationChain(context.Background(), map[string]any{"name": "Default"})
	id := created["id"].(string)

	res, err := e.UpdateEscalationChain(context.Background(), id, map[string]any{"name": "Renamed"})
	if err != nil {
		t.Fatalf("UpdateEscalationChain: %v", err)
	}
	if res["name"] != "Renamed" {
		t.Errorf("name = %v, want Renamed", res["name"])
	}
}

func TestRegisterMobileDeviceValidatesUser(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)

	// Missing required fields.
	if _, err := e.RegisterMobileDevice(context.Background(), map[string]any{"user_id": "u1"}); err == nil {
		t.Fatal("expected validation error for missing platform/push_token")
	}
	// Unknown user.
	_, err := e.RegisterMobileDevice(context.Background(), map[string]any{"user_id": "usr-nope", "platform": "ios", "push_token": "tok"})
	if err == nil {
		t.Fatal("expected error for unknown user")
	}

	created, _ := e.CreateUser(context.Background(), map[string]any{"name": "Mob"})
	uid := created["id"].(string)
	res, err := e.RegisterMobileDevice(context.Background(), map[string]any{"user_id": uid, "platform": "IOS", "push_token": "tok"})
	if err != nil {
		t.Fatalf("RegisterMobileDevice: %v", err)
	}
	if res["platform"] != "ios" {
		t.Errorf("platform not lowercased: %v", res["platform"])
	}
	if res["active"] != true {
		t.Errorf("active = %v, want true", res["active"])
	}
}

func TestCreateMobileSessionDeviceOwnership(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	created, _ := e.CreateUser(context.Background(), map[string]any{"name": "Mob"})
	uid := created["id"].(string)
	dev, _ := e.RegisterMobileDevice(context.Background(), map[string]any{"user_id": uid, "platform": "ios", "push_token": "tok"})
	did := dev["device_id"].(string)

	// device_id used for session is the device row id, not the inner device_id field.
	devRowID := dev["id"].(string)
	res, err := e.CreateMobileSession(context.Background(), map[string]any{"user_id": uid, "device_id": devRowID})
	if err != nil {
		t.Fatalf("CreateMobileSession: %v", err)
	}
	if res["token"] == nil || res["token"] == "" {
		t.Errorf("session token not generated")
	}
	_ = did
}
