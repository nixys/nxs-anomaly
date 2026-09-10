package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// maintenance.go implements planned-maintenance windows.
//
// A window says: between these two instants, these integrations are being
// worked on, so record what they send but page nobody. It is deliberately not a
// new suppression mechanism. The service already has one — a group in status
// "silenced" is excluded from the worker's due-scan, keeps its alerts, and
// shows as silenced in the UI — so a window is a rule that silences the groups
// an integration opens while the window is open, until the window ends.
//
// Two consequences of that choice are worth stating, because both are the
// reason to prefer it over dropping the alerts:
//
//   - Nothing is lost. After the window, the groups are there, in order, with
//     their alerts, which is how you find out that the deploy you were doing
//     broke something unrelated at 02:40.
//   - A group already escalating when the window opens is left alone. Somebody
//     has been woken for it and may be working on it; silencing it underneath
//     them would take away the thing they are looking at. A window suppresses
//     what the maintenance is expected to produce, not what preceded it.
//
// The heartbeat (dead-man switch) consults windows too, and that is the case
// that motivated the feature: a source taken down for planned work is a silent
// source, so without windows every planned maintenance raises SourceSilent at
// the moment the people who would answer it are already busy causing it.

// maintenanceIntegrations returns the integration ids a window covers.
func maintenanceIntegrations(window map[string]any) []string {
	raw, _ := window["integration_ids"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if id := strings.TrimSpace(fmt.Sprintf("%v", v)); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// activeMaintenance returns the window covering integrationID at now, or nil.
//
// Takes the windows as an already-loaded map because it is called from inside
// ingest's advisory lock, where a database read is not allowed.
func activeMaintenance(windows map[string]map[string]any, integrationID string, now time.Time) map[string]any {
	var found map[string]any
	for _, w := range windows {
		starts, errStart := utils.ParseDatetime(utils.StrVal(w, "starts_at"))
		ends, errEnd := utils.ParseDatetime(utils.StrVal(w, "ends_at"))
		if errStart != nil || errEnd != nil {
			// A window whose bounds cannot be read suppresses nothing. Failing
			// open is right here: the alternative is a malformed row silently
			// swallowing pages, which is the one outcome this service must not
			// produce.
			continue
		}
		if now.Before(starts) || !now.Before(ends) {
			continue
		}
		covers := false
		for _, id := range maintenanceIntegrations(w) {
			if id == integrationID {
				covers = true
				break
			}
		}
		if !covers {
			continue
		}
		// Overlapping windows are allowed — a long one for a datacentre move and
		// a short one for a service inside it. The one that ends last wins, so
		// the silence lasts as long as any window says it should.
		if found == nil {
			found = w
			continue
		}
		if prev, err := utils.ParseDatetime(utils.StrVal(found, "ends_at")); err == nil && ends.After(prev) {
			found = w
		}
	}
	return found
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

func (e *Engine) CreateMaintenanceWindow(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"name", "starts_at", "ends_at"}); err != nil {
		return nil, errValidation(err.Error())
	}
	starts, ends, err := maintenanceBounds(payload)
	if err != nil {
		return nil, err
	}
	integrationIDs, err := maintenanceIntegrationsArg(payload)
	if err != nil {
		return nil, err
	}
	if err := e.ensureItemsExist(ctx, "integrations", "unknown integrations", integrationIDs); err != nil {
		return nil, err
	}
	teamID := nilIfEmpty(utils.StrVal(payload, "team_id"))
	if teamID != nil {
		if err := e.ensureTeamsExist(ctx, []string{teamID.(string)}); err != nil {
			return nil, err
		}
	}
	ts := utils.ToISO(utils.UTCNow())
	result, err := e.store.UpdateCollections(ctx, []string{"maintenance_windows"}, []string{"maintenance_windows"},
		func(state *store.State) (any, error) {
			window := map[string]any{
				"id":              utils.MakeID("mnt"),
				"name":            fmt.Sprintf("%v", payload["name"]),
				"reason":          utils.StrVal(payload, "reason"),
				"team_id":         teamID,
				"integration_ids": toAnySlice(integrationIDs),
				"starts_at":       utils.ToISO(starts),
				"ends_at":         utils.ToISO(ends),
				"created_at":      ts,
				"updated_at":      ts,
			}
			stampProvisioner(ctx, window)
			state.MaintenanceWindows[window["id"].(string)] = window
			e.auditIn(state, ctx, AuditCreate, "maintenance_window", window["id"].(string), map[string]any{
				"integration_ids": integrationIDs,
				"starts_at":       window["starts_at"],
				"ends_at":         window["ends_at"],
			})
			return window, nil
		}, advisoryLock["create_maintenance_window"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) UpdateMaintenanceWindow(ctx context.Context, windowID string, payload map[string]any) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	// Both bounds are validated together, so a payload that moves only one of
	// them is checked against the stored value of the other rather than against
	// nothing. Read here, outside the mutator, because that is where the
	// existing row is available without a nested load.
	existing, err := e.store.GetItem(ctx, "maintenance_windows", windowID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, errNotFound("maintenance window not found")
	}
	if err := e.authorizeItem(ctx, "maintenance_windows", existing); err != nil {
		return nil, err
	}
	if err := guardProvisioned(ctx, "maintenance_windows", existing); err != nil {
		return nil, err
	}
	merged := map[string]any{
		"starts_at": utils.StrVal(existing, "starts_at"),
		"ends_at":   utils.StrVal(existing, "ends_at"),
	}
	for _, key := range []string{"starts_at", "ends_at"} {
		if v, ok := payload[key]; ok {
			merged[key] = v
		}
	}
	starts, ends, err := maintenanceBounds(merged)
	if err != nil {
		return nil, err
	}
	var integrationIDs []string
	if _, ok := payload["integration_ids"]; ok {
		integrationIDs, err = maintenanceIntegrationsArg(payload)
		if err != nil {
			return nil, err
		}
		if err := e.ensureItemsExist(ctx, "integrations", "unknown integrations", integrationIDs); err != nil {
			return nil, err
		}
	}

	result, err := e.store.UpdateCollectionsFiltered(ctx,
		[]store.LoadSpec{{Collection: "maintenance_windows"}},
		[]string{"maintenance_windows"},
		func(state *store.State) (any, error) {
			window, ok := state.MaintenanceWindows[windowID]
			if !ok {
				return nil, errNotFound("maintenance window not found")
			}
			if name := utils.StrVal(payload, "name"); name != "" {
				window["name"] = name
			}
			if _, ok := payload["reason"]; ok {
				window["reason"] = utils.StrVal(payload, "reason")
			}
			if integrationIDs != nil {
				window["integration_ids"] = toAnySlice(integrationIDs)
			}
			window["starts_at"] = utils.ToISO(starts)
			window["ends_at"] = utils.ToISO(ends)
			window["updated_at"] = ts
			state.MaintenanceWindows[windowID] = window
			e.auditIn(state, ctx, AuditUpdate, "maintenance_window", windowID, map[string]any{
				"starts_at": window["starts_at"],
				"ends_at":   window["ends_at"],
			})
			return window, nil
		}, advisoryLock["maintenance_window"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

// maintenanceBounds validates the window's interval.
func maintenanceBounds(payload map[string]any) (time.Time, time.Time, error) {
	starts, err := utils.ParseDatetime(utils.StrVal(payload, "starts_at"))
	if err != nil {
		return time.Time{}, time.Time{}, errValidation("starts_at must be an RFC3339 timestamp")
	}
	ends, err := utils.ParseDatetime(utils.StrVal(payload, "ends_at"))
	if err != nil {
		return time.Time{}, time.Time{}, errValidation("ends_at must be an RFC3339 timestamp")
	}
	if !ends.After(starts) {
		// A window that ends before it starts covers nothing, which would be
		// discovered during the maintenance it was supposed to cover.
		return time.Time{}, time.Time{}, errValidation("ends_at must be after starts_at")
	}
	return starts, ends, nil
}

// maintenanceIntegrationsArg reads and validates integration_ids.
func maintenanceIntegrationsArg(payload map[string]any) ([]string, error) {
	raw, ok := payload["integration_ids"].([]any)
	if !ok {
		return nil, errValidation("integration_ids must be a list of integration ids")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		id := strings.TrimSpace(fmt.Sprintf("%v", v))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		// Deliberately not "empty means everything": the cost of a typo would be
		// a deployment that pages nobody, discovered by nobody being paged.
		return nil, errValidation("integration_ids must name at least one integration")
	}
	sort.Strings(out)
	return out, nil
}

// silenceForMaintenance puts a freshly created group straight into the silenced
// state for the remainder of the window. A nil window silences nothing.
//
// Runs inside the ingest advisory lock against pre-loaded windows.
func silenceForMaintenance(g model.AlertGroup, window map[string]any, now time.Time, ts string) bool {
	if window == nil {
		return false
	}
	ends, err := utils.ParseDatetime(utils.StrVal(window, "ends_at"))
	if err != nil {
		return false
	}
	minutes := int(ends.Sub(now).Minutes())
	if minutes < 1 {
		minutes = 1
	}
	name := utils.StrVal(window, "name")
	// The system actor, not the person who created the window: nobody decided
	// to silence this particular group, a rule did, and the audit trail should
	// not read as though somebody sat there silencing alerts at 3am.
	if err := g.Silence(ts, utils.ToISO(ends), "Maintenance window: "+name, minutes, authz.SystemActor); err != nil {
		return false
	}
	g.AppendLog("maintenance_silenced",
		"Silenced by maintenance window "+name,
		map[string]any{"window_id": utils.StrVal(window, "id"), "until": utils.ToISO(ends)})
	return true
}
