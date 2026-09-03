package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// ── Schedule v2: rotation validation, overrides and preview ───────────────────

// parseScheduleTimezone validates the IANA zone name before it is stored. The
// read path falls back to UTC for an unloadable zone (a schedule must still
// page someone), so the write path is the only place a typo can be reported.
func parseScheduleTimezone(payload map[string]any, def string) (string, error) {
	name := strDefault(utils.StrVal(payload, "timezone"), def)
	if _, err := time.LoadLocation(name); err != nil {
		return "", errValidation(fmt.Sprintf("unknown timezone: %s", name))
	}
	return name, nil
}

// parseScheduleRotation validates a rotation payload and checks that every
// participant exists, so a rotation cannot silently page a deleted user.
func (e *Engine) parseScheduleRotation(ctx context.Context, raw any) (*rotation, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errValidation("rotation must be an object")
	}
	rot, err := parseRotation(m)
	if err != nil {
		return nil, errValidation(err.Error())
	}
	if rot == nil {
		return nil, nil
	}
	if len(rot.participantIDs) > 0 {
		if err := e.ensureUsersExist(ctx, rot.participantIDs); err != nil {
			return nil, err
		}
	}
	return rot, nil
}

// actorRef identifies who performed an action for storage inside an entity.
// The audit trail is the authoritative record; this is the copy an operator
// reading the schedule itself can see without cross-referencing it.
func actorRef(ctx context.Context) any {
	actor := authz.FromContext(ctx)
	if actor.ID == "" {
		return nil
	}
	return map[string]any{
		"id":   actor.ID,
		"kind": actor.Kind,
		"name": actor.Describe(),
	}
}

// UpdateScheduleOverride edits an existing override in place. Only the fields
// present in the payload change, so a UI that only wants to extend an override
// need not resend the user and reason.
func (e *Engine) UpdateScheduleOverride(ctx context.Context, schedID, overrideID string, payload map[string]any) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	if v := utils.StrVal(payload, "user_id"); v != "" {
		if err := e.ensureUsersExist(ctx, []string{v}); err != nil {
			return nil, err
		}
	}

	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("schedules", schedID), []string{"schedules"},
		func(state *store.State) (any, error) {
			sched := state.Schedules[schedID]
			if sched == nil {
				return nil, errNotFound(fmt.Sprintf("schedule %s not found", schedID))
			}
			if err := e.authorizeItem(ctx, "schedules", sched); err != nil {
				return nil, err
			}
			override := findOverride(sched, overrideID)
			if override == nil {
				return nil, errNotFound(fmt.Sprintf("override %s not found", overrideID))
			}
			if v := utils.StrVal(payload, "user_id"); v != "" {
				override["user_id"] = v
			}
			if v := utils.StrVal(payload, "start_at"); v != "" {
				t, err := utils.ParseDatetime(v)
				if err != nil {
					return nil, errValidation(fmt.Sprintf("invalid start_at: %v", err))
				}
				override["start_at"] = utils.ToISO(t)
			}
			if v := utils.StrVal(payload, "until"); v != "" {
				t, err := utils.ParseDatetime(v)
				if err != nil {
					return nil, errValidation(fmt.Sprintf("invalid until: %v", err))
				}
				override["until"] = utils.ToISO(t)
			}
			if v, ok := payload["reason"]; ok {
				override["reason"] = fmt.Sprintf("%v", v)
			}
			start, err1 := utils.ParseDatetime(utils.StrVal(override, "start_at"))
			until, err2 := utils.ParseDatetime(utils.StrVal(override, "until"))
			if err1 != nil || err2 != nil || !until.After(start) {
				return nil, errValidation("until must be after start_at")
			}
			override["updated_at"] = ts
			override["updated_by"] = actorRef(ctx)
			sched["updated_at"] = ts
			e.auditIn(state, ctx, AuditUpdate, "schedule", schedID,
				auditFields(payload, map[string]any{"override_id": overrideID}))
			return map[string]any{"schedule": sched, "override": override}, nil
		}, advisoryLock["update_schedule_override"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

// DeleteScheduleOverride removes an override. Deleting an override that is
// currently active hands the slot straight back to the rotation, which is the
// intended way to cancel cover that is no longer needed.
func (e *Engine) DeleteScheduleOverride(ctx context.Context, schedID, overrideID string) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("schedules", schedID), []string{"schedules"},
		func(state *store.State) (any, error) {
			sched := state.Schedules[schedID]
			if sched == nil {
				return nil, errNotFound(fmt.Sprintf("schedule %s not found", schedID))
			}
			if err := e.authorizeItem(ctx, "schedules", sched); err != nil {
				return nil, err
			}
			kept := make([]any, 0, len(anyList(sched["overrides"])))
			found := false
			for _, raw := range anyList(sched["overrides"]) {
				o, ok := raw.(map[string]any)
				if ok && utils.StrVal(o, "id") == overrideID {
					found = true
					continue
				}
				kept = append(kept, raw)
			}
			if !found {
				return nil, errNotFound(fmt.Sprintf("override %s not found", overrideID))
			}
			sched["overrides"] = kept
			sched["updated_at"] = ts
			e.auditIn(state, ctx, AuditDelete, "schedule", schedID, map[string]any{"override_id": overrideID})
			return sched, nil
		}, advisoryLock["delete_schedule_override"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func findOverride(sched map[string]any, overrideID string) map[string]any {
	for _, raw := range anyList(sched["overrides"]) {
		o, ok := raw.(map[string]any)
		if ok && utils.StrVal(o, "id") == overrideID {
			return o
		}
	}
	return nil
}

// PreviewSchedule renders the on-call timeline for a window, defaulting to the
// next four weeks, together with the coverage gaps and overlaps an operator
// needs to see before trusting the schedule with production pages.
func (e *Engine) PreviewSchedule(ctx context.Context, schedID, fromStr, toStr string) (map[string]any, error) {
	state, err := e.store.ReadCollections(ctx, []string{"schedules", "users"})
	if err != nil {
		return nil, err
	}
	sched := state.Schedules[schedID]
	if sched == nil {
		return nil, errNotFound(fmt.Sprintf("schedule %s not found", schedID))
	}
	if err := e.authorizeItem(ctx, "schedules", sched); err != nil {
		return nil, err
	}
	from := utils.UTCNow()
	if fromStr != "" {
		t, err := utils.ParseDatetime(fromStr)
		if err != nil {
			return nil, errValidation(fmt.Sprintf("invalid from: %v", err))
		}
		from = t
	}
	to := from.Add(defaultPreviewWindow)
	if toStr != "" {
		t, err := utils.ParseDatetime(toStr)
		if err != nil {
			return nil, errValidation(fmt.Sprintf("invalid to: %v", err))
		}
		to = t
	}
	if !to.After(from) {
		return nil, errValidation("to must be after from")
	}
	if to.Sub(from) > maxPreviewWindow {
		to = from.Add(maxPreviewWindow)
	}

	known := knownUserIDs(state.Users)
	ghosts := ghostParticipants(sched, known)
	segments := applyRoster(scheduleTimeline(sched, from, to), known)
	items := make([]any, 0, len(segments))
	gaps := make([]any, 0)
	overlaps := make([]any, 0)
	var covered time.Duration
	participants := map[string]bool{}
	for _, seg := range segments {
		items = append(items, segmentJSON(seg))
		switch {
		case len(seg.UserIDs) == 0:
			gaps = append(gaps, segmentJSON(seg))
		default:
			covered += seg.End.Sub(seg.Start)
			if len(seg.UserIDs) > 1 {
				overlaps = append(overlaps, segmentJSON(seg))
			}
			for _, uid := range seg.UserIDs {
				participants[uid] = true
			}
		}
	}

	users := make([]any, 0, len(participants))
	for uid := range participants {
		if u := state.Users[uid]; u != nil {
			users = append(users, map[string]any{"id": uid, "name": utils.StrVal(u, "name")})
		} else {
			users = append(users, map[string]any{"id": uid, "name": uid})
		}
	}

	warnings := make([]any, 0)
	if !utils.BoolVal(sched, "enabled", true) {
		warnings = append(warnings, "schedule is disabled: nobody will be paged")
	}
	if len(ghosts) > 0 {
		warnings = append(warnings,
			fmt.Sprintf("%d participant(s) no longer exist and page nobody: %s",
				len(ghosts), strings.Join(ghosts, ", ")))
	}
	if len(gaps) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d coverage gap(s) in the previewed window", len(gaps)))
	}
	if len(overlaps) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d overlapping shift interval(s): more than one user on call", len(overlaps)))
	}

	total := to.Sub(from)
	ratio := 0.0
	if total > 0 {
		ratio = float64(covered) / float64(total)
	}
	return map[string]any{
		"schedule_id":    schedID,
		"timezone":       utils.StrVal(sched, "timezone"),
		"from":           utils.ToISO(from),
		"to":             utils.ToISO(to),
		"segments":       items,
		"gaps":           gaps,
		"overlaps":       overlaps,
		"warnings":       warnings,
		"coverage_ratio": ratio,
		"participants":   users,
		"unknown_users":  toAnySlice(ghosts),
	}, nil
}

// knownUserIDs turns a loaded users collection into a membership set.
func knownUserIDs(users map[string]map[string]any) map[string]bool {
	known := make(map[string]bool, len(users))
	for id := range users {
		known[id] = true
	}
	return known
}

// scheduleNamedUserIDs lists every user a schedule refers to, across the
// rotation, the legacy shifts and the overrides.
func scheduleNamedUserIDs(sched map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	if rot, ok := sched["rotation"].(map[string]any); ok {
		ids, _ := utils.CoerceStringList(rot["participant_ids"])
		for _, id := range ids {
			add(id)
		}
	}
	for _, raw := range anyList(sched["shifts"]) {
		if s, ok := raw.(map[string]any); ok {
			add(utils.StrVal(s, "user_id"))
		}
	}
	for _, raw := range anyList(sched["overrides"]) {
		if o, ok := raw.(map[string]any); ok {
			add(utils.StrVal(o, "user_id"))
		}
	}
	sort.Strings(out)
	return out
}

// ghostParticipants lists user IDs a schedule still names but that no longer
// exist — deleting a user does not rewrite history, and a rotation naming a
// deleted person is a silent hole rather than a loud error.
func ghostParticipants(sched map[string]any, known map[string]bool) []string {
	var out []string
	for _, id := range scheduleNamedUserIDs(sched) {
		if !known[id] {
			out = append(out, id)
		}
	}
	return out
}

// assertScheduleCovered rejects a schedule that leaves part of the next week
// unassigned. The window is short on purpose: a chain must not depend on
// coverage nobody has planned yet, but it also must not be blocked by a
// rotation that simply has not been extended to next quarter.
func (e *Engine) assertScheduleCovered(ctx context.Context, schedID string) error {
	sched, err := e.store.GetItem(ctx, "schedules", schedID)
	if err != nil {
		return err
	}
	if sched == nil {
		return nil // existence is checked by the caller
	}
	known, err := e.scheduleRoster(ctx, sched)
	if err != nil {
		return err
	}
	now := utils.UTCNow()
	gaps := scheduleGaps(sched, now, now.Add(coverageCheckWindow), known)
	if len(gaps) == 0 {
		return nil
	}
	return errValidation(fmt.Sprintf(
		"schedule %s has %d coverage gap(s) in the next %d days (first: %s → %s); "+
			"fix the schedule or set allow_uncovered=true on this step to accept it",
		schedID, len(gaps), int(coverageCheckWindow.Hours()/24),
		utils.ToISO(gaps[0].Start), utils.ToISO(gaps[0].End)))
}

// scheduleRoster resolves which of the users a schedule names still exist.
// Point lookups rather than a full users read: a schedule names a handful of
// people, and this runs on the chain write path.
func (e *Engine) scheduleRoster(ctx context.Context, sched map[string]any) (map[string]bool, error) {
	known := map[string]bool{}
	for _, id := range scheduleNamedUserIDs(sched) {
		item, err := e.store.GetItem(ctx, "users", id)
		if err != nil {
			return nil, err
		}
		known[id] = item != nil
	}
	return known, nil
}

// purgeUserFromSchedules removes a deleted user from every schedule that names
// them: rotation participants, legacy shifts and overrides.
//
// Without this a rotation keeps handing the pager to a person who no longer
// exists, and nothing pages for that whole slot. Reported, not returned as an
// error: the user row is already gone by the time this runs, and failing here
// would report a deletion that did happen as failed.
func (e *Engine) purgeUserFromSchedules(ctx context.Context, userID string) (int, error) {
	result, err := e.store.UpdateCollections(ctx, []string{"schedules"}, []string{"schedules"},
		func(state *store.State) (any, error) {
			touched := 0
			for _, sched := range state.Schedules {
				changed := false
				if rot, ok := sched["rotation"].(map[string]any); ok {
					ids, _ := utils.CoerceStringList(rot["participant_ids"])
					kept := make([]string, 0, len(ids))
					for _, id := range ids {
						if id != userID {
							kept = append(kept, id)
						}
					}
					if len(kept) != len(ids) {
						rot["participant_ids"] = toAnySlice(kept)
						changed = true
					}
				}
				for _, key := range []string{"shifts", "overrides"} {
					entries := anyList(sched[key])
					kept := make([]any, 0, len(entries))
					for _, raw := range entries {
						if m, ok := raw.(map[string]any); ok && utils.StrVal(m, "user_id") == userID {
							continue
						}
						kept = append(kept, raw)
					}
					if len(kept) != len(entries) {
						sched[key] = kept
						changed = true
					}
				}
				if changed {
					sched["updated_at"] = utils.ToISO(utils.UTCNow())
					touched++
				}
			}
			return touched, nil
		}, advisoryLock["purge_user_from_schedules"])
	if err != nil {
		return 0, err
	}
	return result.(int), nil
}

// ScheduleCoverageReport re-checks every schedule that an escalation chain
// actually depends on.
//
// The write-time gate can only judge a schedule as it was when the chain was
// saved; the schedule can be edited into holes, disabled, or lose a
// participant afterwards. This is the standing check that finds that, for the
// worker's metric, for the readiness page and for whoever is on call tonight.
func (e *Engine) ScheduleCoverageReport(ctx context.Context) (map[string]any, error) {
	// Reference collections through the short-TTL cache: this runs on every
	// worker cycle, and schedules, users and chains change on human timescales.
	schedules, err := e.refCollection(ctx, "schedules")
	if err != nil {
		return nil, err
	}
	users, err := e.refCollection(ctx, "users")
	if err != nil {
		return nil, err
	}
	chains, err := e.refCollection(ctx, "escalation_chains")
	if err != nil {
		return nil, err
	}
	known := knownUserIDs(users)
	now := utils.UTCNow()

	// Which chains reference which schedule, and whether the step accepted the
	// gaps deliberately.
	type ref struct {
		chains    []any
		acceptAll bool
	}
	refs := map[string]*ref{}
	for _, chain := range chains {
		for _, raw := range anyList(chain["steps"]) {
			step, ok := raw.(map[string]any)
			if !ok || utils.StrVal(step, "kind") != StepNotifySchedule {
				continue
			}
			schedID := utils.StrVal(step, "schedule_id")
			if schedID == "" {
				continue
			}
			r := refs[schedID]
			if r == nil {
				r = &ref{acceptAll: true}
				refs[schedID] = r
			}
			r.chains = append(r.chains, map[string]any{
				"id": utils.StrVal(chain, "id"), "name": utils.StrVal(chain, "name"),
			})
			if !utils.BoolVal(step, "allow_uncovered", false) {
				r.acceptAll = false
			}
		}
	}

	items := make([]any, 0)
	degraded := 0
	visible := 0
	for id, sched := range schedules {
		// Team scoping applies here as it does to the schedule list: the report
		// names schedules and the chains that page through them.
		if err := e.authorizeItem(ctx, "schedules", sched); err != nil {
			continue
		}
		visible++
		gaps := scheduleGaps(sched, now, now.Add(coverageCheckWindow), known)
		ghosts := ghostParticipants(sched, known)
		disabled := !utils.BoolVal(sched, "enabled", true)
		if len(gaps) == 0 && len(ghosts) == 0 && !disabled {
			continue
		}
		r := refs[id]
		entry := map[string]any{
			"schedule_id":   id,
			"name":          utils.StrVal(sched, "name"),
			"disabled":      disabled,
			"gap_count":     len(gaps),
			"unknown_users": toAnySlice(ghosts),
			"attached_to":   []any{},
			"acknowledged":  false,
		}
		if len(gaps) > 0 {
			entry["first_gap"] = segmentJSON(gaps[0])
		}
		if r != nil {
			entry["attached_to"] = r.chains
			entry["acknowledged"] = r.acceptAll
			// Only a schedule a chain actually pages through counts as
			// degraded coverage; an unattached draft is nobody's incident.
			if !r.acceptAll {
				degraded++
			}
		}
		items = append(items, entry)
	}
	sort.Slice(items, func(i, j int) bool {
		return utils.StrVal(items[i].(map[string]any), "name") <
			utils.StrVal(items[j].(map[string]any), "name")
	})
	return map[string]any{
		"checked_at":        utils.ToISO(now),
		"window_days":       int(coverageCheckWindow.Hours() / 24),
		"items":             items,
		"degraded_attached": degraded,
		// Counted after scoping, so "N degraded of M" is a statement about the
		// same set of schedules the caller can see.
		"schedules_total":    visible,
		"schedules_degraded": len(items),
	}, nil
}
