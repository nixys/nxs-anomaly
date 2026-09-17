package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// ErrConflict is returned when an operation would leave other objects pointing
// at something that no longer exists.
var ErrConflict = errors.New("conflict")

type conflictError struct{ msg string }

func (e *conflictError) Error() string   { return e.msg }
func (e *conflictError) Is(t error) bool { return t == ErrConflict }

// deleteBlockedByReferences refuses to delete an object that paging still
// depends on.
//
// References are validated when they are written — a chain cannot name an
// unknown user — but deleting the target used to succeed silently. The chain
// then logged "missing_chain" or skipped "unknown users" at the moment an alert
// arrived, which is the worst moment to learn it. The answer names every
// dependent object so it can be unlinked first.
func (e *Engine) deleteBlockedByReferences(ctx context.Context, collection, id string) error {
	refs, err := e.pagingReferences(ctx, collection, id, utils.UTCNow())
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	sort.Strings(refs)
	return &conflictError{fmt.Sprintf("%s %s is still used by: %s",
		singular(collection), id, strings.Join(refs, "; "))}
}

func (e *Engine) pagingReferences(ctx context.Context, collection, id string, now time.Time) ([]string, error) {
	var refs []string
	switch collection {
	case "users", "teams", "schedules":
		chains, err := e.store.ListCollection(ctx, "escalation_chains")
		if err != nil {
			return nil, err
		}
		for _, chain := range chains {
			for i, raw := range anyList(chain["steps"]) {
				step, _ := raw.(map[string]any)
				if stepReferences(step, collection, id) {
					refs = append(refs, fmt.Sprintf("escalation chain %q step %d (%s)",
						utils.StrVal(chain, "name"), i+1, utils.StrVal(step, "kind")))
				}
			}
		}
	case "escalation_chains":
		integrations, err := e.store.ListCollection(ctx, "integrations")
		if err != nil {
			return nil, err
		}
		for _, integ := range integrations {
			if utils.StrVal(integ, "deleted_at") != "" {
				continue
			}
			for _, raw := range anyList(integ["routes"]) {
				route, _ := raw.(map[string]any)
				if utils.StrVal(route, "escalation_chain_id") == id {
					refs = append(refs, fmt.Sprintf("integration %q route %q",
						utils.StrVal(integ, "name"), utils.StrVal(route, "name")))
				}
			}
		}
	}
	if collection != "users" {
		return refs, nil
	}

	integrations, err := e.store.ListCollection(ctx, "integrations")
	if err != nil {
		return nil, err
	}
	for _, integ := range integrations {
		if utils.StrVal(integ, "deleted_at") != "" {
			continue
		}
		policy, _ := integ["notification_policy"].(map[string]any)
		for _, field := range []string{"emergency_user_id", "epic_user_id"} {
			if utils.StrVal(policy, field) == id {
				refs = append(refs, fmt.Sprintf("integration %q notification policy (%s)", utils.StrVal(integ, "name"), field))
			}
		}
	}
	schedules, err := e.store.ListCollection(ctx, "schedules")
	if err != nil {
		return nil, err
	}
	for _, sched := range schedules {
		name := utils.StrVal(sched, "name")
		if rot, ok := sched["rotation"].(map[string]any); ok && utils.BoolVal(rot, "enabled", true) {
			ids, _ := utils.CoerceStringList(rot["participant_ids"])
			for _, pid := range ids {
				if pid == id {
					refs = append(refs, fmt.Sprintf("schedule %q rotation", name))
					break
				}
			}
		}
		// Past shifts and overrides are history, not paging: they are removed
		// with the user, as before.
		for _, raw := range anyList(sched["shifts"]) {
			shift, _ := raw.(map[string]any)
			if utils.StrVal(shift, "user_id") == id && entryStillPages(shift, "end_at", now) {
				refs = append(refs, fmt.Sprintf("schedule %q shift", name))
				break
			}
		}
		for _, raw := range anyList(sched["overrides"]) {
			ovr, _ := raw.(map[string]any)
			if utils.StrVal(ovr, "user_id") == id && entryStillPages(ovr, "until", now) {
				refs = append(refs, fmt.Sprintf("schedule %q override", name))
				break
			}
		}
	}
	return refs, nil
}

func stepReferences(step map[string]any, collection, id string) bool {
	switch collection {
	case "users":
		if utils.StrVal(step, "user_id") == id {
			return true
		}
		for _, uid := range anyToStringSlice(step["user_ids"]) {
			if uid == id {
				return true
			}
		}
	case "teams":
		return utils.StrVal(step, "team_id") == id
	case "schedules":
		return utils.StrVal(step, "schedule_id") == id
	}
	return false
}

// entryStillPages reports whether a shift or override can still put someone on
// call: a recurring shift always can, a one-off one until it ends.
func entryStillPages(entry map[string]any, endField string, now time.Time) bool {
	if rec := strings.ToLower(utils.StrVal(entry, "recurrence")); rec != "" && rec != "none" {
		return true
	}
	end, err := utils.ParseDatetime(utils.StrVal(entry, endField))
	return err != nil || end.After(now)
}
