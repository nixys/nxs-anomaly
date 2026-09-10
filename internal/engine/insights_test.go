package engine

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

func insightsEngine(t *testing.T) (*Engine, *storetest.Store) {
	t.Helper()
	st := storetest.New()
	return New(st), st
}

// The screen used to ask twelve questions to draw six numbers, and could answer
// nothing about direction. One call now returns both.
func TestInsightsSummaryCountsByLevelNotBySpelling(t *testing.T) {
	eng, st := insightsEngine(t)
	ctx := context.Background()
	now := time.Now().UTC()
	day := now.Format("2006-01-02T15:04:05Z")

	for i, severity := range []string{"critical", "high", "error", "medium", "wobbly"} {
		st.Seed("alert_groups", map[string]any{
			"id":             string(rune('a'+i)) + "_grp",
			"integration_id": "int_1",
			"status":         "open",
			"severity":       severity,
			"created_at":     day,
		})
	}

	out, err := eng.GetInsightsSummary(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	levels, _ := out["groups_by_level"].(map[string]int)
	if levels["error"] != 2 {
		t.Errorf("groups_by_level[error] = %d, want 2 — high and error are one level", levels["error"])
	}
	if levels["critical"] != 1 || levels["warning"] != 1 {
		t.Errorf("levels = %#v", levels)
	}
	// A spelling the service does not model is counted as itself, not folded
	// into a level it may not belong to.
	if levels["unknown"] != 1 {
		t.Errorf("levels[unknown] = %d, want the unmodelled word counted apart", levels["unknown"])
	}
	statuses, _ := out["groups_by_status"].(map[string]int)
	if statuses["open"] != 5 {
		t.Errorf("groups_by_status[open] = %d, want 5", statuses["open"])
	}
}

// A quiet day must appear in the trend as a zero, not be missing: a line that
// skips empty days draws a straight line through a quiet week.
func TestInsightsTrendFillsEveryDayInRange(t *testing.T) {
	eng, _ := insightsEngine(t)
	to := time.Now().UTC()
	from := to.Add(-3 * 24 * time.Hour)

	out, err := eng.GetInsightsSummary(context.Background(), map[string]any{
		"from": from.Format(time.RFC3339),
		"to":   to.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	trend, _ := out["trend"].([]map[string]any)
	if len(trend) != 4 {
		t.Fatalf("trend has %d buckets, want one per day including both ends", len(trend))
	}
	for _, bucket := range trend {
		if bucket["day"] == "" {
			t.Errorf("bucket without a day: %#v", bucket)
		}
	}
}

func TestInsightsSummaryRejectsAnInvertedRange(t *testing.T) {
	eng, _ := insightsEngine(t)
	now := time.Now().UTC()
	_, err := eng.GetInsightsSummary(context.Background(), map[string]any{
		"from": now.Format(time.RFC3339),
		"to":   now.Add(-time.Hour).Format(time.RFC3339),
	})
	if err == nil {
		t.Fatal("a range that ends before it starts must be refused, not answered with an empty chart")
	}
}

// The cap is what keeps a dashboard from becoming a year-long report scan.
func TestInsightsSummaryClampsAnAbsurdRange(t *testing.T) {
	eng, _ := insightsEngine(t)
	to := time.Now().UTC()
	out, err := eng.GetInsightsSummary(context.Background(), map[string]any{
		"from": to.Add(-5 * 365 * 24 * time.Hour).Format(time.RFC3339),
		"to":   to.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	trend, _ := out["trend"].([]map[string]any)
	if len(trend) > 94 {
		t.Errorf("trend has %d buckets, want the range clamped to the cap", len(trend))
	}
}

var _ store.PostgreSQLStore = (*storetest.Store)(nil)
