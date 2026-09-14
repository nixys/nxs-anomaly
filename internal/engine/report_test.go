package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/nixys/nxs-anomaly/internal/store"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// fakeReportSource is a canned reportDataSource: unit tests exercise
// buildOnCallQualityReport and GenerateAndDeliverOnCallQualityReport against
// it, never a live ClickHouse — see report_clickhouse.go's header comment.
type fakeReportSource struct {
	calls int
}

func (f *fakeReportSource) AckAttainment(context.Context, string, time.Time, time.Time) (AckAttainment, error) {
	f.calls++
	return AckAttainment{Matured: 10, OnTime: 7, Late: 3, Pending: 2}, nil
}

func (f *fakeReportSource) LateEpisodes(context.Context, string, time.Time, time.Time, int) ([]EpisodeRef, error) {
	return []EpisodeRef{
		{AlertGroupID: "grp_1", EpisodeID: "ep_1", Service: "checkout", Severity: "critical", OpenedAt: time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC), Detail: 120},
	}, nil
}

func (f *fakeReportSource) ChannelStats(context.Context, string, time.Time, time.Time) ([]ChannelStat, error) {
	return []ChannelStat{
		{Channel: "telegram", Attempts: 20, Delivered: 16, Failed: 4, Skipped: 0, Retries: 3, TopErrorClass: "timeout"},
	}, nil
}

func (f *fakeReportSource) NightPageCounts(context.Context, string, time.Time, time.Time) ([24]int, error) {
	var hours [24]int
	hours[2] = 5
	hours[14] = 15
	return hours, nil
}

func (f *fakeReportSource) EscalationStats(context.Context, string, time.Time, time.Time) (EscalationStats, error) {
	return EscalationStats{Episodes: 40, Exhausted: 4, ReopenedNewAlert: 2, ReopenedUnresolved: 1}, nil
}

func (f *fakeReportSource) ExhaustedEpisodes(context.Context, string, time.Time, time.Time, int) ([]EpisodeRef, error) {
	return []EpisodeRef{
		{AlertGroupID: "grp_2", EpisodeID: "ep_2", Service: "billing", Severity: "high", OpenedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), Detail: 5},
	}, nil
}

func (f *fakeReportSource) NoiseSources(context.Context, string, time.Time, time.Time, int) ([]NoiseSource, error) {
	return []NoiseSource{
		{AlertName: "HighCPU", Service: "checkout", Alerts: 300, Episodes: 3},
	}, nil
}

func TestBuildOnCallQualityReport_ComputesSharesFromCounts(t *testing.T) {
	eng := New(storetest.New())
	eng.SetReportSource(&fakeReportSource{})

	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)
	report, err := eng.buildOnCallQualityReport(context.Background(), "team_1", from, to)
	if err != nil {
		t.Fatalf("build report: %v", err)
	}

	if got, want := report.Ack.LateShare(), 0.3; got != want {
		t.Errorf("ack late share = %v, want %v", got, want)
	}
	if got, want := report.Channels[0].FailureShare(), 0.2; got != want {
		t.Errorf("channel failure share = %v, want %v", got, want)
	}
	if got, want := report.Escalations.ExhaustedShare(), 0.1; got != want {
		t.Errorf("exhausted share = %v, want %v", got, want)
	}
	if got, want := report.NightPagesTotal(), 20; got != want {
		t.Errorf("night pages total = %d, want %d", got, want)
	}
	// Only hour 2 falls in [0,6): 5 of 20 pages, i.e. 0.25.
	if got, want := report.NightPagesShare(), 0.25; got != want {
		t.Errorf("night pages share = %v, want %v", got, want)
	}
	if got, want := report.Noise[0].AlertsPerEpisode(), 100.0; got != want {
		t.Errorf("alerts per episode = %v, want %v", got, want)
	}
}

func TestBuildOnCallQualityReport_NoSourceIsNotAvailable(t *testing.T) {
	eng := New(storetest.New())
	_, err := eng.buildOnCallQualityReport(context.Background(),
		"", time.Now(), time.Now())
	if !errors.Is(err, ErrReportsNotAvailable) {
		t.Fatalf("got %v, want ErrReportsNotAvailable", err)
	}
}

func TestReportPlainText_NamesOffendersForDrillDown(t *testing.T) {
	eng := New(storetest.New())
	eng.SetReportSource(&fakeReportSource{})
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	report, err := eng.buildOnCallQualityReport(context.Background(), "team_1", from, from.AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("build report: %v", err)
	}
	text := report.PlainText()
	for _, want := range []string{"grp_1", "grp_2", "telegram", "HighCPU", "checkout"} {
		if !strings.Contains(text, want) {
			t.Errorf("plain text digest missing %q:\n%s", want, text)
		}
	}
}

// TestGenerateAndDeliverOnCallQualityReport_IsIdempotentByPeriod is the
// property GenerateAndDeliverOnCallQualityReport promises: a second call for
// the same team and period must not requery ClickHouse or enqueue a second
// round of notifications.
func TestGenerateAndDeliverOnCallQualityReport_IsIdempotentByPeriod(t *testing.T) {
	st := storetest.New()
	eng := New(st)
	src := &fakeReportSource{}
	eng.SetReportSource(src)
	eng.reportRecipients = []string{"oncall-lead@example.com"}

	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	first, err := eng.GenerateAndDeliverOnCallQualityReport(context.Background(), "team_1", now)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first["id"] == "" {
		t.Fatal("report has no id")
	}
	if got := st.Count("notifications"); got != 1 {
		t.Fatalf("notifications after first call = %d, want 1", got)
	}
	if got := st.Count("reports"); got != 1 {
		t.Fatalf("reports after first call = %d, want 1", got)
	}

	second, err := eng.GenerateAndDeliverOnCallQualityReport(context.Background(), "team_1", now)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second["id"] != first["id"] {
		t.Fatalf("second call produced a different report: %v vs %v", second["id"], first["id"])
	}
	if got := st.Count("notifications"); got != 1 {
		t.Fatalf("notifications after second call = %d, want still 1 (no duplicate send)", got)
	}
	if got := st.Count("reports"); got != 1 {
		t.Fatalf("reports after second call = %d, want still 1", got)
	}
	// The second call's cheap pre-check must short-circuit before querying
	// ClickHouse again.
	if src.calls != 1 {
		t.Fatalf("reportDataSource queried %d times, want 1 (second call should hit the stored report)", src.calls)
	}
}

func TestGenerateAndDeliverOnCallQualityReport_NoSourceIsNotAvailable(t *testing.T) {
	eng := New(storetest.New())
	_, err := eng.GenerateAndDeliverOnCallQualityReport(context.Background(), "", time.Now())
	if !errors.Is(err, ErrReportsNotAvailable) {
		t.Fatalf("got %v, want ErrReportsNotAvailable", err)
	}
}

func TestReportPeriodIsStableAcrossRetries(t *testing.T) {
	st := storetest.New()
	eng := New(st)
	eng.SetReportSource(&fakeReportSource{})
	eng.reportRecipients = []string{"lead@example.com", "lead@example.com"}
	now := time.Date(2026, 9, 15, 6, 0, 0, 0, time.UTC)
	first, err := eng.GenerateAndDeliverOnCallQualityReport(context.Background(), "team-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if first["period_start"] != "2026-09-08T00:00:00+00:00" || first["period_end"] != "2026-09-15T00:00:00+00:00" {
		t.Fatalf("unexpected period: %v", first)
	}
	second, err := eng.GenerateAndDeliverOnCallQualityReport(context.Background(), "team-a", now.Add(3*time.Hour))
	if err != nil || first["id"] != second["id"] || st.Count("notifications") != 1 {
		t.Fatalf("retry must reuse report and delivery: %v %v", second, err)
	}
}

func TestReportsDoNotExposeOtherTeamsThroughGlobalDigest(t *testing.T) {
	st := storetest.New()
	for _, team := range []string{"team-a", "team-b", ""} {
		st.Seed("reports", map[string]any{"id": "report-" + team, "team_id": team})
	}
	eng := New(st)
	for _, teams := range [][]string{{"team-a"}, nil} {
		ctx := scopedHistoryCtx(teams...)
		page, err := eng.ListCollectionPage(ctx, "reports", nil)
		if err != nil {
			t.Fatal(err)
		}
		items := page["items"].([]map[string]any)
		if len(items) != len(teams) {
			t.Fatalf("visible reports: %v", items)
		}
		for _, id := range []string{"report-", "report-team-b"} {
			if _, err := eng.GetItem(ctx, "reports", id); !isForbidden(err) {
				t.Fatalf("%s must be forbidden: %v", id, err)
			}
		}
	}
	st.Seed("notifications", map[string]any{"id": "digest", "reason": "oncall_quality_report", "payload": map[string]any{"text": "all teams"}})
	if _, err := eng.GetItem(scopedHistoryCtx("team-a"), "notifications", "digest"); !isForbidden(err) {
		t.Fatalf("digest notification leaks: %v", err)
	}
	if _, err := eng.GetDeliveryAttempts(scopedHistoryCtx("team-a"), "digest"); !isForbidden(err) {
		t.Fatalf("digest attempts leak: %v", err)
	}
	if _, err := eng.GetItem(context.Background(), "reports", "report-"); err != nil {
		t.Fatal(err)
	}
}

// A real database is necessary here: a mutex-backed fake cannot prove the
// advisory lock and the report/notification transaction work together.
func TestReportConcurrentGenerationPostgreSQL(t *testing.T) {
	dsn := os.Getenv("NXS_ANOMALY_TEST_REPORT_DB_DSN")
	if dsn == "" {
		t.Skip("set NXS_ANOMALY_TEST_REPORT_DB_DSN to a disposable PostgreSQL database")
	}
	t.Setenv("NXS_ANOMALY_DB_DSN", dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	team := fmt.Sprintf("report-review-%d", time.Now().UnixNano())
	target := team + "@example.invalid"
	now := time.Now().UTC()
	id := reportID(team, now.Truncate(24*time.Hour).AddDate(0, 0, -7))
	defer func() { _, _ = st.DeleteItem(context.Background(), "reports", id) }()
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		eng := New(st)
		eng.SetReportSource(&fakeReportSource{})
		eng.reportRecipients = []string{target, target}
		go func() { <-start; _, err := eng.GenerateAndDeliverOnCallQualityReport(ctx, team, now); results <- err }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	notifications, err := st.ListCollection(ctx, "notifications")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, n := range notifications {
		if n["target"] == target {
			count++
			defer func(id string) { _, _ = st.DeleteItem(context.Background(), "notifications", id) }(n["id"].(string))
			if n["status"] != "delivery_scheduled" {
				t.Fatalf("not enqueued: %v", n)
			}
		}
	}
	if count != 1 {
		t.Fatalf("concurrent runs queued %d notifications, want 1", count)
	}
	report, err := st.GetItem(ctx, "reports", id)
	if err != nil || report == nil {
		t.Fatalf("report missing: %v", err)
	}
}
