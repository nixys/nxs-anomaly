package model

import "github.com/nixys/nxs-anomaly/internal/store"

// Schedule is the typed Record wrapper for the schedules collection.
// TypedColumns: team_id.
type Schedule struct{ mapBacked }

var _ store.Record = Schedule{}

func WrapSchedule(m map[string]any) Schedule { return Schedule{mapBacked{m}} }

func (s Schedule) TypedValues() []any {
	return []any{tvStr(s.raw, "team_id")}
}

// EscalationChain is the typed Record wrapper for the escalation_chains collection.
// No typed columns — only id + data jsonb.
type EscalationChain struct{ mapBacked }

var _ store.Record = EscalationChain{}

func WrapEscalationChain(m map[string]any) EscalationChain {
	return EscalationChain{mapBacked{m}}
}

func (EscalationChain) TypedValues() []any { return nil }

// Integration is the typed Record wrapper for the integrations collection.
// TypedColumns: key, name, type, source_type, deleted_at, team_id.
type Integration struct{ mapBacked }

var _ store.Record = Integration{}

func WrapIntegration(m map[string]any) Integration { return Integration{mapBacked{m}} }

func (i Integration) TypedValues() []any {
	return []any{
		tvStr(i.raw, "key"),
		tvStr(i.raw, "name"),
		tvStr(i.raw, "type"),
		tvStr(i.raw, "source_type"),
		tvStr(i.raw, "deleted_at"),
		tvStr(i.raw, "team_id"),
	}
}

func init() {
	registerMapBacked("schedules", func(m map[string]any) store.Record { return WrapSchedule(m) })
	registerMapBacked("escalation_chains", func(m map[string]any) store.Record { return WrapEscalationChain(m) })
	registerMapBacked("integrations", func(m map[string]any) store.Record { return WrapIntegration(m) })
}
