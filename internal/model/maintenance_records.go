package model

import "github.com/nixys/nxs-anomaly/internal/store"

// MaintenanceWindow is the typed Record wrapper for the maintenance_windows
// collection: an interval during which the named integrations are being worked
// on, so their alerts are recorded but nobody is paged.
// TypedColumns: team_id, starts_at, ends_at.
type MaintenanceWindow struct{ mapBacked }

var _ store.Record = MaintenanceWindow{}

func WrapMaintenanceWindow(m map[string]any) MaintenanceWindow {
	return MaintenanceWindow{mapBacked{m}}
}

func (w MaintenanceWindow) TypedValues() []any {
	return []any{
		// tvAny rather than tvStr for the team: an unassigned window is visible
		// to everyone, and that rule is expressed as a NULL team_id, which an
		// empty string would not be.
		tvAny(w.raw, "team_id"),
		tvStr(w.raw, "starts_at"),
		tvStr(w.raw, "ends_at"),
	}
}

func init() {
	registerMapBacked("maintenance_windows", func(m map[string]any) store.Record { return WrapMaintenanceWindow(m) })
}
