package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// heartbeat.go watches the monitoring rather than the monitored: it notices when
// a source that should be talking has gone quiet.
//
// Every other signal in this service starts with an alert arriving. That leaves
// the one failure nobody is told about — the exporter that died, the cron that
// stopped, the network segment that took the Alertmanager with it. Silence looks
// exactly like health, and the longer it lasts the more reassuring it gets.
//
// The check is opt-in per integration, because "this source is expected to say
// something every N seconds" is a fact about the source that only its owner
// knows. An integration with no interval configured is never reported: plenty of
// them are legitimately quiet for weeks.

// heartbeatMinInterval floors the configured interval. Below a minute the check
// would fire on ordinary scheduling jitter — the worker cycle itself is five
// seconds, and a source is not "down" because two of its ticks landed on the
// wrong side of one.
const heartbeatMinInterval = time.Minute

// heartbeatAlertName is the label every silence alert carries. It is stable so
// that a route can match it, and distinct so it cannot collide with a real alert
// from the source it is about.
const heartbeatAlertName = "SourceSilent"

// sanitizeHeartbeat normalises the per-integration heartbeat settings.
//
// interval_seconds is the whole contract: how long the source may stay silent
// before that is news. grace_seconds buys the source a late tick without raising
// anything, and defaults to a third of the interval — enough to absorb jitter,
// short enough that the report still means something.
func sanitizeHeartbeat(raw any) map[string]any {
	m, _ := raw.(map[string]any)
	if m == nil {
		return map[string]any{"interval_seconds": 0, "grace_seconds": 0}
	}
	interval := maxInt(utils.IntVal(m, "interval_seconds"), 0)
	if interval > 0 && time.Duration(interval)*time.Second < heartbeatMinInterval {
		interval = int(heartbeatMinInterval / time.Second)
	}
	grace := maxInt(utils.IntVal(m, "grace_seconds"), 0)
	if interval > 0 && grace == 0 {
		grace = interval / 3
	}
	return map[string]any{"interval_seconds": interval, "grace_seconds": grace}
}

// heartbeatSettings reads the sanitised block back off an integration.
func heartbeatSettings(integration map[string]any) (interval, grace time.Duration, enabled bool) {
	m, _ := integration["heartbeat"].(map[string]any)
	if m == nil {
		return 0, 0, false
	}
	secs := utils.IntVal(m, "interval_seconds")
	if secs <= 0 {
		return 0, 0, false
	}
	return time.Duration(secs) * time.Second,
		time.Duration(utils.IntVal(m, "grace_seconds")) * time.Second, true
}

// ProcessSourceHeartbeats raises an alert for every source that has gone quiet
// past its declared interval, and resolves it when the source comes back.
//
// The alert is raised through IngestAlert rather than written directly, so it is
// routed, grouped, escalated and delivered by exactly the machinery a real alert
// uses. A dead-man switch that took its own private path to the responder would
// be the one alert nobody had ever tested end to end.
//
// Returns the number of sources currently reported silent.
func (e *Engine) ProcessSourceHeartbeats(ctx context.Context) (int, error) {
	integrations, err := e.refCollection(ctx, "integrations")
	if err != nil {
		return 0, err
	}
	windows, err := e.refCollection(ctx, "maintenance_windows")
	if err != nil {
		return 0, err
	}
	now := utils.UTCNow()
	silent := 0
	for id, integration := range integrations {
		interval, grace, enabled := heartbeatSettings(integration)
		if !enabled || utils.StrVal(integration, "deleted_at") != "" {
			continue
		}
		// A source taken down for planned work is a silent source. Without this
		// check every maintenance raises SourceSilent at the moment the people
		// who would answer it are already busy causing it — which is the case
		// that motivated maintenance windows in the first place. Not counted as
		// silent either: the metric drives an alert of its own.
		if activeMaintenance(windows, id, now) != nil {
			continue
		}
		lastAt, seen, err := e.store.LastAlertReceivedAt(ctx, id)
		if err != nil {
			// One unreadable integration must not stop the rest being checked:
			// the point of this step is noticing silence, and skipping the whole
			// pass would be a quieter version of the same failure.
			slog.Warn("heartbeat_lookup_failed", "integration_id", id, "error", err)
			continue
		}
		if !seen {
			// Never delivered anything. That may be a source wired up minutes ago
			// and not yet firing, so there is nothing to compare against and
			// nothing honest to say.
			continue
		}
		overdue := now.Sub(lastAt) > interval+grace
		if overdue {
			silent++
		}
		if err := e.reconcileHeartbeat(ctx, id, integration, overdue, lastAt, now); err != nil {
			slog.Warn("heartbeat_reconcile_failed", "integration_id", id, "error", err)
		}
	}
	e.sink().SetSourcesSilent(silent)
	return silent, nil
}

// reconcileHeartbeat raises or clears the silence alert, but only on a change of
// state.
//
// The transition is recorded on the integration so that a source silent for a
// week produces one alert rather than one per worker cycle. Re-ingesting every
// five seconds would keep the group open and its alert_count climbing, which
// reads as a storm from the source that is, by definition, sending nothing.
func (e *Engine) reconcileHeartbeat(ctx context.Context, id string, integration map[string]any, overdue bool, lastAt, now time.Time) error {
	wasSilent := utils.StrVal(integration, "heartbeat_silent_since") != ""
	if overdue == wasSilent {
		return nil
	}
	key := utils.StrVal(integration, "key")
	if key == "" {
		return fmt.Errorf("integration %s has no key", id)
	}
	name := strDefault(utils.StrVal(integration, "name"), id)

	payload := map[string]any{
		"title":    fmt.Sprintf("Source silent: %s", name),
		"severity": "critical",
		"labels": map[string]any{
			"alertname":      heartbeatAlertName,
			"integration":    name,
			"integration_id": id,
		},
		"annotations": map[string]any{
			"last_alert_at": utils.ToISO(lastAt),
			"silent_for":    now.Sub(lastAt).Round(time.Second).String(),
		},
	}
	if overdue {
		payload["status"] = "firing"
		payload["message"] = fmt.Sprintf(
			"No alert received from %s for %s; it was expected to report at least every %s.",
			name, now.Sub(lastAt).Round(time.Second), heartbeatIntervalOf(integration))
	} else {
		payload["status"] = "resolved"
		payload["message"] = fmt.Sprintf("%s is reporting again.", name)
	}
	if _, err := e.IngestAlert(ctx, key, payload); err != nil {
		return err
	}
	return e.markHeartbeatState(ctx, id, overdue, now)
}

// heartbeatIntervalOf renders the configured interval for the alert text.
func heartbeatIntervalOf(integration map[string]any) time.Duration {
	interval, grace, _ := heartbeatSettings(integration)
	return interval + grace
}

// markHeartbeatState records the transition on the integration row.
func (e *Engine) markHeartbeatState(ctx context.Context, id string, silent bool, now time.Time) error {
	_, err := e.store.UpdateCollectionsFiltered(ctx,
		loadItems("integrations", id), []string{"integrations"},
		func(state *store.State) (any, error) {
			integration := state.Integrations[id]
			if integration == nil {
				return nil, nil
			}
			if silent {
				integration["heartbeat_silent_since"] = utils.ToISO(now)
			} else {
				delete(integration, "heartbeat_silent_since")
			}
			integration["updated_at"] = utils.ToISO(now)
			return nil, nil
		}, advisoryLock["heartbeat_state"])
	return err
}
