package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// SeedDemo creates demo data (users, team, schedule, chain, integration).
func (e *Engine) SeedDemo(ctx context.Context, force bool) (map[string]any, error) {
	seedCols := []string{"users", "teams", "schedules", "escalation_chains", "integrations"}
	if !force {
		hasAny, err := e.store.CollectionsHaveAny(ctx, seedCols)
		if err != nil {
			return nil, err
		}
		if hasAny {
			return nil, fmt.Errorf("database is not empty, pass --force to reseed")
		}
	}
	if force {
		allCols := []string{
			"users", "teams", "schedules", "escalation_chains", "integrations",
			"chatops_channels", "chatops_messages",
			"mobile_devices", "mobile_sessions", "alerts", "alert_groups",
			"notifications", "notification_batches", "notification_delivery_attempts",
			"kafka_outbox",
		}
		if err := e.store.ClearCollections(ctx, allCols); err != nil {
			return nil, err
		}
	}

	now := utils.UTCNow()

	alice, err := e.CreateUser(ctx, map[string]any{
		"name": "Alice SRE", "username": "alice", "email": "alice@example.com",
		"notification_targets": []any{map[string]any{"type": "log"}},
	})
	if err != nil {
		return nil, err
	}
	bob, err := e.CreateUser(ctx, map[string]any{
		"name": "Bob Platform", "username": "bob", "email": "bob@example.com",
		"notification_targets": []any{map[string]any{"type": "log"}},
	})
	if err != nil {
		return nil, err
	}
	team, err := e.CreateTeam(ctx, map[string]any{
		"name":       "Platform",
		"member_ids": []any{alice["id"], bob["id"]},
	})
	if err != nil {
		return nil, err
	}
	// A rotation rather than a pair of shifts: the demo schedule has to stay
	// covered indefinitely, otherwise the coverage check refuses to attach it
	// to the demo chain a week from now.
	sched, err := e.CreateSchedule(ctx, map[string]any{
		"name":    "Primary",
		"team_id": team["id"],
		"rotation": map[string]any{
			"start_at":         utils.ToISO(now.Add(-time.Hour)),
			"handoff_interval": 1,
			"handoff_unit":     "days",
			"participant_ids":  []any{alice["id"], bob["id"]},
		},
	})
	if err != nil {
		return nil, err
	}
	chain, err := e.CreateEscalationChain(ctx, map[string]any{
		"name": "Default chain",
		"steps": []any{
			map[string]any{"kind": StepNotifySchedule, "schedule_id": sched["id"]},
			map[string]any{"kind": StepWait, "delay_minutes": 0},
			map[string]any{"kind": StepNotifyTeam, "team_id": team["id"]},
		},
	})
	if err != nil {
		return nil, err
	}
	integration, err := e.CreateIntegration(ctx, map[string]any{
		"name":     "Default webhook",
		"group_by": []any{"alertname", "service"},
		"routes": []any{
			map[string]any{
				"name":                "critical-only",
				"match_type":          "labels",
				"labels":              map[string]any{"severity": "critical"},
				"escalation_chain_id": chain["id"],
			},
			map[string]any{
				"name":                "default",
				"match_type":          "all",
				"is_default":          true,
				"escalation_chain_id": chain["id"],
			},
		},
	})
	if err != nil {
		return nil, err
	}
	chatopsChannel, err := e.CreateChatopsChannel(ctx, map[string]any{
		"platform": "slack",
		"name":     "#platform-oncall",
		"team_id":  team["id"],
	})
	if err != nil {
		return nil, err
	}
	mobileDevice, err := e.RegisterMobileDevice(ctx, map[string]any{
		"user_id":     alice["id"],
		"platform":    "ios",
		"push_token":  "demo-push-token-alice", // #nosec G101 -- demo seed data, not a real credential
		"device_name": "Alice iPhone",
	})
	if err != nil {
		return nil, err
	}
	mobileSession, err := e.CreateMobileSession(ctx, map[string]any{
		"user_id":   alice["id"],
		"device_id": mobileDevice["id"],
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"users":            []map[string]any{alice, bob},
		"team":             team,
		"schedule":         sched,
		"escalation_chain": chain,
		"integration":      integration,
		"chatops_channel":  chatopsChannel,
		"mobile_device":    mobileDevice,
		"mobile_session":   mobileSession,
	}, nil
}
