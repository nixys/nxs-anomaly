package authz

import (
	"context"
	"testing"
)

func TestParseRole(t *testing.T) {
	cases := map[string]Role{
		"admin":     RoleAdmin,
		"editor":    RoleEditor,
		"responder": RoleResponder,
		"viewer":    RoleViewer,
		"readonly":  RoleViewer, // legacy scope
		"":          RoleNone,
		"root":      RoleNone, // unknown must not be privileged
		"Admin":     RoleNone, // case-sensitive on purpose
	}
	for in, want := range cases {
		if got := ParseRole(in); got != want {
			t.Errorf("ParseRole(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanIsOrdered(t *testing.T) {
	allowed := map[Role][]Action{
		RoleViewer:    {ActionRead},
		RoleResponder: {ActionRead, ActionRespond},
		RoleEditor:    {ActionRead, ActionRespond, ActionEdit},
		RoleAdmin:     {ActionRead, ActionRespond, ActionEdit, ActionAdmin},
		RoleNone:      {},
	}
	every := []Action{ActionRead, ActionRespond, ActionEdit, ActionAdmin}
	for role, grants := range allowed {
		granted := map[Action]bool{}
		for _, a := range grants {
			granted[a] = true
		}
		actor := Actor{Role: role}
		for _, a := range every {
			if got := actor.Can(a); got != granted[a] {
				t.Errorf("role %q Can(%q) = %v, want %v", role, a, got, granted[a])
			}
		}
	}
}

func TestCanUnknownActionDenied(t *testing.T) {
	if (Actor{Role: RoleAdmin}).Can(Action("delete-everything")) {
		t.Fatal("unknown action must be denied even for admin")
	}
}

func TestZeroActorHasNoRights(t *testing.T) {
	var zero Actor
	for _, a := range []Action{ActionRead, ActionRespond, ActionEdit, ActionAdmin} {
		if zero.Can(a) {
			t.Fatalf("zero Actor must not be allowed to %q", a)
		}
	}
}

func TestSystemActorHasNoRole(t *testing.T) {
	if SystemActor.Can(ActionRead) || SystemActor.Can(ActionAdmin) {
		t.Fatal("SystemActor must hold no permissions; it bypasses checks, it does not pass them")
	}
	if !SystemActor.IsSystem() {
		t.Fatal("SystemActor.IsSystem() must be true")
	}
}

func TestContextRoundTrip(t *testing.T) {
	want := Actor{
		ID: "usr-1", Kind: KindUser, DisplayName: "alice", Role: RoleEditor,
		TeamIDs: []string{"team-a"}, TeamScoped: true,
	}
	got := FromContext(NewContext(context.Background(), want))
	// Compared field by field because Actor now carries a slice; reflect.DeepEqual
	// would do, but naming the fields keeps the failure message useful.
	if got.ID != want.ID || got.Kind != want.Kind || got.DisplayName != want.DisplayName ||
		got.Role != want.Role || got.TeamScoped != want.TeamScoped ||
		len(got.TeamIDs) != len(want.TeamIDs) || got.TeamIDs[0] != want.TeamIDs[0] {
		t.Fatalf("FromContext = %+v, want %+v", got, want)
	}
}

// TestMayAccessTeam pins the visibility rule, including the two cases that make
// it safe to switch on: an unassigned object is visible to everyone, and an
// actor that is not scoped is unaffected.
func TestMayAccessTeam(t *testing.T) {
	scoped := Actor{Kind: KindUser, Role: RoleResponder, TeamIDs: []string{"team-a", "team-b"}, TeamScoped: true}
	cases := []struct {
		name  string
		actor Actor
		team  string
		want  bool
	}{
		{"own team", scoped, "team-a", true},
		{"other own team", scoped, "team-b", true},
		{"foreign team", scoped, "team-c", false},
		{"unassigned object", scoped, "", true},
		{"unscoped actor, foreign team", Actor{Role: RoleAdmin}, "team-c", true},
		{"scoped actor with no teams", Actor{Role: RoleViewer, TeamScoped: true}, "team-a", false},
		{"scoped actor with no teams, unassigned", Actor{Role: RoleViewer, TeamScoped: true}, "", true},
	}
	for _, tc := range cases {
		if got := tc.actor.MayAccessTeam(tc.team); got != tc.want {
			t.Errorf("%s: MayAccessTeam(%q) = %v, want %v", tc.name, tc.team, got, tc.want)
		}
	}
}

func TestContextWithoutActorIsSystem(t *testing.T) {
	got := FromContext(context.Background())
	if !got.IsSystem() {
		t.Fatalf("bare context must yield the system actor, got %+v", got)
	}
	if got.Role != RoleNone {
		t.Fatalf("fallback actor must hold no role, got %q", got.Role)
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		actor Actor
		want  string
	}{
		{Actor{DisplayName: "alice", ID: "usr-1"}, "alice"},
		{Actor{ID: "usr-1"}, "usr-1"},
		{Actor{}, "unknown"},
	}
	for _, c := range cases {
		if got := c.actor.Describe(); got != c.want {
			t.Errorf("Describe(%+v) = %q, want %q", c.actor, got, c.want)
		}
	}
}
