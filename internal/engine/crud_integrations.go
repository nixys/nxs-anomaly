package engine

import (
	"context"
	"fmt"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// ── Integrations ──────────────────────────────────────────────────────────────

func (e *Engine) CreateIntegration(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"name"}); err != nil {
		return nil, errValidation(err.Error())
	}
	if err := rejectInlineSecret("webhook_secret", utils.StrVal(payload, "webhook_secret")); err != nil {
		return nil, err
	}
	ts := utils.ToISO(utils.UTCNow())
	policy, err := e.sanitizeNotificationPolicy(ctx, payload["notification_policy"])
	if err != nil {
		return nil, err
	}
	pool, err := e.sanitizeLegacyPool(ctx, payload["legacy_pool"])
	if err != nil {
		return nil, err
	}

	if err := e.ensureChainsExist(ctx, routeChainIDs(payload)); err != nil {
		return nil, err
	}
	// Owning team. Empty means unassigned, which is visible to everyone —
	// see internal/engine/scope.go for the full rule.
	teamID := nilIfEmpty(utils.StrVal(payload, "team_id"))
	if teamID != nil {
		if err := e.ensureTeamsExist(ctx, []string{teamID.(string)}); err != nil {
			return nil, err
		}
		// A scoped actor may only hand an integration to a team of their own,
		// otherwise creating one would be a way to place objects outside your
		// own boundary — or worse, inside someone else's.
		if !authz.FromContext(ctx).MayAccessTeam(teamID.(string)) {
			return nil, errForbiddenTeam("integrations")
		}
	}
	result, err := e.store.UpdateCollections(ctx, nil, []string{"integrations"},
		func(state *store.State) (any, error) {
			routes, err := sanitizeRoutes(payload)
			if err != nil {
				return nil, err
			}
			templates, err := sanitizeTemplates(payload["templates"])
			if err != nil {
				return nil, err
			}
			// Compiled here purely to reject a bad rule now. A pipeline that only
			// failed at ingest would be a 400 nobody sees and alerts nobody
			// enriched, discovered during an incident.
			if _, err := CompileAlertPipeline(payload["pipeline"]); err != nil {
				return nil, err
			}
			groupBy, _ := utils.CoerceStringList(payload["group_by"])
			if len(groupBy) == 0 {
				groupBy = []string{"alertname", "service"}
			}
			key := utils.StrVal(payload, "key")
			if key == "" {
				key = utils.MakeID("key")
			}
			itype := strDefault(utils.StrVal(payload, "type"), "webhook")
			integration := map[string]any{
				"id":                  utils.MakeID("int"),
				"name":                fmt.Sprintf("%v", payload["name"]),
				"key":                 key,
				"routing_key":         key,
				"type":                itype,
				"source_type":         strDefault(utils.StrVal(payload, "source_type"), itype),
				"group_by":            toAnySlice(groupBy),
				"routes":              routes,
				"notification_policy": policy,
				"legacy_pool":         pool,
				"templates":           templates,
				"pipeline":            payload["pipeline"],
				"team_id":             teamID,
				"heartbeat":           sanitizeHeartbeat(payload["heartbeat"]),
				"webhook_secret":      nilIfEmpty(utils.StrVal(payload, "webhook_secret")),
				"kafka_topic":         nilIfEmpty(utils.StrVal(payload, "kafka_topic")),
				"created_at":          ts,
				"updated_at":          ts,
			}
			stampProvisioner(ctx, integration)
			state.Integrations[integration["id"].(string)] = integration
			e.auditIn(state, ctx, AuditCreate, "integration", integration["id"].(string), auditFields(payload, nil))
			return integration, nil
		}, advisoryLock["create_integration"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) UpdateIntegration(ctx context.Context, integID string, payload map[string]any) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	var policy map[string]any
	var policySet bool
	if v, ok := payload["notification_policy"]; ok {
		var err error
		policySet = true
		policy, err = e.sanitizeNotificationPolicy(ctx, v)
		if err != nil {
			return nil, err
		}
	}
	var pool map[string]any
	var poolSet bool
	if v, ok := payload["legacy_pool"]; ok {
		var err error
		poolSet = true
		pool, err = e.sanitizeLegacyPool(ctx, v)
		if err != nil {
			return nil, err
		}
	}

	if _, ok := payload["routes"]; ok {
		if err := e.ensureChainsExist(ctx, routeChainIDs(payload)); err != nil {
			return nil, err
		}
	}
	// Reassigning ownership is checked on both sides: the actor must already
	// reach the integration (guarded below, inside the mutation) and must be
	// entitled to the team they are moving it to. Checking only one side would
	// let an integration be pushed out of reach or pulled into a foreign team.
	var teamID any
	teamIDSet := false
	if v, ok := payload["team_id"]; ok {
		teamIDSet = true
		teamID = nilIfEmpty(fmt.Sprintf("%v", v))
		if teamID != nil {
			if err := e.ensureTeamsExist(ctx, []string{teamID.(string)}); err != nil {
				return nil, err
			}
			if !authz.FromContext(ctx).MayAccessTeam(teamID.(string)) {
				return nil, errForbiddenTeam("integrations")
			}
		}
	}
	result, err := e.store.UpdateCollectionsFiltered(ctx,
		loadItems("integrations", integID),
		[]string{"integrations"},
		func(state *store.State) (any, error) {
			integ := state.Integrations[integID]
			if integ == nil {
				return nil, errNotFound(fmt.Sprintf("integration %s not found", integID))
			}
			if err := e.authorizeItem(ctx, "integrations", integ); err != nil {
				return nil, err
			}
			if err := guardProvisioned(ctx, "integrations", integ); err != nil {
				return nil, err
			}
			if teamIDSet {
				integ["team_id"] = teamID
			}
			if v, ok := payload["name"]; ok {
				integ["name"] = fmt.Sprintf("%v", v)
			}
			if v, ok := payload["type"]; ok {
				integ["type"] = strDefault(fmt.Sprintf("%v", v), "webhook")
			}
			if v, ok := payload["source_type"]; ok {
				integ["source_type"] = strDefault(fmt.Sprintf("%v", v), "webhook")
			}
			if v, ok := payload["group_by"]; ok {
				groupBy, _ := utils.CoerceStringList(v)
				if len(groupBy) == 0 {
					groupBy = []string{"alertname", "service"}
				}
				integ["group_by"] = toAnySlice(groupBy)
			}
			if _, ok := payload["routes"]; ok {
				routes, err := sanitizeRoutes(payload)
				if err != nil {
					return nil, err
				}
				integ["routes"] = routes
			}
			if policySet {
				integ["notification_policy"] = policy
			}
			if poolSet {
				integ["legacy_pool"] = pool
			}
			if v, ok := payload["templates"]; ok {
				templates, err := sanitizeTemplates(v)
				if err != nil {
					return nil, err
				}
				integ["templates"] = templates
			}
			if v, ok := payload["pipeline"]; ok {
				if _, err := CompileAlertPipeline(v); err != nil {
					return nil, err
				}
				integ["pipeline"] = v
			}
			if v, ok := payload["heartbeat"]; ok {
				integ["heartbeat"] = sanitizeHeartbeat(v)
				// Turning the check off clears the state with it: leaving the
				// marker behind would make a later re-enable start from "was
				// silent" and resolve an alert nobody ever saw.
				if _, _, enabled := heartbeatSettings(integ); !enabled {
					delete(integ, "heartbeat_silent_since")
				}
			}
			if v, ok := payload["webhook_secret"]; ok {
				if v == nil || v == "" {
					integ["webhook_secret"] = nil
				} else {
					sv := fmt.Sprintf("%v", v)
					if err := rejectInlineSecret("webhook_secret", sv); err != nil {
						return nil, err
					}
					integ["webhook_secret"] = sv
				}
			}
			if v, ok := payload["kafka_topic"]; ok {
				if v == nil || v == "" {
					integ["kafka_topic"] = nil
				} else {
					integ["kafka_topic"] = fmt.Sprintf("%v", v)
				}
			}
			integ["updated_at"] = ts
			e.auditIn(state, ctx, AuditUpdate, "integration", integID, auditFields(payload, nil))
			return integ, nil
		}, advisoryLock["update_integration"])
	if err != nil {
		return nil, err
	}
	e.invalidateTemplateCache(integID)
	return result.(map[string]any), nil
}

func (e *Engine) RotateIntegrationKey(ctx context.Context, integID string) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("integrations", integID), []string{"integrations"},
		func(state *store.State) (any, error) {
			integ := state.Integrations[integID]
			if integ == nil {
				return nil, errNotFound(fmt.Sprintf("integration %s not found", integID))
			}
			if err := e.authorizeItem(ctx, "integrations", integ); err != nil {
				return nil, err
			}
			// Not guarded by the provisioner on purpose. A leaked routing key is
			// an incident, the answer to it is "rotate now", and Terraform has
			// no action that rotates: the key is generated here and read back
			// into state, so the next refresh picks up the new one instead of
			// fighting it. Blocking this would mean a leaked key stays live
			// until somebody works out how to edit the pipeline.
			newKey := utils.MakeID("key")
			integ["key"] = newKey
			integ["routing_key"] = newKey
			integ["updated_at"] = ts
			// The new key is emphatically not recorded: it is the credential
			// every sender of this integration will use.
			e.auditIn(state, ctx, "integration.rotate_key", "integration", integID, nil)
			return integ, nil
		}, advisoryLock["rotate_integration_key"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}
