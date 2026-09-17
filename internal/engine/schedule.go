package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Schedule v2: one native rotation per schedule.
//
// A schedule resolves who is on call at an instant from three sources, in
// descending priority: an active override, the rotation, and the legacy
// shift list kept for schedules created before the rotation existed. Only one
// source answers for a given instant; the rotation is not merged with shifts,
// because a schedule that has been migrated should stop behaving like two
// half-configured schedules at once.
//
// All wall-clock reasoning happens in the schedule's timezone. Handoffs in
// days or weeks step the calendar (AddDate), so a weekly handoff at 10:00
// local stays at 10:00 local across a DST transition; handoffs in hours are
// absolute durations, because "every 8 hours" means eight hours of real time.

// rotationUnits are the handoff intervals a rotation may use. Months are
// deliberately absent: a monthly on-call handoff is a scheduling decision that
// wants explicit dates, not calendar arithmetic with 28-to-31-day periods.
var rotationUnits = map[string]bool{"hours": true, "days": true, "weeks": true}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// weekdayOrder is the canonical order restriction days are stored in, so a
// round-trip through the API does not reshuffle what the user typed.
var weekdayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// defaultPreviewWindow is how far ahead a preview and a coverage check look
// when the caller does not say. Four weeks is the acceptance criterion for the
// preview and covers a full rotation cycle for every supported handoff.
const defaultPreviewWindow = 28 * 24 * time.Hour

// maxPreviewWindow caps an explicit range. The preview walks every boundary in
// the window, so an unbounded range is an unbounded response.
const maxPreviewWindow = 180 * 24 * time.Hour

// coverageCheckWindow is the horizon a schedule must cover before it can be
// attached to an escalation chain without an explicit acknowledgement.
const coverageCheckWindow = 7 * 24 * time.Hour

// maxBoundaryPoints bounds each boundary-generating loop. It is a runaway
// guard, not a budget: the tightest supported configuration — an hourly
// handoff over the 180-day maximum window — needs 4320 points, so the limit
// must stay comfortably above that or a preview would silently end in one
// long wrong segment instead of the real handoffs.
const maxBoundaryPoints = 50000

// rotation is the parsed, validated form of schedule["rotation"].
type rotation struct {
	enabled        bool
	startAt        time.Time
	interval       int
	unit           string
	participantIDs []string
	restriction    *restriction
}

// restriction narrows a rotation to a daily wall-clock window on selected
// weekdays — the "business hours" layer of a schedule that would otherwise
// page its rotation around the clock.
type restriction struct {
	startMinute int // minutes from local midnight
	endMinute   int
	days        []string
}

// scheduleLocation resolves the schedule timezone. An unknown or empty zone
// falls back to UTC rather than failing the read path: a schedule that was
// stored with a zone this binary cannot load must still page someone.
func scheduleLocation(schedule map[string]any) *time.Location {
	name := utils.StrVal(schedule, "timezone")
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func parseRotationFromSchedule(schedule map[string]any) (*rotation, bool) {
	raw, ok := schedule["rotation"].(map[string]any)
	if !ok {
		return nil, false
	}
	rot, err := parseRotation(raw)
	if err != nil || rot == nil {
		return nil, false
	}
	return rot, rot.enabled && len(rot.participantIDs) > 0
}

// parseRotation validates and normalises a rotation payload. A nil result with
// a nil error means "no rotation configured".
func parseRotation(raw map[string]any) (*rotation, error) {
	if raw == nil {
		return nil, nil
	}
	rot := &rotation{
		enabled:  utils.BoolVal(raw, "enabled", true),
		interval: utils.IntVal(raw, "handoff_interval"),
		unit:     strings.ToLower(strDefault(utils.StrVal(raw, "handoff_unit"), "weeks")),
	}
	ids, err := utils.CoerceStringList(raw["participant_ids"])
	if err != nil {
		return nil, fmt.Errorf("invalid participant_ids: %w", err)
	}
	rot.participantIDs = ids

	if !rotationUnits[rot.unit] {
		return nil, fmt.Errorf("unsupported handoff_unit: %s", rot.unit)
	}
	if rot.interval <= 0 {
		rot.interval = 1
	}
	startStr := utils.StrVal(raw, "start_at")
	if startStr == "" {
		if len(rot.participantIDs) == 0 {
			return rot, nil
		}
		return nil, fmt.Errorf("rotation start_at is required")
	}
	start, err := utils.ParseDatetime(startStr)
	if err != nil {
		return nil, fmt.Errorf("invalid rotation start_at: %w", err)
	}
	rot.startAt = start

	res, err := parseRestriction(raw["restriction"])
	if err != nil {
		return nil, err
	}
	rot.restriction = res
	return rot, nil
}

func parseRestriction(raw any) (*restriction, error) {
	m, ok := raw.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, nil
	}
	startStr := utils.StrVal(m, "start")
	endStr := utils.StrVal(m, "end")
	if startStr == "" && endStr == "" {
		return nil, nil
	}
	start, err := parseClockMinute(startStr)
	if err != nil {
		return nil, fmt.Errorf("invalid restriction start: %w", err)
	}
	end, err := parseClockMinute(endStr)
	if err != nil {
		return nil, fmt.Errorf("invalid restriction end: %w", err)
	}
	if start == end {
		return nil, fmt.Errorf("restriction start and end must differ")
	}
	res := &restriction{startMinute: start, endMinute: end}
	days, err := utils.CoerceStringList(m["days"])
	if err != nil {
		return nil, fmt.Errorf("invalid restriction days: %w", err)
	}
	seen := map[string]bool{}
	for _, d := range days {
		key := strings.ToLower(strings.TrimSpace(d))
		if len(key) > 3 {
			key = key[:3]
		}
		if _, ok := weekdayNames[key]; !ok {
			return nil, fmt.Errorf("unsupported restriction day: %s", d)
		}
		seen[key] = true
	}
	for _, day := range weekdayOrder {
		if seen[day] {
			res.days = append(res.days, day)
		}
	}
	return res, nil
}

// parseClockMinute reads "HH:MM" into minutes from midnight.
func parseClockMinute(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("expected HH:MM, got %q", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func formatClockMinute(m int) string {
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// rotationJSON renders a rotation back into the stored representation.
func rotationJSON(rot *rotation) map[string]any {
	if rot == nil {
		return nil
	}
	out := map[string]any{
		"enabled":          rot.enabled,
		"handoff_interval": rot.interval,
		"handoff_unit":     rot.unit,
		"participant_ids":  toAnySlice(rot.participantIDs),
	}
	if !rot.startAt.IsZero() {
		out["start_at"] = utils.ToISO(rot.startAt)
	}
	if rot.restriction != nil {
		out["restriction"] = map[string]any{
			"start": formatClockMinute(rot.restriction.startMinute),
			"end":   formatClockMinute(rot.restriction.endMinute),
			"days":  toAnySlice(rot.restriction.days),
		}
	}
	return out
}

// handoffAt returns the instant of the n-th handoff after the rotation start.
// n may be negative; the caller's search brackets the target time from both
// sides.
func (r *rotation) handoffAt(n int, loc *time.Location) time.Time {
	if r.unit == "hours" {
		return r.startAt.Add(time.Duration(n*r.interval) * time.Hour)
	}
	days := n * r.interval
	if r.unit == "weeks" {
		days *= 7
	}
	// AddDate on a time in loc preserves the wall clock, which is what a
	// handoff "every day at 10:00" means through a DST transition.
	return r.startAt.In(loc).AddDate(0, 0, days)
}

// periodIndex returns the number of completed handoff periods between the
// rotation start and at. It estimates from the average period length and then
// corrects, so a rotation whose start is years back costs the same as one that
// started yesterday.
func (r *rotation) periodIndex(at time.Time, loc *time.Location) int {
	var avg time.Duration
	switch r.unit {
	case "hours":
		avg = time.Duration(r.interval) * time.Hour
	case "days":
		avg = time.Duration(r.interval) * 24 * time.Hour
	default:
		avg = time.Duration(r.interval) * 7 * 24 * time.Hour
	}
	n := int(at.Sub(r.startAt) / avg)
	// Correct in both directions: DST shifts and the integer division above
	// can leave the estimate one period off either way.
	for i := 0; i < 8 && r.handoffAt(n, loc).After(at); i++ {
		n--
	}
	for i := 0; i < 8 && !r.handoffAt(n+1, loc).After(at); i++ {
		n++
	}
	return n
}

// participantAt returns the rotation participant on call at at, or "" when the
// rotation has not started yet or a restriction window excludes this instant.
func (r *rotation) participantAt(at time.Time, loc *time.Location) string {
	if !r.enabled || len(r.participantIDs) == 0 || r.startAt.IsZero() {
		return ""
	}
	if at.Before(r.startAt) {
		return ""
	}
	if !r.restrictionAllows(at, loc) {
		return ""
	}
	n := r.periodIndex(at, loc)
	if n < 0 {
		return ""
	}
	return r.participantIDs[n%len(r.participantIDs)]
}

// restrictionAllows reports whether at falls inside the daily window. A window
// whose end is before its start spans midnight, and is attributed to the
// weekday it started on.
func (r *rotation) restrictionAllows(at time.Time, loc *time.Location) bool {
	res := r.restriction
	if res == nil {
		return true
	}
	local := at.In(loc)
	minute := local.Hour()*60 + local.Minute()
	day := local.Weekday()
	if res.endMinute > res.startMinute {
		if minute < res.startMinute || minute >= res.endMinute {
			return false
		}
		return res.allowsDay(day)
	}
	// Overnight window: before the end belongs to the previous day's window.
	if minute < res.endMinute {
		return res.allowsDay((day + 6) % 7)
	}
	if minute >= res.startMinute {
		return res.allowsDay(day)
	}
	return false
}

func (res *restriction) allowsDay(day time.Weekday) bool {
	if len(res.days) == 0 {
		return true
	}
	for _, name := range res.days {
		if weekdayNames[name] == day {
			return true
		}
	}
	return false
}

// --- resolution ---

// scheduleSource names which layer answered an on-call query.
const (
	sourceOverride = "override"
	sourceRotation = "rotation"
	sourceShift    = "shift"
	sourceNone     = ""
)

// resolveScheduleAt returns the on-call user IDs at at and the layer they came
// from. A schedule with "enabled": false answers for nobody: disabling is how
// an operator takes a schedule out of service without deleting it.
func resolveScheduleAt(schedule map[string]any, at time.Time) ([]string, string) {
	if schedule == nil {
		return nil, sourceNone
	}
	if !utils.BoolVal(schedule, "enabled", true) {
		return nil, sourceNone
	}
	loc := scheduleLocation(schedule)

	if ids := activeOverrideUsers(schedule, at); len(ids) > 0 {
		return ids, sourceOverride
	}
	if rot, ok := parseRotationFromSchedule(schedule); ok {
		if uid := rot.participantAt(at, loc); uid != "" {
			return []string{uid}, sourceRotation
		}
		// A configured rotation owns the schedule: falling through to legacy
		// shifts here would page someone the rotation deliberately excluded.
		return nil, sourceNone
	}
	if ids := activeShiftUsers(schedule, at); len(ids) > 0 {
		return ids, sourceShift
	}
	return nil, sourceNone
}

// activeOverrideUsers returns the user covering at, when an override does.
//
// Overrides are allowed to overlap — cover for cover happens — so the tie has
// to be broken somehow. The most recently created override wins: it is the
// later decision, and it is the one whoever created it expects to take effect.
// Ties on created_at fall back to list order, which is creation order.
func activeOverrideUsers(schedule map[string]any, at time.Time) []string {
	var winner map[string]any
	var winnerAt time.Time
	for _, ov := range anyList(schedule["overrides"]) {
		o, ok := ov.(map[string]any)
		if !ok {
			continue
		}
		startStr := utils.StrVal(o, "start_at")
		if startStr == "" {
			startStr = utils.StrVal(o, "created_at")
		}
		startAt, err1 := utils.ParseDatetime(startStr)
		untilAt, err2 := utils.ParseDatetime(utils.StrVal(o, "until"))
		if err1 != nil || err2 != nil {
			continue
		}
		if at.Before(startAt) || !at.Before(untilAt) {
			continue
		}
		created, err := utils.ParseDatetime(utils.StrVal(o, "created_at"))
		if err != nil {
			created = time.Time{}
		}
		if winner == nil || !created.Before(winnerAt) {
			winner, winnerAt = o, created
		}
	}
	if winner == nil {
		return nil
	}
	return []string{utils.StrVal(winner, "user_id")}
}

func activeShiftUsers(schedule map[string]any, at time.Time) []string {
	shifts, _ := schedule["shifts"].([]any)
	var ids []string
	for _, sh := range shifts {
		s, ok := sh.(map[string]any)
		if !ok {
			continue
		}
		if shiftActive(s, at) {
			ids = append(ids, utils.StrVal(s, "user_id"))
		}
	}
	return ids
}

// --- preview and coverage ---

// scheduleSegment is one interval of unchanged on-call assignment.
type scheduleSegment struct {
	Start   time.Time
	End     time.Time
	UserIDs []string
	Source  string
}

// scheduleTimeline walks the schedule between from and to and returns
// contiguous segments, including empty ones (the gaps). It evaluates the
// schedule at every instant where the answer can change — handoffs,
// restriction window edges, shift and override boundaries — which is exact for
// the supported model and avoids the aliasing a fixed sampling step would have.
func scheduleTimeline(schedule map[string]any, from, to time.Time) []scheduleSegment {
	if schedule == nil || !to.After(from) {
		return nil
	}
	points := scheduleBoundaries(schedule, from, to)
	var segments []scheduleSegment
	for i := 0; i < len(points)-1; i++ {
		start, end := points[i], points[i+1]
		if !end.After(start) {
			continue
		}
		ids, source := resolveScheduleAt(schedule, start)
		if n := len(segments); n > 0 {
			prev := &segments[n-1]
			if prev.Source == source && sameIDs(prev.UserIDs, ids) {
				prev.End = end
				continue
			}
		}
		segments = append(segments, scheduleSegment{Start: start, End: end, UserIDs: ids, Source: source})
	}
	return segments
}

// scheduleBoundaries collects every instant in [from, to] at which the on-call
// answer may change, sorted and deduplicated, always including from and to.
func scheduleBoundaries(schedule map[string]any, from, to time.Time) []time.Time {
	loc := scheduleLocation(schedule)
	points := []time.Time{from, to}
	add := func(t time.Time) {
		if !t.Before(from) && !t.After(to) {
			points = append(points, t)
		}
	}

	for _, ov := range anyList(schedule["overrides"]) {
		o, ok := ov.(map[string]any)
		if !ok {
			continue
		}
		if t, err := utils.ParseDatetime(utils.StrVal(o, "start_at")); err == nil {
			add(t)
		}
		if t, err := utils.ParseDatetime(utils.StrVal(o, "until")); err == nil {
			add(t)
		}
	}

	if rot, ok := parseRotationFromSchedule(schedule); ok {
		n := rot.periodIndex(from, loc)
		if n < 0 {
			n = 0
		}
		for i := 0; i < maxBoundaryPoints; i++ {
			t := rot.handoffAt(n+i, loc)
			if t.After(to) {
				break
			}
			add(t)
		}
		if rot.restriction != nil {
			// One window edge pair per local day in the range, plus a day of
			// slack on each side so an overnight window is not clipped.
			day := from.In(loc).AddDate(0, 0, -1)
			for i := 0; i < maxBoundaryPoints && !day.After(to.In(loc).AddDate(0, 0, 1)); i++ {
				midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
				add(midnight.Add(time.Duration(rot.restriction.startMinute) * time.Minute))
				add(midnight.Add(time.Duration(rot.restriction.endMinute) * time.Minute))
				day = day.AddDate(0, 0, 1)
			}
		}
	} else {
		for _, sh := range anyList(schedule["shifts"]) {
			s, ok := sh.(map[string]any)
			if !ok {
				continue
			}
			start, err1 := utils.ParseDatetime(utils.StrVal(s, "start_at"))
			end, err2 := utils.ParseDatetime(utils.StrVal(s, "end_at"))
			if err1 != nil || err2 != nil {
				continue
			}
			recurrence := strings.ToLower(utils.StrVal(s, "recurrence"))
			period := time.Duration(0)
			switch recurrence {
			case "daily":
				period = 24 * time.Hour
			case "weekly":
				period = 7 * 24 * time.Hour
			}
			if period == 0 {
				add(start)
				add(end)
				continue
			}
			duration := end.Sub(start)
			occurrence := start
			if occurrence.Before(from) {
				skipped := from.Sub(occurrence) / period
				occurrence = occurrence.Add(skipped * period)
			}
			for i := 0; i < maxBoundaryPoints && !occurrence.After(to); i++ {
				add(occurrence)
				add(occurrence.Add(duration))
				occurrence = occurrence.Add(period)
			}
		}
	}

	sort.Slice(points, func(i, j int) bool { return points[i].Before(points[j]) })
	deduped := points[:0]
	for i, p := range points {
		if i > 0 && p.Equal(deduped[len(deduped)-1]) {
			continue
		}
		deduped = append(deduped, p)
	}
	return deduped
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// applyRoster drops user IDs that no longer exist from a timeline.
//
// A rotation keeps naming a participant after that person is deleted, and a
// segment naming a ghost is not coverage: nothing would page. Rewriting those
// segments to empty makes them show up as the gaps they actually are, in the
// preview, in the coverage ratio and in the chain gate. known == nil means
// "the caller cannot check", and the timeline passes through untouched.
func applyRoster(segments []scheduleSegment, known map[string]bool) []scheduleSegment {
	if known == nil {
		return segments
	}
	out := make([]scheduleSegment, 0, len(segments))
	for _, seg := range segments {
		kept := make([]string, 0, len(seg.UserIDs))
		for _, uid := range seg.UserIDs {
			if known[uid] {
				kept = append(kept, uid)
			}
		}
		if len(kept) == 0 {
			seg.UserIDs = nil
			seg.Source = sourceNone
		} else {
			seg.UserIDs = kept
		}
		// Merge with the previous segment when the rewrite made them equal:
		// two adjacent ghost segments are one gap, not two.
		if n := len(out); n > 0 && out[n-1].Source == seg.Source && sameIDs(out[n-1].UserIDs, seg.UserIDs) {
			out[n-1].End = seg.End
			continue
		}
		out = append(out, seg)
	}
	return out
}

// scheduleGaps returns the uncovered intervals of a schedule in [from, to].
// Pass the known roster to count segments naming deleted users as gaps.
func scheduleGaps(schedule map[string]any, from, to time.Time, known map[string]bool) []scheduleSegment {
	var gaps []scheduleSegment
	for _, seg := range applyRoster(scheduleTimeline(schedule, from, to), known) {
		if len(seg.UserIDs) == 0 {
			gaps = append(gaps, seg)
		}
	}
	return gaps
}

// scheduleOverlaps returns the intervals in which more than one person is on
// call at once.
func scheduleOverlaps(schedule map[string]any, from, to time.Time, known map[string]bool) []scheduleSegment {
	var overlaps []scheduleSegment
	for _, seg := range applyRoster(scheduleTimeline(schedule, from, to), known) {
		if len(seg.UserIDs) > 1 {
			overlaps = append(overlaps, seg)
		}
	}
	return overlaps
}

// ScheduleOnCallAt reports who is on call in a stored schedule at an instant.
// Exported for the Grafana compatibility layer, which holds schedule rows but
// not the engine's internals.
func ScheduleOnCallAt(schedule map[string]any, at time.Time) []string {
	ids, _ := resolveScheduleAt(schedule, at)
	return ids
}

// ScheduleHasGaps reports whether a stored schedule leaves any part of the
// next week uncovered. Exported for the same reason as ScheduleOnCallAt.
// ShiftEndFor returns when userID's current on-call stretch in this schedule
// ends, and false when they are not on call in it at `at`.
//
// It is what makes a check-in expire on its own. The alternative — a fixed
// timeout — either outlives the shift, leaving a stale "I am watching this"
// behind someone who went to bed, or cuts short a long one and silently drops
// the flag mid-shift.
//
// The horizon is a week: a stretch longer than that is a schedule with no
// rotation at all, and the fallback timeout covers it.
func ShiftEndFor(schedule map[string]any, userID string, at time.Time) (time.Time, bool) {
	for _, seg := range scheduleTimeline(schedule, at, at.AddDate(0, 0, 7)) {
		if seg.Start.After(at) {
			break
		}
		if !seg.End.After(at) {
			continue
		}
		for _, id := range seg.UserIDs {
			if id == userID {
				return seg.End, true
			}
		}
	}
	return time.Time{}, false
}

func ScheduleHasGaps(schedule map[string]any, from time.Time) bool {
	return len(scheduleGaps(schedule, from, from.Add(coverageCheckWindow), nil)) > 0
}

// segmentJSON renders a segment for the API.
func segmentJSON(seg scheduleSegment) map[string]any {
	ids := seg.UserIDs
	if ids == nil {
		ids = []string{}
	}
	return map[string]any{
		"start":            utils.ToISO(seg.Start),
		"end":              utils.ToISO(seg.End),
		"user_ids":         toAnySlice(ids),
		"source":           seg.Source,
		"duration_seconds": int(seg.End.Sub(seg.Start).Seconds()),
	}
}
