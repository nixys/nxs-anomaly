package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
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

// Bad input is the caller's mistake, so the API must answer 400, not 500. These
// used to return plain errors, which writeEngineError maps to "internal error".
func TestCreateUserRejectsBadInputAsValidation(t *testing.T) {
	cases := map[string]map[string]any{
		"priority":            {"name": "Probe", "priority": "bogus"},
		"notification target": {"name": "Probe", "notification_targets": []any{map[string]any{"type": "bogus", "target": "x"}}},
		"timezone":            {"name": "Probe", "timezone": "Mars/Base"},
	}
	for name, payload := range cases {
		_, err := crudEngine(newMemStore()).CreateUser(context.Background(), payload)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
}

func TestUpdateUserRejectsUnknownTimezone(t *testing.T) {
	e := crudEngine(newMemStore())
	user, err := e.CreateUser(context.Background(), map[string]any{"name": "Probe"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, err = e.UpdateUser(context.Background(), user["id"].(string), map[string]any{"timezone": "Mars/Base"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
}

func TestSanitizersReportBadInputAsValidation(t *testing.T) {
	e := crudEngine(newMemStore())
	ctx := context.Background()
	route := func(r map[string]any) map[string]any { return map[string]any{"routes": []any{r}} }
	checks := map[string]error{}
	_, checks["escalation step"] = e.sanitizeStep(ctx, map[string]any{"kind": "BOGUS"}, 0)
	_, checks["policy channel"] = e.sanitizeNotificationPolicy(ctx, map[string]any{"channels": []any{"bogus"}})
	_, checks["route match_type"] = sanitizeRoutes(route(map[string]any{"match_type": "bogus"}))
	_, checks["route pattern"] = sanitizeRoutes(route(map[string]any{"match_type": "regex", "pattern": "(", "is_default": true}))
	_, checks["default route count"] = sanitizeRoutes(route(map[string]any{"match_type": "labels", "labels": map[string]any{"a": "b"}}))
	for name, err := range checks {
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
}

// The phone's view is read by the chains that name the person and the groups
// they were notified about, not by loading every unresolved group — that cost
// five seconds per event-stream tick on a stand with 89,000 open groups. The
// answer must be the same set as before: direct, team and schedule steps, and
// notifications, and nothing else.
func TestMobileDashboardReadsOnlyWhatConcernsThePerson(t *testing.T) {
	ms := newMemStore()
	now := time.Now().UTC()
	ms.seed("users", map[string]any{"id": "u1", "name": "One"}, map[string]any{"id": "u2", "name": "Two"})
	ms.seed("teams", map[string]any{"id": "t1", "name": "T", "member_ids": []any{"u1"}})
	ms.seed("schedules", map[string]any{"id": "s1", "name": "S", "timezone": "UTC", "enabled": true,
		"shifts": []any{map[string]any{"user_id": "u1", "recurrence": "none",
			"start_at": now.Add(-time.Hour).Format(time.RFC3339), "end_at": now.Add(time.Hour).Format(time.RFC3339)}}})
	step := func(kind string, kv ...any) map[string]any {
		s := map[string]any{"kind": kind}
		for i := 0; i+1 < len(kv); i += 2 {
			s[kv[i].(string)] = kv[i+1]
		}
		return s
	}
	ms.seed("escalation_chains",
		map[string]any{"id": "c_direct", "steps": []any{step(StepNotifyUser, "user_ids", []any{"u1"})}},
		map[string]any{"id": "c_team", "steps": []any{step(StepNotifyTeam, "team_id", "t1")}},
		map[string]any{"id": "c_sched", "steps": []any{step(StepNotifySchedule, "schedule_id", "s1")}},
		map[string]any{"id": "c_other", "steps": []any{step(StepNotifyUser, "user_ids", []any{"u2"})}})
	group := func(id, chain, status string) map[string]any {
		return map[string]any{"id": id, "escalation_chain_id": chain, "status": status, "title": id, "logs": []any{}}
	}
	ms.seed("alert_groups",
		group("g_direct", "c_direct", "open"), group("g_team", "c_team", "acknowledged"),
		group("g_sched", "c_sched", "silenced"), group("g_notified", "c_other", "open"),
		group("g_other", "c_other", "open"), group("g_resolved", "c_direct", "resolved"))
	for i := 0; i < 500; i++ {
		ms.seed("alert_groups", group(fmt.Sprintf("g_noise_%d", i), "c_other", "open"))
	}
	ms.seed("notifications", map[string]any{"id": "n1", "user_id": "u1", "alert_group_id": "g_notified", "channel": "log"})

	got, err := crudEngine(ms).MobileRelevantGroups(context.Background(), "u1")
	if err != nil {
		t.Fatalf("relevant groups: %v", err)
	}
	var ids []string
	for _, g := range got {
		ids = append(ids, g["id"].(string))
	}
	sort.Strings(ids)
	want := []string{"g_direct", "g_notified", "g_sched", "g_team"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("relevant groups = %v, want %v", ids, want)
	}
}

// The dashboard is the phone's list, polled every 30 seconds: it carries what
// a row shows, not each group's logs and alert ids, which grow without bound.
func TestMobileDashboardListsGroupsWithoutTheirLogs(t *testing.T) {
	ms := newMemStore()
	ms.seed("users", map[string]any{"id": "u1", "name": "One"})
	ms.seed("escalation_chains", map[string]any{"id": "c1", "steps": []any{map[string]any{"kind": StepNotifyUser, "user_ids": []any{"u1"}}}})
	ms.seed("alert_groups", map[string]any{"id": "g1", "escalation_chain_id": "c1", "status": "open", "title": "Disk",
		"severity": "critical", "alert_count": 3, "alert_ids": []any{"a1", "a2", "a3"},
		"logs": []any{map[string]any{"id": "l1", "message": "paged"}}})
	d, err := crudEngine(ms).GetMobileDashboard(context.Background(), "u1")
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	groups, _ := d["assigned_alert_groups"].([]map[string]any)
	if len(groups) != 1 {
		t.Fatalf("groups = %v", groups)
	}
	g := groups[0]
	if _, ok := g["logs"]; ok {
		t.Error("dashboard still carries logs")
	}
	if _, ok := g["alert_ids"]; ok {
		t.Error("dashboard still carries alert_ids")
	}
	if g["title"] != "Disk" || g["severity"] != "critical" || g["alert_count"] != 3 || g["status"] != "open" {
		t.Errorf("row lost what the list shows: %v", g)
	}
	// The stored group is untouched.
	if len(ms.row("alert_groups", "g1")["logs"].([]any)) != 1 {
		t.Error("the stored group lost its logs")
	}
}
