package model

import "github.com/nixys/nxs-anomaly/internal/store"

// Report is the typed Record wrapper for the reports collection: a generated
// on-call quality digest for one team and period. See internal/engine/report.go
// for how the digest itself is built.
// TypedColumns: team_id, period_start, period_end, generated_at, format.
type Report struct{ mapBacked }

var _ store.Record = Report{}

func WrapReport(m map[string]any) Report {
	return Report{mapBacked{m}}
}

func (r Report) TypedValues() []any {
	return []any{
		// tvAny, not tvStr: an unassigned (installation-wide) report has a NULL
		// team_id, same reasoning as MaintenanceWindow.
		tvAny(r.raw, "team_id"),
		tvStr(r.raw, "period_start"),
		tvStr(r.raw, "period_end"),
		tvStr(r.raw, "generated_at"),
		tvStr(r.raw, "format"),
	}
}

func init() {
	registerMapBacked("reports", func(m map[string]any) store.Record { return WrapReport(m) })
}
