// Package authz carries the identity of whoever is performing an operation and
// the rules for what that identity may do.
//
// It is deliberately dependency-free so that both the HTTP layer (which
// authenticates) and the engine (which records who did what) can import it
// without creating a cycle.
package authz

import "context"

// Role is a coarse permission level. The four roles are ordered: every role
// grants everything the role below it grants.
type Role string

const (
	RoleAdmin     Role = "admin"     // everything, including identity and destructive operations
	RoleEditor    Role = "editor"    // configuration: integrations, chains, schedules, teams
	RoleResponder Role = "responder" // acting on alerts: acknowledge, resolve, silence
	RoleViewer    Role = "viewer"    // read-only
	RoleNone      Role = ""          // no access; also the zero value, so an unset Role is never privileged
)

// rank orders roles so permission checks stay a single comparison. RoleNone is
// absent from the map on purpose: its lookup yields 0, below every real role.
var rank = map[Role]int{
	RoleViewer:    1,
	RoleResponder: 2,
	RoleEditor:    3,
	RoleAdmin:     4,
}

// ParseRole maps a configured or stored string onto a Role. The legacy scopes
// used before roles existed are accepted so existing API keys keep working:
// "admin" is unchanged and "readonly" becomes RoleViewer. Anything
// unrecognised (including "") yields RoleNone, so a typo denies access rather
// than granting it.
func ParseRole(s string) Role {
	switch Role(s) {
	case RoleAdmin, RoleEditor, RoleResponder, RoleViewer:
		return Role(s)
	}
	if s == "readonly" {
		return RoleViewer
	}
	return RoleNone
}

// Valid reports whether r is one of the four real roles.
func (r Role) Valid() bool { return rank[r] > 0 }

// RoleRank exposes the ordering so callers outside this package can pick the
// higher of two roles — group-to-role mapping needs it, because someone in two
// mapped groups should get the wider of the two, not whichever was seen first.
func RoleRank(r Role) int { return rank[r] }

func (r Role) String() string { return string(r) }

// Action is a capability an actor may or may not have. Actions are grouped by
// the lowest role that grants them rather than enumerated per role, because the
// roles are strictly ordered.
type Action string

const (
	ActionRead    Action = "read"    // any GET
	ActionRespond Action = "respond" // acknowledge / resolve / silence an alert group
	ActionEdit    Action = "edit"    // create or change configuration objects
	ActionAdmin   Action = "admin"   // identity, destructive and operational endpoints
)

// minRole is the lowest role granting each action.
var minRole = map[Action]Role{
	ActionRead:    RoleViewer,
	ActionRespond: RoleResponder,
	ActionEdit:    RoleEditor,
	ActionAdmin:   RoleAdmin,
}

// Kind describes what sort of principal an Actor is, so audit records can tell
// a human from an integration.
const (
	KindUser    = "user"    // a person, via session or (later) OIDC
	KindService = "service" // an API key
	KindSystem  = "system"  // the worker acting on its own, with no request behind it
)

// Actor is the authenticated principal behind an operation.
type Actor struct {
	ID          string // user id, API key id, or "" for the system actor
	Kind        string // KindUser, KindService or KindSystem
	DisplayName string // human-readable label for audit output
	Role        Role
	// TeamIDs are the teams this actor belongs to. Meaningful only when
	// TeamScoped is set; otherwise it is left nil and nothing reads it.
	TeamIDs []string
	// TeamScoped marks an actor whose view is limited to their teams' objects
	// plus unassigned ones. It is a separate field rather than being inferred
	// from len(TeamIDs) because "belongs to no team" and "is not subject to
	// scoping" are different states with opposite meanings: the first sees
	// only unassigned objects, the second sees everything.
	TeamScoped bool
	// Provisioner names the infrastructure-as-code tool this request came from
	// ("terraform"), and is empty for a person in a browser or an ordinary API
	// call. It decides two things: objects created by such a request are
	// stamped with it, and objects already stamped may only be changed by a
	// request carrying the same name. It is not a permission — it never widens
	// what an actor may do, only narrows what may be done to what a tool owns.
	Provisioner string
}

// MayAccessTeam reports whether the actor may see or act on an object owned by
// teamID. An empty teamID means the object is unassigned, which is visible to
// everyone: that is what lets team scoping be switched on without changing
// anything on a deployment that has never assigned a team.
func (a Actor) MayAccessTeam(teamID string) bool {
	if !a.TeamScoped || teamID == "" {
		return true
	}
	for _, id := range a.TeamIDs {
		if id == teamID {
			return true
		}
	}
	return false
}

// SystemActor is used by the worker loop and other unattended paths. It is
// deliberately not RoleAdmin: nothing checks permissions on its behalf, and
// giving it a real role would make an audit record indistinguishable from an
// administrator's action.
var SystemActor = Actor{Kind: KindSystem, DisplayName: "system", Role: RoleNone}

// Can reports whether the actor may perform the action.
func (a Actor) Can(action Action) bool {
	required, known := minRole[action]
	if !known {
		return false // unknown action: deny rather than fall through
	}
	return rank[a.Role] >= rank[required]
}

// IsSystem reports whether this is the unattended worker actor.
func (a Actor) IsSystem() bool { return a.Kind == KindSystem }

// Describe returns a stable label for logs and timeline entries.
func (a Actor) Describe() string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	if a.ID != "" {
		return a.ID
	}
	return "unknown"
}

type contextKey struct{}

// NewContext returns ctx carrying the actor.
func NewContext(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, contextKey{}, a)
}

// FromContext returns the actor carried by ctx. Contexts with no actor yield
// SystemActor: the worker and background jobs run without a request, and that
// is exactly what the system actor represents. Request paths always set an
// actor explicitly, so this fallback never silently upgrades a caller's rights
// — SystemActor holds no role.
func FromContext(ctx context.Context) Actor {
	if a, ok := ctx.Value(contextKey{}).(Actor); ok {
		return a
	}
	return SystemActor
}
