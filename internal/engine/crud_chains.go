package engine

import (
	"context"
	"fmt"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// ── Escalation Chains ─────────────────────────────────────────────────────────

func (e *Engine) CreateEscalationChain(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := utils.EnsureRequired(payload, []string{"name"}); err != nil {
		return nil, errValidation(err.Error())
	}
	ts := utils.ToISO(utils.UTCNow())
	rawSteps, _ := payload["steps"].([]any)
	// Steps can be empty for a draft chain.
	var steps []any
	for i, raw := range rawSteps {
		step, err := e.sanitizeStep(ctx, raw, i)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}

	result, err := e.store.UpdateCollections(ctx, nil, []string{"escalation_chains"},
		func(state *store.State) (any, error) {
			chain := map[string]any{
				"id":         utils.MakeID("esc"),
				"name":       fmt.Sprintf("%v", payload["name"]),
				"steps":      steps,
				"created_at": ts,
				"updated_at": ts,
			}
			stampProvisioner(ctx, chain)
			state.EscalationChains[chain["id"].(string)] = chain
			e.auditIn(state, ctx, AuditCreate, "escalation_chain", chain["id"].(string), auditFields(payload, nil))
			return chain, nil
		}, advisoryLock["create_escalation_chain"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (e *Engine) UpdateEscalationChain(ctx context.Context, chainID string, payload map[string]any) (map[string]any, error) {
	ts := utils.ToISO(utils.UTCNow())
	var steps []any
	stepsSet := false
	if v, ok := payload["steps"]; ok {
		rawSteps, _ := v.([]any)
		stepsSet = true
		for i, raw := range rawSteps {
			step, err := e.sanitizeStep(ctx, raw, i)
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
		}
	}

	result, err := e.store.UpdateCollectionsFiltered(ctx,
		loadItems("escalation_chains", chainID),
		[]string{"escalation_chains"},
		func(state *store.State) (any, error) {
			chain := state.EscalationChains[chainID]
			if chain == nil {
				return nil, errNotFound(fmt.Sprintf("escalation_chain %s not found", chainID))
			}
			if err := guardProvisioned(ctx, "escalation_chains", chain); err != nil {
				return nil, err
			}
			if v, ok := payload["name"]; ok {
				chain["name"] = fmt.Sprintf("%v", v)
			}
			if stepsSet {
				chain["steps"] = steps
			}
			chain["updated_at"] = ts
			e.auditIn(state, ctx, AuditUpdate, "escalation_chain", chainID, auditFields(payload, nil))
			return chain, nil
		}, advisoryLock["update_escalation_chain"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}
