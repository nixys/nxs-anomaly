package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// InsightsSummary is everything the insights screen shows, in one answer.
//
// It replaces twelve separate COUNT queries the frontend used to fire — one per
// tile and one per distribution row — which was twelve round trips for one
// screen, and which could not answer the question that screen exists for:
// whether things are getting better or worse. The buckets do that.
type InsightsSummary struct {
	// Point-in-time counts, within whatever retention keeps.
	GroupsByStatus map[string]int `json:"groups_by_status"`
	// Keyed by severity level, not by the spelling a source happened to send:
	// "high" and "error" are one bar, which is the same rule the filter and the
	// ordering use.
	GroupsByLevel        map[string]int `json:"groups_by_level"`
	NotificationsByState map[string]int `json:"notifications_by_state"`
	// One row per day in the requested range, oldest first, including days on
	// which nothing happened — a gap in a trend line must read as "nothing
	// happened", not as "no data was returned".
	Trend []InsightsBucket `json:"trend"`
}

// InsightsBucket is one day of the trend.
type InsightsBucket struct {
	Day       string `json:"day"`
	Opened    int    `json:"opened"`
	Resolved  int    `json:"resolved"`
	Delivered int    `json:"delivered"`
	Failed    int    `json:"failed"`
}

// severityLevelSQL maps the stored spelling to its level inside SQL, so a
// GROUP BY counts "high" and "error" as one thing. Built from severityAliases
// for the same reason severityRankSQL is: one table, no second place to forget.
func severityLevelSQL(column string) string {
	levels := make([]string, 0, len(severityAliases))
	for level := range severityAliases {
		levels = append(levels, level)
	}
	sort.Strings(levels)
	var b strings.Builder
	b.WriteString("CASE lower(" + column + ")")
	for _, level := range levels {
		for _, alias := range severityAliases[level] {
			fmt.Fprintf(&b, " WHEN '%s' THEN '%s'", alias, level)
		}
	}
	b.WriteString(" ELSE 'unknown' END")
	return b.String()
}

// InsightsSummaryQuery gathers the whole insights screen in three statements.
//
// integrationIDs narrows to what the caller may see (nil means unrestricted);
// integrationID narrows further to the one the operator picked. from and to
// bound the trend only: the tiles are current state, which is what makes them
// tiles rather than another chart.
func (s *pgStore) InsightsSummaryQuery(ctx context.Context, integrationID string, integrationIDs []string, from, to time.Time) (InsightsSummary, error) {
	out := InsightsSummary{
		GroupsByStatus:       map[string]int{},
		GroupsByLevel:        map[string]int{},
		NotificationsByState: map[string]int{},
	}

	where, args := insightsScope(integrationID, integrationIDs, "")
	groupWhere := ""
	if where != "" {
		groupWhere = " WHERE " + where
	}

	rows, err := s.pool.Query(ctx,
		"SELECT status, "+severityLevelSQL("severity")+" AS level, COUNT(*) "+
			"FROM nxs_anomaly_alert_groups"+groupWhere+" GROUP BY 1, 2", args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var status, level string
		var n int
		if err := rows.Scan(&status, &level, &n); err != nil {
			rows.Close()
			return out, err
		}
		out.GroupsByStatus[status] += n
		out.GroupsByLevel[level] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	nWhere, nArgs := insightsScope(integrationID, integrationIDs, "")
	notifWhere := ""
	if nWhere != "" {
		notifWhere = " WHERE " + nWhere
	}
	rows, err = s.pool.Query(ctx,
		"SELECT status, COUNT(*) FROM nxs_anomaly_notifications"+notifWhere+" GROUP BY 1", nArgs...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return out, err
		}
		out.NotificationsByState[status] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	buckets, err := s.insightsTrend(ctx, integrationID, integrationIDs, from, to)
	if err != nil {
		return out, err
	}
	out.Trend = buckets
	return out, nil
}

// insightsScope builds the shared WHERE for both tables. Both carry a typed
// integration_id column, so one helper serves both.
func insightsScope(integrationID string, integrationIDs []string, extra string) (string, []any) {
	var clauses []string
	var args []any
	if integrationID != "" {
		args = append(args, integrationID)
		clauses = append(clauses, fmt.Sprintf("integration_id = $%d", len(args)))
	}
	if integrationIDs != nil {
		if len(integrationIDs) == 0 {
			// A caller scoped to no integration sees nothing — an empty result,
			// not an unscoped one.
			clauses = append(clauses, "FALSE")
		} else {
			phs := make([]string, len(integrationIDs))
			for i, id := range integrationIDs {
				args = append(args, id)
				phs[i] = fmt.Sprintf("$%d", len(args))
			}
			clauses = append(clauses, "integration_id IN ("+strings.Join(phs, ",")+")")
		}
	}
	if extra != "" {
		clauses = append(clauses, extra)
	}
	return strings.Join(clauses, " AND "), args
}

// insightsTrend counts openings, resolutions and delivery outcomes per day.
//
// Days with no rows are filled in here rather than left out: a line chart that
// skips empty days draws a quiet week as a straight line between two spikes.
func (s *pgStore) insightsTrend(ctx context.Context, integrationID string, integrationIDs []string, from, to time.Time) ([]InsightsBucket, error) {
	byDay := map[string]*InsightsBucket{}
	day := from.UTC().Truncate(24 * time.Hour)
	end := to.UTC()
	for !day.After(end) {
		key := day.Format("2006-01-02")
		byDay[key] = &InsightsBucket{Day: key}
		day = day.Add(24 * time.Hour)
	}

	gWhere, gArgs := insightsScope(integrationID, integrationIDs, "created_at >= $$FROM$$ AND created_at <= $$TO$$")
	gWhere, gArgs = bindRange(gWhere, gArgs, from, to)
	rows, err := s.pool.Query(ctx,
		"SELECT to_char(date_trunc('day', created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day, "+
			"COUNT(*) AS opened, "+
			"COUNT(*) FILTER (WHERE status = 'resolved') AS resolved "+
			"FROM nxs_anomaly_alert_groups WHERE "+gWhere+" GROUP BY 1", gArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		var opened, resolved int
		if err := rows.Scan(&key, &opened, &resolved); err != nil {
			rows.Close()
			return nil, err
		}
		if b, ok := byDay[key]; ok {
			b.Opened = opened
			b.Resolved = resolved
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	nWhere, nArgs := insightsScope(integrationID, integrationIDs, "created_at >= $$FROM$$ AND created_at <= $$TO$$")
	nWhere, nArgs = bindRange(nWhere, nArgs, from, to)
	rows, err = s.pool.Query(ctx,
		"SELECT to_char(date_trunc('day', created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day, "+
			"COUNT(*) FILTER (WHERE status = 'delivered') AS delivered, "+
			"COUNT(*) FILTER (WHERE status = 'failed') AS failed "+
			"FROM nxs_anomaly_notifications WHERE "+nWhere+" GROUP BY 1", nArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		var delivered, failed int
		if err := rows.Scan(&key, &delivered, &failed); err != nil {
			rows.Close()
			return nil, err
		}
		if b, ok := byDay[key]; ok {
			b.Delivered = delivered
			b.Failed = failed
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(byDay))
	for k := range byDay {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]InsightsBucket, 0, len(keys))
	for _, k := range keys {
		out = append(out, *byDay[k])
	}
	return out, nil
}

// bindRange substitutes the two range placeholders with real argument numbers.
// The markers keep insightsScope free of any knowledge about how many arguments
// its caller already used.
func bindRange(where string, args []any, from, to time.Time) (string, []any) {
	args = append(args, from)
	where = strings.Replace(where, "$$FROM$$", fmt.Sprintf("$%d", len(args)), 1)
	args = append(args, to)
	where = strings.Replace(where, "$$TO$$", fmt.Sprintf("$%d", len(args)), 1)
	return where, args
}
