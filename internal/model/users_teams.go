package model

import "github.com/nixys/nxs-anomaly/internal/store"

// User is the typed Record wrapper for the users collection.
// TypedColumns: username, email, on_duty, priority.
type User struct{ mapBacked }

var _ store.Record = User{}

func WrapUser(m map[string]any) User { return User{mapBacked{m}} }

func (u User) TypedValues() []any {
	return []any{
		tvStr(u.raw, "username"),
		tvStr(u.raw, "email"),
		tvBool(u.raw, "on_duty"),
		tvStr(u.raw, "priority"),
	}
}

// Team is the typed Record wrapper for the teams collection.
// TypedColumns: name.
type Team struct{ mapBacked }

var _ store.Record = Team{}

func WrapTeam(m map[string]any) Team { return Team{mapBacked{m}} }

func (t Team) TypedValues() []any {
	return []any{tvStr(t.raw, "name")}
}

func init() {
	registerMapBacked("users", func(m map[string]any) store.Record { return WrapUser(m) })
	registerMapBacked("teams", func(m map[string]any) store.Record { return WrapTeam(m) })
}
