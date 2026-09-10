package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// insightsMaxRange caps how far back a trend may be asked for. The query scans
// two tables over the range; a year of it on a busy installation is a report,
// not a dashboard, and the screen offers 24 hours to 30 days.
const insightsMaxRange = 92 * 24 * time.Hour

// GetInsightsSummary answers the insights screen in one call: the current
// counts it shows and the daily trend behind them.
//
// It exists because that screen used to ask twelve separate questions to draw
// six numbers, and could answer none of the questions a reader actually has
// about direction — whether today is worse than last Tuesday.
func (e *Engine) GetInsightsSummary(ctx context.Context, params map[string]any) (map[string]any, error) {
	to := time.Now().UTC()
	if v := utils.StrVal(params, "to"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("insights: invalid to: %w", err)
		}
		to = parsed.UTC()
	}
	from := to.Add(-7 * 24 * time.Hour)
	if v := utils.StrVal(params, "from"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("insights: invalid from: %w", err)
		}
		from = parsed.UTC()
	}
	if !from.Before(to) {
		return nil, fmt.Errorf("insights: from must be before to")
	}
	if to.Sub(from) > insightsMaxRange {
		from = to.Add(-insightsMaxRange)
	}

	// Team scoping narrows the same way the history view does: a summary that
	// counted integrations the caller cannot open would leak how much is
	// happening behind a wall they are not allowed through.
	var scoped []string
	if actor := authz.FromContext(ctx); actor.TeamScoped {
		visible, err := e.visibleIntegrationIDs(ctx, actor)
		if err != nil {
			return nil, err
		}
		// visibleIntegrationIDs answers in the shape the generic filters take;
		// this query wants plain strings, and an empty (not nil) slice is what
		// says "reachable: none".
		scoped = []string{}
		for _, id := range visible {
			if s, ok := id.(string); ok {
				scoped = append(scoped, s)
			}
		}
	}

	summary, err := e.store.InsightsSummaryQuery(ctx, utils.StrVal(params, "integration_id"), scoped, from, to)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"from":                   utils.ToISO(from),
		"to":                     utils.ToISO(to),
		"groups_by_status":       summary.GroupsByStatus,
		"groups_by_level":        summary.GroupsByLevel,
		"notifications_by_state": summary.NotificationsByState,
		"trend":                  trendItems(summary.Trend),
	}, nil
}

func trendItems(buckets []store.InsightsBucket) []map[string]any {
	items := make([]map[string]any, len(buckets))
	for i, b := range buckets {
		items[i] = map[string]any{
			"day":       b.Day,
			"opened":    b.Opened,
			"resolved":  b.Resolved,
			"delivered": b.Delivered,
			"failed":    b.Failed,
		}
	}
	return items
}
