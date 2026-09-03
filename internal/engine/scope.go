package engine

import (
	"context"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// scope.go enforces team boundaries.
//
// The rule, in full:
//
//   - An object with no team is visible to everyone. That is what makes turning
//     scoping on a no-op for a deployment that has never assigned a team.
//   - An object owned by a team is visible to that team's members, and to any
//     actor that is not team-scoped (admins, API keys, the worker).
//   - Someone who belongs to no team therefore sees the unassigned objects and
//     nothing else. Removing a person from a team narrows their view; it never
//     widens it.
//
// Only two collections carry a team of their own — schedules and ChatOps
// channels, which had team_id already, and integrations, which gained it in
// migration 0020. Everything downstream of an integration (alerts, alert
// groups) is scoped through integration_id rather than by copying team_id onto
// each row: a copy would be wrong the moment an integration is reassigned,
// while the join key is always current.
//
// Delivery diagnostics are scoped too, each by the nearest thing it belongs to:
//
//   - Notifications carry integration_id, denormalised from their alert group in
//     migration 0021, so they are narrowed by the same filter as the groups.
//   - Delivery attempts have no key of their own that a filter can use. A scoped
//     caller must name a notification, and that notification is authorised
//     before its attempts are returned; the unfiltered feed would be every
//     attempt in the deployment. See GetDeliveryAttempts.
//   - History is scoped at the group query, which is what bounds the
//     notifications and attempts it inlines. Scoping it later would mean the
//     rows had already been read.
//
// This matters more than diagnostics usually do: a notification carries its
// target, which is somebody's Telegram id, phone number or address.

// teamOwnedCollections carry team_id directly.
var teamOwnedCollections = map[string]bool{
	"integrations":     true,
	"schedules":        true,
	"chatops_channels": true,
	// A window names the integrations it covers, but a scoped caller must not be
	// able to read or edit another team's maintenance plan, so the window
	// carries its own team like the three above.
	"maintenance_windows": true,
}

// integrationScopedCollections are reached through an integration. Notifications
// carry integration_id denormalised from their alert group (migration 0021);
// see that migration for why a copied integration_id cannot go stale while a
// copied team_id would.
var integrationScopedCollections = map[string]bool{
	"alerts":        true,
	"alert_groups":  true,
	"notifications": true,
}

// applyScopeFilters narrows a list query to what the actor may see, returning
// false when the query cannot match anything at all.
//
// The filters map is a conjunction, so an explicit caller-supplied filter is
// intersected with the scope rather than replacing it: asking for one
// integration outside your teams yields nothing, not everything.
func (e *Engine) applyScopeFilters(ctx context.Context, collection string, filters map[string]any) (bool, error) {
	actor := authz.FromContext(ctx)
	if !actor.TeamScoped {
		return true, nil
	}
	switch {
	case teamOwnedCollections[collection]:
		filters["team_id"] = store.InOrNullFilter{Values: toAnySlice(actor.TeamIDs)}
		return true, nil
	case integrationScopedCollections[collection]:
		ids, err := e.visibleIntegrationIDs(ctx, actor)
		if err != nil {
			return false, err
		}
		if len(ids) == 0 {
			return false, nil // no reachable integration: the result is empty
		}
		if wanted, ok := filters["integration_id"]; ok {
			// Intersect rather than widen.
			if !containsAny(ids, wanted) {
				return false, nil
			}
			return true, nil
		}
		filters["integration_id"] = ids
		return true, nil
	default:
		return true, nil
	}
}

// visibleIntegrationIDs lists the integrations the actor may reach: those owned
// by one of their teams plus the unassigned ones.
func (e *Engine) visibleIntegrationIDs(ctx context.Context, actor authz.Actor) ([]any, error) {
	items, _, err := e.store.ListCollectionPage(ctx, "integrations",
		map[string]any{"team_id": store.InOrNullFilter{Values: toAnySlice(actor.TeamIDs)}},
		maxScopedIntegrations, 0, store.SortSpec{})
	if err != nil {
		return nil, err
	}
	ids := make([]any, 0, len(items))
	for _, item := range items {
		ids = append(ids, utils.StrVal(item, "id"))
	}
	return ids, nil
}

// maxScopedIntegrations bounds the IN list built for a scoped query. The beta
// target is a handful of teams with tens of integrations; a deployment that
// exceeds this wants team_id denormalised onto alert_groups instead, and should
// find out by seeing this constant rather than by silently losing rows — hence
// the log in authorizeItem's caller path when the cap is hit.
const maxScopedIntegrations = 1000

// authorizeItem reports whether the actor may see or act on one loaded object.
//
// List queries are narrowed by applyScopeFilters; this is the guard for the
// paths that address an object directly by id, where a filter cannot help.
func (e *Engine) authorizeItem(ctx context.Context, collection string, item map[string]any) error {
	actor := authz.FromContext(ctx)
	if !actor.TeamScoped || item == nil {
		return nil
	}
	switch {
	case teamOwnedCollections[collection]:
		if !actor.MayAccessTeam(utils.StrVal(item, "team_id")) {
			return errForbiddenTeam(collection)
		}
	case integrationScopedCollections[collection]:
		integrationID := utils.StrVal(item, "integration_id")
		if integrationID == "" {
			return nil // not attached to an integration; nothing to scope by
		}
		integration, err := e.store.GetItem(ctx, "integrations", integrationID)
		if err != nil {
			return err
		}
		// A missing integration (hard-deleted, or a group older than its
		// integration) is treated as unassigned rather than as forbidden: it
		// carries no team, so no team boundary is being crossed.
		if integration == nil {
			return nil
		}
		if !actor.MayAccessTeam(utils.StrVal(integration, "team_id")) {
			return errForbiddenTeam(collection)
		}
	}
	return nil
}

// authorizeGroupAction is authorizeItem for an alert group addressed by id. It
// is the guard on acknowledge / resolve / silence and their inverses.
func (e *Engine) authorizeGroupAction(ctx context.Context, groupID string) error {
	if !authz.FromContext(ctx).TeamScoped {
		return nil
	}
	group, err := e.store.GetItem(ctx, "alert_groups", groupID)
	if err != nil {
		return err
	}
	if group == nil {
		return nil // let the operation itself report "not found"
	}
	return e.authorizeItem(ctx, "alert_groups", group)
}

// partitionAuthorizedGroups splits alert group ids into those the actor may act
// on and those blocked by a team boundary.
//
// Bulk operations filter rather than fail: refusing the whole batch because one
// id belongs to another team would make a selection made in the UI unusable,
// and the caller is told exactly which ids were dropped.
func (e *Engine) partitionAuthorizedGroups(ctx context.Context, ids []string) (allowed, forbidden []string, err error) {
	if !authz.FromContext(ctx).TeamScoped {
		return ids, nil, nil
	}
	for _, id := range ids {
		switch err := e.authorizeGroupAction(ctx, id); {
		case err == nil:
			allowed = append(allowed, id)
		case isForbidden(err):
			forbidden = append(forbidden, id)
		default:
			return nil, nil, err
		}
	}
	return allowed, forbidden, nil
}

// containsAny reports whether want (a filter value, always a scalar here) is in
// the allowed set.
func containsAny(allowed []any, want any) bool {
	for _, v := range allowed {
		if v == want {
			return true
		}
	}
	return false
}
