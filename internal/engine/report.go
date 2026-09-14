package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// report.go builds the on-call quality digest (Enterprise): a packaged,
// scheduled result over the same ClickHouse views the analytics dashboards use
// (docs/enterprise/ru/INCIDENT_ANALYTICS_DASHBOARDS.md), rather than a new
// measurement of its own. It answers five questions a manager asks without
// opening Grafana: missed ACKs, channel/delivery problems, night load, repeat
// escalations, and noisy alert sources — each with the alert_group_id/
// episode_id a reader needs to look the case up (GET
// /api/v1/alert-groups/{id}), so the digest names its evidence rather than
// asking to be trusted.
//
// ErrReportsNotAvailable is returned wherever no reportDataSource is
// configured. That is the permanent state of a community build, and the
// startup state of an enterprise install before NXS_ANOMALY_CLICKHOUSE_HOST is
// set — the same "gap in configuration, not a bug" treatment
// AnalyticsConfigured gives an unconfigured Kafka producer.
var ErrReportsNotAvailable = errors.New("on-call quality reports are not available: ClickHouse is not configured for this installation")

// reportFormatV1 names the report's shape so a future v2 layout does not have
// to guess what an old stored row means.
const reportFormatV1 = "oncall_quality_v1"

// reportTopN bounds every "worst offenders" list in the digest. A weekly
// email naming the single worst case is useful; one naming all 4,000 late
// episodes is not, and it is also how a digest turns into an accidental data
// export.
const reportTopN = 10

// nightWindowStartUTC/nightWindowEndUTC bound the "night" bucket used for the
// night-load share.
//
// UTC, not the recipient's timezone: the delivery-attempt event carries no
// per-recipient timezone today (see the dashboards doc, section 3 —
// `nxs_delivery_attempts` has no such column), and a report that pretended to
// know one would be wrong in whichever direction the reader trusted it. This
// is stated in the rendered digest, not hidden in the number.
const (
	nightWindowStartUTC = 0
	nightWindowEndUTC   = 6
)

// reportDataSource is the read side of the digest: independent queries
// against the existing ClickHouse views, kept behind an interface so
// report_test.go can fake it — no live ClickHouse is needed for `go test`.
// The ClickHouse-backed implementation lives in report_clickhouse.go
// (enterprise builds only).
type reportDataSource interface {
	AckAttainment(ctx context.Context, teamID string, from, to time.Time) (AckAttainment, error)
	LateEpisodes(ctx context.Context, teamID string, from, to time.Time, limit int) ([]EpisodeRef, error)
	ChannelStats(ctx context.Context, teamID string, from, to time.Time) ([]ChannelStat, error)
	NightPageCounts(ctx context.Context, teamID string, from, to time.Time) ([24]int, error)
	EscalationStats(ctx context.Context, teamID string, from, to time.Time) (EscalationStats, error)
	ExhaustedEpisodes(ctx context.Context, teamID string, from, to time.Time, limit int) ([]EpisodeRef, error)
	NoiseSources(ctx context.Context, teamID string, from, to time.Time, limit int) ([]NoiseSource, error)
}

// AckAttainment mirrors nxs_response_attainment's ack_* counters for one
// team/period: matured is the eligible cohort whose deadline has passed
// (see the dashboards doc's "matured cohort" formula) — pending episodes are
// neither a success nor a violation and are counted apart, not folded into
// either side.
type AckAttainment struct {
	Matured int
	OnTime  int
	Late    int
	Pending int
}

// LateShare is late/matured, 0 with no matured cohort (nothing to report, not
// a perfect score).
func (a AckAttainment) LateShare() float64 {
	if a.Matured == 0 {
		return 0
	}
	return float64(a.Late) / float64(a.Matured)
}

// EpisodeRef names one case for drill-down. Detail is metric-specific: seconds
// late for a late-ACK entry, the escalation step reached for an exhausted
// entry — named generically because both lists share this shape and nothing
// else reads Detail's unit.
type EpisodeRef struct {
	AlertGroupID string
	EpisodeID    string
	Service      string
	Severity     string
	OpenedAt     time.Time
	Detail       int64
}

// ChannelStat is nxs_notification_fact grouped by channel for the period.
type ChannelStat struct {
	AlertGroupID  string
	Channel       string
	Attempts      int
	Delivered     int
	Failed        int
	Skipped       int
	Retries       int
	TopErrorClass string
}

// FailureShare is failed/attempts, 0 with no attempts.
func (c ChannelStat) FailureShare() float64 {
	if c.Attempts == 0 {
		return 0
	}
	return float64(c.Failed) / float64(c.Attempts)
}

// EscalationStats mirrors nxs_group_episodes' escalation/reopen counters.
type EscalationStats struct {
	Episodes           int
	Exhausted          int
	ReopenedNewAlert   int
	ReopenedUnresolved int
}

// ExhaustedShare is exhausted/episodes, 0 with no episodes.
func (s EscalationStats) ExhaustedShare() float64 {
	if s.Episodes == 0 {
		return 0
	}
	return float64(s.Exhausted) / float64(s.Episodes)
}

// NoiseSource is the per-event nxs_report_alerts projection grouped by
// alertname/service: the volume number a tuning conversation needs — a rule
// firing a thousand times into one group is loud upstream and quiet in the
// episode count, which is the distinction AlertsPerEpisode makes visible.
type NoiseSource struct {
	AlertGroupID string
	AlertName    string
	Service      string
	Alerts       int
	Episodes     int
}

// AlertsPerEpisode is alerts/episodes, 0 with no episodes.
func (n NoiseSource) AlertsPerEpisode() float64 {
	if n.Episodes == 0 {
		return 0
	}
	return float64(n.Alerts) / float64(n.Episodes)
}

// Report is one generated on-call quality digest.
type Report struct {
	ID          string
	TeamID      string
	PeriodStart time.Time
	PeriodEnd   time.Time
	GeneratedAt time.Time
	Format      string

	Ack               AckAttainment
	LateEpisodes      []EpisodeRef
	Channels          []ChannelStat
	NightPagesByHour  [24]int
	Escalations       EscalationStats
	ExhaustedEpisodes []EpisodeRef
	Noise             []NoiseSource
}

// NightPagesTotal sums NightPagesByHour.
func (r *Report) NightPagesTotal() int {
	total := 0
	for _, n := range r.NightPagesByHour {
		total += n
	}
	return total
}

// NightPagesShare is the share of paging notifications that fell in the UTC
// night window [nightWindowStartUTC, nightWindowEndUTC). 0 with no pages.
func (r *Report) NightPagesShare() float64 {
	total := r.NightPagesTotal()
	if total == 0 {
		return 0
	}
	night := 0
	for h := nightWindowStartUTC; h < nightWindowEndUTC; h++ {
		night += r.NightPagesByHour[h]
	}
	return float64(night) / float64(total)
}

// reportID is derived from (teamID, periodStart) so a second call for a
// period already reported is a lookup, not a duplicate — see
// GenerateAndDeliverOnCallQualityReport.
func reportID(teamID string, periodStart time.Time) string {
	key := teamID
	if key == "" {
		key = "_all"
	}
	return fmt.Sprintf("rpt_%s_%s", key, periodStart.UTC().Format("20060102"))
}

// toMap is the report's stored/API shape: the flat columns
// store.TypedColumns["reports"] promotes, plus the digest itself nested under
// each section's name. Timestamps are ISO strings, the format every other
// jsonb-backed row in this codebase uses (see utils.ToISO), so a report reads
// the same as any other collection through GET /api/v1/reports/{id}.
func (r *Report) toMap() map[string]any {
	episodeRefs := func(refs []EpisodeRef) []map[string]any {
		out := make([]map[string]any, 0, len(refs))
		for _, e := range refs {
			out = append(out, map[string]any{
				"alert_group_id": e.AlertGroupID,
				"episode_id":     e.EpisodeID,
				"service":        e.Service,
				"severity":       e.Severity,
				"opened_at":      utils.ToISO(e.OpenedAt),
				"detail":         e.Detail,
			})
		}
		return out
	}
	channels := make([]map[string]any, 0, len(r.Channels))
	for _, c := range r.Channels {
		channels = append(channels, map[string]any{
			"channel":         c.Channel,
			"alert_group_id":  c.AlertGroupID,
			"attempts":        c.Attempts,
			"delivered":       c.Delivered,
			"failed":          c.Failed,
			"skipped":         c.Skipped,
			"retries":         c.Retries,
			"top_error_class": c.TopErrorClass,
			"failure_share":   c.FailureShare(),
		})
	}
	noise := make([]map[string]any, 0, len(r.Noise))
	for _, n := range r.Noise {
		noise = append(noise, map[string]any{
			"alertname":          n.AlertName,
			"alert_group_id":     n.AlertGroupID,
			"service":            n.Service,
			"alerts":             n.Alerts,
			"episodes":           n.Episodes,
			"alerts_per_episode": n.AlertsPerEpisode(),
		})
	}
	return map[string]any{
		"id":           r.ID,
		"team_id":      nilIfEmpty(r.TeamID),
		"period_start": utils.ToISO(r.PeriodStart),
		"period_end":   utils.ToISO(r.PeriodEnd),
		"generated_at": utils.ToISO(r.GeneratedAt),
		"format":       r.Format,
		"ack": map[string]any{
			"matured": r.Ack.Matured, "on_time": r.Ack.OnTime,
			"late": r.Ack.Late, "pending": r.Ack.Pending,
			"late_share": r.Ack.LateShare(),
		},
		"late_episodes": episodeRefs(r.LateEpisodes),
		"channels":      channels,
		"night_load": map[string]any{
			"pages_by_hour_utc": r.NightPagesByHour[:],
			"total":             r.NightPagesTotal(),
			"night_share":       r.NightPagesShare(),
			"night_window_utc":  fmt.Sprintf("%02d:00-%02d:00", nightWindowStartUTC, nightWindowEndUTC),
		},
		"escalations": map[string]any{
			"episodes": r.Escalations.Episodes, "exhausted": r.Escalations.Exhausted,
			"reopened_new_alert":  r.Escalations.ReopenedNewAlert,
			"reopened_unresolved": r.Escalations.ReopenedUnresolved,
			"exhausted_share":     r.Escalations.ExhaustedShare(),
		},
		"exhausted_episodes": episodeRefs(r.ExhaustedEpisodes),
		"noise":              noise,
		// summary_text is PlainText()'s output, stored so the email, the
		// ChatOps `report` command and a GET /api/v1/reports/{id} caller all
		// show the identical rendering rather than three renderers drifting
		// apart from the same struct.
		"summary_text": r.PlainText(),
	}
}

// PlainText renders the digest for the email body and the ChatOps `report`
// summary — the one renderer both use, so the two never drift apart. It names
// each offender's alert_group_id: the drill-down this report promises is
// "look this id up" (GET /api/v1/alert-groups/{id}, or the group page in the
// app), not a raw-event viewer of its own.
func (r *Report) PlainText() string {
	var b strings.Builder
	team := r.TeamID
	if team == "" {
		team = "(installation-wide)"
	}
	fmt.Fprintf(&b, "On-call quality report — %s\n", team)
	fmt.Fprintf(&b, "Period: %s to %s (UTC)\n\n",
		r.PeriodStart.UTC().Format("2006-01-02"), r.PeriodEnd.UTC().Format("2006-01-02"))

	fmt.Fprintf(&b, "Missed ACKs\n")
	fmt.Fprintf(&b, "  matured=%d on_time=%d late=%d pending=%d (%.0f%% of matured were late)\n",
		r.Ack.Matured, r.Ack.OnTime, r.Ack.Late, r.Ack.Pending, r.Ack.LateShare()*100)
	for _, ep := range r.LateEpisodes {
		fmt.Fprintf(&b, "  - %ds late: alert group %s (%s/%s, opened %s)\n",
			ep.Detail, ep.AlertGroupID, ep.Service, ep.Severity, ep.OpenedAt.UTC().Format(time.RFC3339))
	}

	fmt.Fprintf(&b, "\nChannel problems\n")
	for _, c := range r.Channels {
		fmt.Fprintf(&b, "  - %s: %d attempts, %d failed (%.0f%%), %d skipped, %d retries",
			c.Channel, c.Attempts, c.Failed, c.FailureShare()*100, c.Skipped, c.Retries)
		if c.TopErrorClass != "" {
			fmt.Fprintf(&b, ", top error: %s", c.TopErrorClass)
		}
		if c.AlertGroupID != "" {
			fmt.Fprintf(&b, ", alert group %s", c.AlertGroupID)
		}
		b.WriteString("\n")
	}
	if len(r.Channels) == 0 {
		b.WriteString("  (no delivery attempts in this period)\n")
	}

	fmt.Fprintf(&b, "\nNight load (%02d:00-%02d:00 UTC)\n", nightWindowStartUTC, nightWindowEndUTC)
	fmt.Fprintf(&b, "  %d of %d delivered paging notifications (%.0f%%) fell in the night window\n",
		func() int {
			n := 0
			for h := nightWindowStartUTC; h < nightWindowEndUTC; h++ {
				n += r.NightPagesByHour[h]
			}
			return n
		}(), r.NightPagesTotal(), r.NightPagesShare()*100)
	b.WriteString("  (UTC hour, not the recipient's timezone — see the report doc)\n")

	fmt.Fprintf(&b, "\nEscalations and reopened episodes\n")
	fmt.Fprintf(&b, "  episodes=%d exhausted=%d (%.0f%%) reopened_new_alert=%d reopened_unresolved=%d\n",
		r.Escalations.Episodes, r.Escalations.Exhausted, r.Escalations.ExhaustedShare()*100,
		r.Escalations.ReopenedNewAlert, r.Escalations.ReopenedUnresolved)
	for _, ep := range r.ExhaustedEpisodes {
		fmt.Fprintf(&b, "  - exhausted at step %d: alert group %s (%s/%s)\n",
			ep.Detail, ep.AlertGroupID, ep.Service, ep.Severity)
	}

	fmt.Fprintf(&b, "\nNoise sources\n")
	for _, n := range r.Noise {
		fmt.Fprintf(&b, "  - %s (%s): %d alerts across %d episodes (%.1f/episode)\n",
			n.AlertName, n.Service, n.Alerts, n.Episodes, n.AlertsPerEpisode())
		if n.AlertGroupID != "" {
			fmt.Fprintf(&b, "    example alert group: %s\n", n.AlertGroupID)
		}
	}
	if len(r.Noise) == 0 {
		b.WriteString("  (no alerts in this period)\n")
	}

	return b.String()
}

// buildOnCallQualityReport runs the digest's queries and assembles the
// struct. Pure aside from the reportDataSource calls, which is what makes it
// unit-testable against a fake — see report_test.go.
func (e *Engine) buildOnCallQualityReport(ctx context.Context, teamID string, from, to time.Time) (*Report, error) {
	if e.reportSource == nil {
		return nil, ErrReportsNotAvailable
	}
	ack, err := e.reportSource.AckAttainment(ctx, teamID, from, to)
	if err != nil {
		return nil, fmt.Errorf("ack attainment: %w", err)
	}
	lateEpisodes, err := e.reportSource.LateEpisodes(ctx, teamID, from, to, reportTopN)
	if err != nil {
		return nil, fmt.Errorf("late episodes: %w", err)
	}
	channels, err := e.reportSource.ChannelStats(ctx, teamID, from, to)
	if err != nil {
		return nil, fmt.Errorf("channel stats: %w", err)
	}
	nightHours, err := e.reportSource.NightPageCounts(ctx, teamID, from, to)
	if err != nil {
		return nil, fmt.Errorf("night page counts: %w", err)
	}
	esc, err := e.reportSource.EscalationStats(ctx, teamID, from, to)
	if err != nil {
		return nil, fmt.Errorf("escalation stats: %w", err)
	}
	exhausted, err := e.reportSource.ExhaustedEpisodes(ctx, teamID, from, to, reportTopN)
	if err != nil {
		return nil, fmt.Errorf("exhausted episodes: %w", err)
	}
	noise, err := e.reportSource.NoiseSources(ctx, teamID, from, to, reportTopN)
	if err != nil {
		return nil, fmt.Errorf("noise sources: %w", err)
	}
	return &Report{
		TeamID:            teamID,
		PeriodStart:       from,
		PeriodEnd:         to,
		GeneratedAt:       utils.UTCNow(),
		Format:            reportFormatV1,
		Ack:               ack,
		LateEpisodes:      lateEpisodes,
		Channels:          channels,
		NightPagesByHour:  nightHours,
		Escalations:       esc,
		ExhaustedEpisodes: exhausted,
		Noise:             noise,
	}, nil
}

// GenerateAndDeliverOnCallQualityReport builds the on-call quality digest for
// one team over the seven complete UTC days before now, stores it, and enqueues one
// email notification per configured recipient (NXS_ANOMALY_REPORT_RECIPIENTS)
// through the normal delivery/retry/circuit-breaker pipeline — modelled on
// ProcessScheduleShiftNotifications's synthetic, no-alert-group notification,
// see schedule_notify.go.
//
// Idempotent by (team, period): a second call for a period already generated
// — a manual re-run racing the scheduled one, or an overlapping CronJob
// execution — returns the report on file rather than sending a second copy.
// teamID "" is the installation-wide report.
func (e *Engine) GenerateAndDeliverOnCallQualityReport(ctx context.Context, teamID string, now time.Time) (map[string]any, error) {
	if e.reportSource == nil {
		return nil, ErrReportsNotAvailable
	}
	// Complete UTC days keep retries on the same day on one exact cohort.
	to := now.UTC().Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -7)
	id := reportID(teamID, from)

	// Cheap pre-check outside the lock: skip the ClickHouse round trip
	// entirely on the common case of a re-run for a period already done.
	if existing, err := e.store.GetItem(ctx, "reports", id); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	report, err := e.buildOnCallQualityReport(ctx, teamID, from, to)
	if err != nil {
		return nil, err
	}
	report.ID = id
	data := report.toMap()
	text, _ := data["summary_text"].(string)
	recipients := e.reportRecipients

	result, err := e.store.UpdateCollectionsFiltered(ctx,
		loadItems("reports", id),
		[]string{"reports", "notifications"},
		func(state *store.State) (any, error) {
			if existing, ok := state.Reports[id]; ok {
				// Generated by a concurrent run since the pre-check above.
				return existing, nil
			}
			state.Reports[id] = data
			ts := utils.ToISO(report.GeneratedAt)
			seen := map[string]struct{}{}
			for _, target := range recipients {
				idemKey := fmt.Sprintf("%s:email:%s", id, target)
				ntf := model.NewNotification("", "", "", "email", target,
					"oncall_quality_report", ts, idemKey)
				ntf.ScheduleDelivery(map[string]any{
					"kind":      "report_digest",
					"report_id": id,
					"text":      text,
				})
				addNotification(state, ntf, seen)
			}
			return data, nil
		}, advisoryLock["generate_oncall_report"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}
