package engine

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Schedule v2 unit tests. They drive resolveScheduleAt / scheduleTimeline
// directly with map payloads in the stored representation, so they cover what
// the worker actually reads, and the CRUD paths through the in-memory store.

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := utils.ParseDatetime(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func weeklyRotation(participants ...string) map[string]any {
	return map[string]any{
		"id":       "sch_1",
		"timezone": "Europe/Berlin",
		"rotation": map[string]any{
			"enabled":          true,
			"start_at":         "2026-03-02T10:00:00Z",
			"handoff_interval": 1,
			"handoff_unit":     "weeks",
			"participant_ids":  toAnySlice(participants),
		},
	}
}

func TestRotationCyclesThroughParticipants(t *testing.T) {
	sched := weeklyRotation("u_a", "u_b", "u_c")
	cases := []struct {
		at   string
		want string
	}{
		{"2026-03-02T10:00:00Z", "u_a"}, // exactly at the start
		{"2026-03-08T23:59:00Z", "u_a"}, // last minute of the first week
		{"2026-03-09T10:00:00Z", "u_b"},
		{"2026-03-16T10:00:00Z", "u_c"},
		{"2026-03-23T10:00:00Z", "u_a"}, // wraps around
		{"2026-03-02T09:59:00Z", ""},    // before the rotation starts
	}
	for _, tc := range cases {
		got, source := resolveScheduleAt(sched, mustTime(t, tc.at))
		if tc.want == "" {
			if len(got) != 0 {
				t.Errorf("at %s: got %v, want nobody", tc.at, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("at %s: got %v, want [%s]", tc.at, got, tc.want)
		}
		if source != sourceRotation {
			t.Errorf("at %s: source = %q, want %q", tc.at, source, sourceRotation)
		}
	}
}

// A handoff in days or weeks is a wall-clock event: Europe/Berlin moves from
// UTC+1 to UTC+2 on 2026-03-29, and the 10:00 local handoff must move with it.
func TestRotationHandoffKeepsWallClockAcrossDST(t *testing.T) {
	sched := map[string]any{
		"timezone": "Europe/Berlin",
		"rotation": map[string]any{
			"enabled":          true,
			"start_at":         "2026-03-27T09:00:00Z", // 10:00 Berlin, CET
			"handoff_interval": 1,
			"handoff_unit":     "days",
			"participant_ids":  []any{"u_a", "u_b"},
		},
	}
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	rot, ok := parseRotationFromSchedule(sched)
	if !ok {
		t.Fatal("rotation not parsed")
	}
	for n := 0; n < 6; n++ {
		local := rot.handoffAt(n, loc)
		if local.Hour() != 10 || local.Minute() != 0 {
			t.Errorf("handoff %d at %s: wall clock = %02d:%02d, want 10:00",
				n, local.Format(time.RFC3339), local.Hour(), local.Minute())
		}
	}
	// After the transition the same wall clock is one UTC hour earlier.
	if got := rot.handoffAt(3, loc).UTC().Format("15:04"); got != "08:00" {
		t.Errorf("post-DST handoff in UTC = %s, want 08:00", got)
	}
	// And the roster still alternates correctly around the transition.
	for _, tc := range []struct{ at, want string }{
		{"2026-03-27T09:30:00Z", "u_a"},
		{"2026-03-28T09:30:00Z", "u_b"},
		{"2026-03-29T09:30:00Z", "u_a"}, // 11:30 CEST, after the 10:00 handoff
		{"2026-03-30T08:30:00Z", "u_b"}, // 10:30 CEST
	} {
		got, _ := resolveScheduleAt(sched, mustTime(t, tc.at))
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("at %s: got %v, want [%s]", tc.at, got, tc.want)
		}
	}
}

// An hourly handoff is an absolute duration: eight hours of real time stay
// eight hours across the DST boundary, so the wall clock shifts instead.
func TestRotationHourlyHandoffIsAbsolute(t *testing.T) {
	rot := &rotation{
		enabled:        true,
		startAt:        mustTime(t, "2026-03-29T00:00:00Z"),
		interval:       8,
		unit:           "hours",
		participantIDs: []string{"u_a", "u_b"},
	}
	loc, _ := time.LoadLocation("Europe/Berlin")
	if got := rot.handoffAt(1, loc).Sub(rot.startAt); got != 8*time.Hour {
		t.Errorf("hourly handoff spacing = %v, want 8h", got)
	}
	if uid := rot.participantAt(mustTime(t, "2026-03-29T09:00:00Z"), loc); uid != "u_b" {
		t.Errorf("participant = %q, want u_b", uid)
	}
}

func TestRotationRestrictionWindow(t *testing.T) {
	sched := map[string]any{
		"timezone": "Europe/Berlin",
		"rotation": map[string]any{
			"enabled":          true,
			"start_at":         "2026-06-01T00:00:00Z",
			"handoff_interval": 1,
			"handoff_unit":     "weeks",
			"participant_ids":  []any{"u_a"},
			"restriction": map[string]any{
				"start": "09:00",
				"end":   "18:00",
				"days":  []any{"mon", "tue", "wed", "thu", "fri"},
			},
		},
	}
	// 2026-06-01 is a Monday; times are Berlin local (UTC+2 in June).
	cases := []struct {
		at      string
		covered bool
	}{
		{"2026-06-01T08:00:00Z", true},  // 10:00 Mon
		{"2026-06-01T05:00:00Z", false}, // 07:00 Mon, before the window
		{"2026-06-01T17:00:00Z", false}, // 19:00 Mon, after the window
		{"2026-06-06T10:00:00Z", false}, // Saturday
	}
	for _, tc := range cases {
		got, _ := resolveScheduleAt(sched, mustTime(t, tc.at))
		if covered := len(got) > 0; covered != tc.covered {
			t.Errorf("at %s: covered = %v, want %v (got %v)", tc.at, covered, tc.covered, got)
		}
	}
}

func TestRotationOvernightRestrictionWindow(t *testing.T) {
	sched := map[string]any{
		"timezone": "UTC",
		"rotation": map[string]any{
			"enabled":          true,
			"start_at":         "2026-06-01T00:00:00Z",
			"handoff_interval": 1,
			"handoff_unit":     "weeks",
			"participant_ids":  []any{"u_a"},
			"restriction": map[string]any{
				"start": "22:00",
				"end":   "06:00",
				"days":  []any{"mon"},
			},
		},
	}
	for _, tc := range []struct {
		at      string
		covered bool
	}{
		{"2026-06-01T23:00:00Z", true},  // Monday night
		{"2026-06-02T03:00:00Z", true},  // Tuesday morning, same Monday window
		{"2026-06-02T23:00:00Z", false}, // Tuesday night is not configured
		{"2026-06-01T12:00:00Z", false}, // Monday midday
	} {
		got, _ := resolveScheduleAt(sched, mustTime(t, tc.at))
		if covered := len(got) > 0; covered != tc.covered {
			t.Errorf("at %s: covered = %v, want %v", tc.at, covered, tc.covered)
		}
	}
}

func TestOverrideBeatsRotationAndDisabledScheduleCoversNobody(t *testing.T) {
	sched := weeklyRotation("u_a", "u_b")
	sched["overrides"] = []any{map[string]any{
		"id": "ovr_1", "user_id": "u_z",
		"start_at": "2026-03-03T00:00:00Z", "until": "2026-03-04T00:00:00Z",
	}}
	got, source := resolveScheduleAt(sched, mustTime(t, "2026-03-03T12:00:00Z"))
	if len(got) != 1 || got[0] != "u_z" || source != sourceOverride {
		t.Errorf("override window: got %v/%s, want [u_z]/override", got, source)
	}
	got, source = resolveScheduleAt(sched, mustTime(t, "2026-03-04T12:00:00Z"))
	if len(got) != 1 || got[0] != "u_a" || source != sourceRotation {
		t.Errorf("after override: got %v/%s, want [u_a]/rotation", got, source)
	}

	sched["enabled"] = false
	if got, _ := resolveScheduleAt(sched, mustTime(t, "2026-03-03T12:00:00Z")); len(got) != 0 {
		t.Errorf("disabled schedule covered %v, want nobody", got)
	}
}

// Schedules written before Schedule v2 have shifts and no rotation; they must
// keep resolving exactly as they did.
func TestLegacyShiftsStillResolve(t *testing.T) {
	sched := map[string]any{
		"timezone": "UTC",
		"shifts": []any{map[string]any{
			"id": "shift_1", "user_id": "u_legacy",
			"start_at": "2026-06-01T09:00:00Z", "end_at": "2026-06-01T17:00:00Z",
			"recurrence": "daily",
		}},
	}
	got, source := resolveScheduleAt(sched, mustTime(t, "2026-06-03T10:00:00Z"))
	if len(got) != 1 || got[0] != "u_legacy" || source != sourceShift {
		t.Errorf("legacy shift: got %v/%s, want [u_legacy]/shift", got, source)
	}
	if got, _ := resolveScheduleAt(sched, mustTime(t, "2026-06-03T20:00:00Z")); len(got) != 0 {
		t.Errorf("outside shift: got %v, want nobody", got)
	}
}

// A rotation owns the schedule: legacy shifts must not leak back in for the
// hours the rotation deliberately leaves uncovered.
func TestRotationSupersedesLegacyShifts(t *testing.T) {
	sched := weeklyRotation("u_a")
	sched["rotation"].(map[string]any)["restriction"] = map[string]any{"start": "09:00", "end": "18:00"}
	sched["shifts"] = []any{map[string]any{
		"id": "shift_1", "user_id": "u_legacy",
		"start_at": "2026-03-02T00:00:00Z", "end_at": "2026-03-30T00:00:00Z",
	}}
	if got, _ := resolveScheduleAt(sched, mustTime(t, "2026-03-10T03:00:00Z")); len(got) != 0 {
		t.Errorf("outside restriction: got %v, want nobody", got)
	}
}

func TestTimelineSegmentsAndGaps(t *testing.T) {
	sched := map[string]any{
		"timezone": "UTC",
		"rotation": map[string]any{
			"enabled":          true,
			"start_at":         "2026-06-01T00:00:00Z",
			"handoff_interval": 1,
			"handoff_unit":     "days",
			"participant_ids":  []any{"u_a", "u_b"},
			"restriction":      map[string]any{"start": "09:00", "end": "17:00"},
		},
	}
	from := mustTime(t, "2026-06-01T00:00:00Z")
	to := mustTime(t, "2026-06-03T00:00:00Z")
	segments := scheduleTimeline(sched, from, to)
	if len(segments) == 0 {
		t.Fatal("no segments")
	}
	// The timeline must tile [from, to) exactly: no holes, no overlaps.
	if !segments[0].Start.Equal(from) {
		t.Errorf("first segment starts at %s, want %s", segments[0].Start, from)
	}
	if last := segments[len(segments)-1]; !last.End.Equal(to) {
		t.Errorf("last segment ends at %s, want %s", last.End, to)
	}
	for i := 1; i < len(segments); i++ {
		if !segments[i].Start.Equal(segments[i-1].End) {
			t.Errorf("segment %d starts at %s but previous ended at %s",
				i, segments[i].Start, segments[i-1].End)
		}
	}
	// Two days, each covered 09:00–17:00 by a different participant.
	var covered []scheduleSegment
	for _, seg := range segments {
		if len(seg.UserIDs) > 0 {
			covered = append(covered, seg)
		}
	}
	if len(covered) != 2 {
		t.Fatalf("covered segments = %d, want 2 (%v)", len(covered), segments)
	}
	if covered[0].UserIDs[0] != "u_a" || covered[1].UserIDs[0] != "u_b" {
		t.Errorf("covered participants = %v, %v, want u_a, u_b", covered[0].UserIDs, covered[1].UserIDs)
	}
	for _, seg := range covered {
		if got := seg.End.Sub(seg.Start); got != 8*time.Hour {
			t.Errorf("covered segment length = %v, want 8h", got)
		}
	}
	if gaps := scheduleGaps(sched, from, to, nil); len(gaps) != 3 {
		t.Errorf("gaps = %d, want 3 (nights either side of each window)", len(gaps))
	}
}

func TestTimelineFullyCoveredRotationHasNoGaps(t *testing.T) {
	sched := weeklyRotation("u_a", "u_b")
	from := mustTime(t, "2026-03-10T00:00:00Z")
	if gaps := scheduleGaps(sched, from, from.Add(defaultPreviewWindow), nil); len(gaps) != 0 {
		t.Errorf("gaps = %v, want none", gaps)
	}
}

func TestOverlappingShiftsAreReported(t *testing.T) {
	sched := map[string]any{
		"timezone": "UTC",
		"shifts": []any{
			map[string]any{"id": "s1", "user_id": "u_a", "start_at": "2026-06-01T00:00:00Z", "end_at": "2026-06-02T00:00:00Z"},
			map[string]any{"id": "s2", "user_id": "u_b", "start_at": "2026-06-01T12:00:00Z", "end_at": "2026-06-02T12:00:00Z"},
		},
	}
	segments := scheduleTimeline(sched, mustTime(t, "2026-06-01T00:00:00Z"), mustTime(t, "2026-06-02T12:00:00Z"))
	var overlapped int
	for _, seg := range segments {
		if len(seg.UserIDs) > 1 {
			overlapped++
		}
	}
	if overlapped != 1 {
		t.Errorf("overlapping segments = %d, want 1 (%v)", overlapped, segments)
	}
}

// --- CRUD-level tests ---

func scheduleEngineWithUsers(ids ...string) (*Engine, *memStore) {
	ms := newMemStore()
	for _, id := range ids {
		ms.seed("users", map[string]any{"id": id, "name": id})
	}
	return crudEngine(ms), ms
}

func TestCreateScheduleStoresRotation(t *testing.T) {
	e, _ := scheduleEngineWithUsers("u_a", "u_b")
	res, err := e.CreateSchedule(context.Background(), map[string]any{
		"name":     "Primary",
		"timezone": "Europe/Berlin",
		"rotation": map[string]any{
			"start_at":         "2026-06-01T09:00:00Z",
			"handoff_interval": 1,
			"handoff_unit":     "weeks",
			"participant_ids":  []any{"u_a", "u_b"},
			"restriction":      map[string]any{"start": "9:00", "end": "18:00", "days": []any{"Monday", "tue"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	rot, ok := res["rotation"].(map[string]any)
	if !ok {
		t.Fatalf("rotation not stored: %#v", res["rotation"])
	}
	if rot["handoff_unit"] != "weeks" || rot["enabled"] != true {
		t.Errorf("rotation = %#v", rot)
	}
	res2 := rot["restriction"].(map[string]any)
	if res2["start"] != "09:00" || res2["end"] != "18:00" {
		t.Errorf("restriction not normalised: %#v", res2)
	}
	if days := res2["days"].([]any); len(days) != 2 || days[0] != "mon" || days[1] != "tue" {
		t.Errorf("restriction days = %v, want [mon tue]", days)
	}
	if res["enabled"] != true {
		t.Errorf("enabled default = %v, want true", res["enabled"])
	}
}

func TestCreateScheduleRejectsBadRotation(t *testing.T) {
	e, _ := scheduleEngineWithUsers("u_a")
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"unknown timezone", map[string]any{"name": "s", "timezone": "Mars/Olympus"}},
		{"unknown unit", map[string]any{"name": "s", "rotation": map[string]any{
			"start_at": "2026-06-01T00:00:00Z", "handoff_unit": "fortnights", "participant_ids": []any{"u_a"}}}},
		{"missing start", map[string]any{"name": "s", "rotation": map[string]any{
			"handoff_unit": "days", "participant_ids": []any{"u_a"}}}},
		{"unknown participant", map[string]any{"name": "s", "rotation": map[string]any{
			"start_at": "2026-06-01T00:00:00Z", "handoff_unit": "days", "participant_ids": []any{"u_ghost"}}}},
		{"bad restriction", map[string]any{"name": "s", "rotation": map[string]any{
			"start_at": "2026-06-01T00:00:00Z", "handoff_unit": "days", "participant_ids": []any{"u_a"},
			"restriction": map[string]any{"start": "nine", "end": "18:00"}}}},
	}
	for _, tc := range cases {
		if _, err := e.CreateSchedule(context.Background(), tc.payload); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestOverrideLifecycle(t *testing.T) {
	ctx := context.Background()
	e, _ := scheduleEngineWithUsers("u_a", "u_b")
	sched, err := e.CreateSchedule(ctx, map[string]any{"name": "Primary"})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	schedID := sched["id"].(string)

	created, err := e.CreateScheduleOverride(ctx, schedID, map[string]any{
		"user_id":  "u_a",
		"start_at": "2026-06-01T00:00:00Z",
		"until":    "2026-06-02T00:00:00Z",
		"reason":   "conference",
	})
	if err != nil {
		t.Fatalf("CreateScheduleOverride: %v", err)
	}
	override := created["override"].(map[string]any)
	if override["reason"] != "conference" {
		t.Errorf("reason = %v, want conference", override["reason"])
	}
	overrideID := override["id"].(string)

	updated, err := e.UpdateScheduleOverride(ctx, schedID, overrideID, map[string]any{
		"user_id": "u_b",
		"until":   "2026-06-03T00:00:00Z",
		"reason":  "extended",
	})
	if err != nil {
		t.Fatalf("UpdateScheduleOverride: %v", err)
	}
	upd := updated["override"].(map[string]any)
	if upd["user_id"] != "u_b" || upd["reason"] != "extended" {
		t.Errorf("override after update = %#v", upd)
	}
	if upd["updated_at"] == nil {
		t.Error("updated_at not set")
	}

	if _, err := e.UpdateScheduleOverride(ctx, schedID, overrideID, map[string]any{
		"until": "2026-05-01T00:00:00Z",
	}); err == nil {
		t.Error("expected error when until precedes start_at")
	}

	after, err := e.DeleteScheduleOverride(ctx, schedID, overrideID)
	if err != nil {
		t.Fatalf("DeleteScheduleOverride: %v", err)
	}
	if len(anyList(after["overrides"])) != 0 {
		t.Errorf("overrides after delete = %v", after["overrides"])
	}
	if _, err := e.DeleteScheduleOverride(ctx, schedID, overrideID); err == nil {
		t.Error("expected not-found on second delete")
	}
}

func TestPreviewSchedule(t *testing.T) {
	ctx := context.Background()
	e, _ := scheduleEngineWithUsers("u_a", "u_b")
	sched, err := e.CreateSchedule(ctx, map[string]any{
		"name": "Primary",
		"rotation": map[string]any{
			"start_at":         utils.ToISO(utils.UTCNow().Add(-time.Hour)),
			"handoff_interval": 12,
			"handoff_unit":     "hours",
			"participant_ids":  []any{"u_a", "u_b"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	preview, err := e.PreviewSchedule(ctx, sched["id"].(string), "", "")
	if err != nil {
		t.Fatalf("PreviewSchedule: %v", err)
	}
	if got := len(preview["gaps"].([]any)); got != 0 {
		t.Errorf("gaps = %d, want 0", got)
	}
	if ratio := preview["coverage_ratio"].(float64); ratio < 0.999 {
		t.Errorf("coverage_ratio = %v, want ~1", ratio)
	}
	// Four weeks at a 12-hour handoff is 56 segments; allow the partial first one.
	if segments := len(preview["segments"].([]any)); segments < 55 || segments > 58 {
		t.Errorf("segments = %d, want ~56", segments)
	}
	if len(preview["participants"].([]any)) != 2 {
		t.Errorf("participants = %v", preview["participants"])
	}
}

func TestPreviewScheduleReportsGapsAndWarnings(t *testing.T) {
	ctx := context.Background()
	e, _ := scheduleEngineWithUsers("u_a")
	sched, err := e.CreateSchedule(ctx, map[string]any{"name": "Empty"})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	preview, err := e.PreviewSchedule(ctx, sched["id"].(string), "", "")
	if err != nil {
		t.Fatalf("PreviewSchedule: %v", err)
	}
	if len(preview["gaps"].([]any)) == 0 {
		t.Error("empty schedule reported no gaps")
	}
	if len(preview["warnings"].([]any)) == 0 {
		t.Error("empty schedule reported no warnings")
	}
}

func TestChainRejectsUncoveredScheduleUnlessAcknowledged(t *testing.T) {
	ctx := context.Background()
	e, _ := scheduleEngineWithUsers("u_a")
	empty, err := e.CreateSchedule(ctx, map[string]any{"name": "Empty"})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	emptyID := empty["id"].(string)

	_, err = e.CreateEscalationChain(ctx, map[string]any{
		"name":  "chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_SCHEDULE", "schedule_id": emptyID}},
	})
	if err == nil {
		t.Fatal("expected coverage error when attaching an uncovered schedule")
	}

	chain, err := e.CreateEscalationChain(ctx, map[string]any{
		"name": "chain",
		"steps": []any{map[string]any{
			"kind": "NOTIFY_SCHEDULE", "schedule_id": emptyID, "allow_uncovered": true,
		}},
	})
	if err != nil {
		t.Fatalf("acknowledged attach failed: %v", err)
	}
	step := anyList(chain["steps"])[0].(map[string]any)
	if step["allow_uncovered"] != true {
		t.Errorf("allow_uncovered not persisted: %#v", step)
	}

	covered, err := e.CreateSchedule(ctx, map[string]any{
		"name": "Covered",
		"rotation": map[string]any{
			"start_at":         utils.ToISO(utils.UTCNow().Add(-time.Hour)),
			"handoff_interval": 1,
			"handoff_unit":     "days",
			"participant_ids":  []any{"u_a"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if _, err := e.CreateEscalationChain(ctx, map[string]any{
		"name":  "chain2",
		"steps": []any{map[string]any{"kind": "NOTIFY_SCHEDULE", "schedule_id": covered["id"]}},
	}); err != nil {
		t.Fatalf("covered schedule rejected: %v", err)
	}
}

func TestGetScheduleOncallReportsSourceAndNext(t *testing.T) {
	ctx := context.Background()
	e, _ := scheduleEngineWithUsers("u_a", "u_b")
	sched, err := e.CreateSchedule(ctx, map[string]any{
		"name": "Primary",
		"rotation": map[string]any{
			"start_at":         utils.ToISO(utils.UTCNow().Add(-time.Hour)),
			"handoff_interval": 6,
			"handoff_unit":     "hours",
			"participant_ids":  []any{"u_a", "u_b"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	oncall, err := e.GetScheduleOncall(ctx, sched["id"].(string), "")
	if err != nil {
		t.Fatalf("GetScheduleOncall: %v", err)
	}
	if oncall["source"] != sourceRotation {
		t.Errorf("source = %v, want rotation", oncall["source"])
	}
	ids := oncall["user_ids"].([]string)
	if len(ids) != 1 || ids[0] != "u_a" {
		t.Errorf("user_ids = %v, want [u_a]", ids)
	}
	next, ok := oncall["next"].(map[string]any)
	if !ok {
		t.Fatalf("next missing: %#v", oncall)
	}
	if got := next["user_ids"].([]any); len(got) != 1 || got[0] != "u_b" {
		t.Errorf("next user_ids = %v, want [u_b]", got)
	}
}

// --- fixes verified: roster, overrides, coverage report, shift notifications ---

func TestLatestOverrideWinsOnOverlap(t *testing.T) {
	sched := weeklyRotation("u_a")
	sched["overrides"] = []any{
		map[string]any{
			"id": "ovr_old", "user_id": "u_old",
			"start_at": "2026-03-03T00:00:00Z", "until": "2026-03-05T00:00:00Z",
			"created_at": "2026-03-01T00:00:00Z",
		},
		map[string]any{
			"id": "ovr_new", "user_id": "u_new",
			"start_at": "2026-03-04T00:00:00Z", "until": "2026-03-06T00:00:00Z",
			"created_at": "2026-03-02T00:00:00Z",
		},
	}
	// Inside the overlap the later decision wins.
	got, _ := resolveScheduleAt(sched, mustTime(t, "2026-03-04T12:00:00Z"))
	if len(got) != 1 || got[0] != "u_new" {
		t.Errorf("overlap: got %v, want [u_new]", got)
	}
	// Outside it, each override still covers its own window.
	got, _ = resolveScheduleAt(sched, mustTime(t, "2026-03-03T12:00:00Z"))
	if len(got) != 1 || got[0] != "u_old" {
		t.Errorf("before overlap: got %v, want [u_old]", got)
	}
	got, _ = resolveScheduleAt(sched, mustTime(t, "2026-03-05T12:00:00Z"))
	if len(got) != 1 || got[0] != "u_new" {
		t.Errorf("after overlap: got %v, want [u_new]", got)
	}
}

func TestDeletedParticipantCountsAsGap(t *testing.T) {
	sched := weeklyRotation("u_a", "u_ghost")
	from := mustTime(t, "2026-03-02T10:00:00Z")
	to := from.Add(14 * 24 * time.Hour)

	if gaps := scheduleGaps(sched, from, to, nil); len(gaps) != 0 {
		t.Fatalf("without a roster the timeline must be unchanged, got %d gaps", len(gaps))
	}
	known := map[string]bool{"u_a": true}
	gaps := scheduleGaps(sched, from, to, known)
	if len(gaps) != 1 {
		t.Fatalf("gaps = %d, want 1 (the ghost's week)", len(gaps))
	}
	if got := gaps[0].End.Sub(gaps[0].Start); got != 7*24*time.Hour {
		t.Errorf("ghost gap = %v, want 168h", got)
	}
}

func TestPreviewReportsUnknownParticipants(t *testing.T) {
	ctx := context.Background()
	e, ms := scheduleEngineWithUsers("u_a", "u_b")
	sched, err := e.CreateSchedule(ctx, map[string]any{
		"name": "Primary",
		"rotation": map[string]any{
			"start_at":         utils.ToISO(utils.UTCNow().Add(-time.Hour)),
			"handoff_interval": 1,
			"handoff_unit":     "days",
			"participant_ids":  []any{"u_a", "u_b"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	// Delete u_b behind the engine's back: the row is gone, the schedule still
	// names them. This is the state a plain DELETE used to leave behind.
	ms.mu.Lock()
	delete(ms.data["users"], "u_b")
	ms.mu.Unlock()

	preview, err := e.PreviewSchedule(ctx, sched["id"].(string), "", "")
	if err != nil {
		t.Fatalf("PreviewSchedule: %v", err)
	}
	unknown := preview["unknown_users"].([]any)
	if len(unknown) != 1 || unknown[0] != "u_b" {
		t.Fatalf("unknown_users = %v, want [u_b]", unknown)
	}
	if len(preview["gaps"].([]any)) == 0 {
		t.Error("a rotation naming a deleted user reported no gaps")
	}
	if ratio := preview["coverage_ratio"].(float64); ratio > 0.6 {
		t.Errorf("coverage_ratio = %v, want ~0.5 with half the rotation missing", ratio)
	}
	if len(preview["warnings"].([]any)) == 0 {
		t.Error("no warning about the missing participant")
	}
}

func TestDeleteUserRemovesThemFromSchedules(t *testing.T) {
	ctx := context.Background()
	e, _ := scheduleEngineWithUsers("u_a", "u_b")
	sched, err := e.CreateSchedule(ctx, map[string]any{
		"name": "Primary",
		"rotation": map[string]any{
			"start_at":         utils.ToISO(utils.UTCNow().Add(-time.Hour)),
			"handoff_interval": 1,
			"handoff_unit":     "days",
			"participant_ids":  []any{"u_a", "u_b"},
		},
		"shifts": []any{map[string]any{
			"user_id":  "u_b",
			"start_at": "2026-06-01T00:00:00Z",
			"end_at":   "2026-06-02T00:00:00Z",
		}},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	schedID := sched["id"].(string)
	if _, err := e.CreateScheduleOverride(ctx, schedID, map[string]any{
		"user_id": "u_b", "until": utils.ToISO(utils.UTCNow().Add(time.Hour)),
	}); err != nil {
		t.Fatalf("CreateScheduleOverride: %v", err)
	}

	if _, err := e.DeleteEntity(ctx, "users", "u_b"); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}

	after, err := e.GetItem(ctx, "schedules", schedID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	rot := after["rotation"].(map[string]any)
	ids, _ := utils.CoerceStringList(rot["participant_ids"])
	if len(ids) != 1 || ids[0] != "u_a" {
		t.Errorf("participant_ids = %v, want [u_a]", ids)
	}
	if len(anyList(after["shifts"])) != 0 {
		t.Errorf("shifts = %v, want none", after["shifts"])
	}
	if len(anyList(after["overrides"])) != 0 {
		t.Errorf("overrides = %v, want none", after["overrides"])
	}
}

func TestScheduleCoverageReportSeparatesAcknowledged(t *testing.T) {
	ctx := context.Background()
	e, _ := scheduleEngineWithUsers("u_a")

	empty, err := e.CreateSchedule(ctx, map[string]any{"name": "Empty"})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	covered, err := e.CreateSchedule(ctx, map[string]any{
		"name": "Covered",
		"rotation": map[string]any{
			"start_at":         utils.ToISO(utils.UTCNow().Add(-time.Hour)),
			"handoff_interval": 1,
			"handoff_unit":     "days",
			"participant_ids":  []any{"u_a"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	// An unattached empty schedule is degraded but not an incident.
	report, err := e.ScheduleCoverageReport(ctx)
	if err != nil {
		t.Fatalf("ScheduleCoverageReport: %v", err)
	}
	if report["schedules_degraded"].(int) != 1 || report["degraded_attached"].(int) != 0 {
		t.Fatalf("unattached: degraded=%v attached=%v", report["schedules_degraded"], report["degraded_attached"])
	}

	if _, err := e.CreateEscalationChain(ctx, map[string]any{
		"name": "acknowledged",
		"steps": []any{map[string]any{
			"kind": "NOTIFY_SCHEDULE", "schedule_id": empty["id"], "allow_uncovered": true,
		}},
	}); err != nil {
		t.Fatalf("CreateEscalationChain: %v", err)
	}
	report, _ = e.ScheduleCoverageReport(ctx)
	if report["degraded_attached"].(int) != 0 {
		t.Errorf("an acknowledged gap must not count as degraded: %v", report["degraded_attached"])
	}

	// Now break a schedule that a chain depends on without acknowledgement:
	// the write-time gate passed, the standing check must still catch it.
	if _, err := e.CreateEscalationChain(ctx, map[string]any{
		"name":  "live",
		"steps": []any{map[string]any{"kind": "NOTIFY_SCHEDULE", "schedule_id": covered["id"]}},
	}); err != nil {
		t.Fatalf("CreateEscalationChain: %v", err)
	}
	if _, err := e.UpdateSchedule(ctx, covered["id"].(string), map[string]any{"enabled": false}); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	report, _ = e.ScheduleCoverageReport(ctx)
	if report["degraded_attached"].(int) != 1 {
		t.Errorf("degraded_attached = %v, want 1 after the schedule was disabled", report["degraded_attached"])
	}
	for _, raw := range report["items"].([]any) {
		item := raw.(map[string]any)
		if item["schedule_id"] == covered["id"] && item["disabled"] != true {
			t.Errorf("disabled schedule not reported as disabled: %#v", item)
		}
	}
}

func TestPreviewCoversLongWindowWithHourlyHandoff(t *testing.T) {
	sched := map[string]any{
		"timezone": "UTC",
		"rotation": map[string]any{
			"enabled":          true,
			"start_at":         "2026-06-01T00:00:00Z",
			"handoff_interval": 1,
			"handoff_unit":     "hours",
			"participant_ids":  []any{"u_a", "u_b"},
		},
	}
	from := mustTime(t, "2026-06-01T00:00:00Z")
	to := from.Add(maxPreviewWindow)
	segments := scheduleTimeline(sched, from, to)
	// One segment per hour over the whole window: the boundary walk must not
	// stop early and merge the tail into one wrong segment.
	if want := int(maxPreviewWindow.Hours()); len(segments) != want {
		t.Fatalf("segments = %d, want %d", len(segments), want)
	}
	last := segments[len(segments)-1]
	if got := last.End.Sub(last.Start); got != time.Hour {
		t.Errorf("last segment = %v, want 1h (silent truncation)", got)
	}
}

func TestShiftNotificationsOptInBaselineAndHandoff(t *testing.T) {
	ctx := context.Background()
	ms := newMemStore()
	ms.seed("users",
		map[string]any{"id": "u_a", "name": "A", "username": "a",
			"notification_targets": []any{map[string]any{"type": "telegram", "target": "111"}}},
		map[string]any{"id": "u_b", "name": "B", "username": "b",
			"notification_targets": []any{map[string]any{"type": "telegram", "target": "222"}}},
	)
	e := crudEngine(ms)
	start := utils.UTCNow().Add(-30 * time.Minute)

	// Opt-out by default: a schedule without the flag never notifies.
	quiet, err := e.CreateSchedule(ctx, map[string]any{
		"name": "Quiet",
		"rotation": map[string]any{
			"start_at": utils.ToISO(start), "handoff_interval": 1, "handoff_unit": "hours",
			"participant_ids": []any{"u_a", "u_b"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if n, err := e.ProcessScheduleShiftNotifications(ctx); err != nil || n != 0 {
		t.Fatalf("quiet schedule: n=%d err=%v, want 0", n, err)
	}
	if s := ms.row("schedules", quiet["id"].(string)); s["shift_notification"] != nil {
		t.Errorf("opt-out schedule got shift state: %#v", s["shift_notification"])
	}

	sched, err := e.CreateSchedule(ctx, map[string]any{
		"name":                   "Loud",
		"notify_on_shift_change": true,
		"rotation": map[string]any{
			"start_at": utils.ToISO(start), "handoff_interval": 1, "handoff_unit": "hours",
			"participant_ids": []any{"u_a", "u_b"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	schedID := sched["id"].(string)

	// First cycle records the baseline silently — enabling the flag must not
	// page whoever is on call right now.
	n, err := e.ProcessScheduleShiftNotifications(ctx)
	if err != nil || n != 0 {
		t.Fatalf("baseline cycle: n=%d err=%v, want 0", n, err)
	}
	stored := ms.row("schedules", schedID)
	if stored["shift_notification"] == nil {
		t.Fatal("baseline not recorded")
	}
	if ms.count("notifications") != 0 {
		t.Fatalf("baseline created %d notifications", ms.count("notifications"))
	}

	// Rewind the rotation so the next cycle sees a handoff to u_b.
	ms.mu.Lock()
	row := ms.data["schedules"][schedID]
	rot := row["rotation"].(map[string]any)
	rot["start_at"] = utils.ToISO(utils.UTCNow().Add(-90 * time.Minute))
	ms.mu.Unlock()

	n, err = e.ProcessScheduleShiftNotifications(ctx)
	if err != nil {
		t.Fatalf("handoff cycle: %v", err)
	}
	if n != 1 {
		t.Fatalf("handoff notifications = %d, want 1", n)
	}
	var found map[string]any
	ms.mu.Lock()
	for _, row := range ms.data["notifications"] {
		found = row
	}
	ms.mu.Unlock()
	if utils.StrVal(found, "user_id") != "u_b" {
		t.Errorf("notified user = %v, want u_b", found["user_id"])
	}
	if utils.StrVal(found, "channel") != "telegram" || utils.StrVal(found, "target") != "222" {
		t.Errorf("notification channel/target = %#v", found)
	}
	if utils.StrVal(found, "reason") != shiftNotificationReason {
		t.Errorf("reason = %v", found["reason"])
	}
	if utils.StrVal(found, "alert_group_id") != "" {
		t.Errorf("shift notification must not be attached to a group: %v", found["alert_group_id"])
	}

	// A second cycle with no further change sends nothing.
	if n, err := e.ProcessScheduleShiftNotifications(ctx); err != nil || n != 0 {
		t.Fatalf("stable cycle: n=%d err=%v, want 0", n, err)
	}
}

// A user on call through a rotation is a recipient of the group, so the mobile
// dashboard must treat it as theirs. Before Schedule v2 this check only looked
// at legacy shifts, which a rotation-only schedule does not have.
func TestGroupRelevanceFollowsRotation(t *testing.T) {
	state := store.NewState()
	state.Schedules["sch_1"] = weeklyRotation("u_a", "u_b")
	state.EscalationChains["esc_1"] = map[string]any{
		"id": "esc_1",
		"steps": []any{map[string]any{
			"kind": StepNotifySchedule, "schedule_id": "sch_1",
		}},
	}
	group := map[string]any{"id": "grp_1", "escalation_chain_id": "esc_1"}
	at := mustTime(t, "2026-03-03T12:00:00Z") // first rotation week: u_a

	if !groupIsRelevantToUser(state, group, "u_a", map[string]bool{}, at) {
		t.Error("user on call via rotation not considered relevant")
	}
	if groupIsRelevantToUser(state, group, "u_b", map[string]bool{}, at) {
		t.Error("user off shift considered relevant")
	}
	// Next week the pager moves, and so does relevance.
	next := mustTime(t, "2026-03-10T12:00:00Z")
	if !groupIsRelevantToUser(state, group, "u_b", map[string]bool{}, next) {
		t.Error("relevance did not follow the handoff")
	}
}

// --- second-revision fixes: scoping and per-cycle cost ---

// A scoped reader must not learn about another team's schedules through the
// coverage report, the preview or the on-call endpoint — all three name the
// schedule, and the report also names the chains that page through it.
func TestScheduleReadsRespectTeamScoping(t *testing.T) {
	ctx := context.Background()
	e, ms := scheduleEngineWithUsers("u_a")
	ms.seed("teams",
		map[string]any{"id": "team_mine", "name": "Mine"},
		map[string]any{"id": "team_other", "name": "Other"},
	)
	mine, err := e.CreateSchedule(ctx, map[string]any{"name": "Mine", "team_id": "team_mine"})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	other, err := e.CreateSchedule(ctx, map[string]any{"name": "Other", "team_id": "team_other"})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	scoped := authz.NewContext(ctx, authz.Actor{
		ID: "u_a", Kind: authz.KindUser, Role: authz.RoleEditor,
		TeamIDs: []string{"team_mine"}, TeamScoped: true,
	})

	if _, err := e.PreviewSchedule(scoped, utils.StrVal(other, "id"), "", ""); err == nil {
		t.Error("preview of another team's schedule was allowed")
	}
	if _, err := e.GetScheduleOncall(scoped, utils.StrVal(other, "id"), ""); err == nil {
		t.Error("on-call of another team's schedule was allowed")
	}
	if _, err := e.PreviewSchedule(scoped, utils.StrVal(mine, "id"), "", ""); err != nil {
		t.Errorf("preview of own team's schedule refused: %v", err)
	}

	report, err := e.ScheduleCoverageReport(scoped)
	if err != nil {
		t.Fatalf("ScheduleCoverageReport: %v", err)
	}
	// Both schedules are empty, so both are degraded — but only one is visible.
	for _, raw := range report["items"].([]any) {
		if raw.(map[string]any)["schedule_id"] == other["id"] {
			t.Errorf("coverage report leaked another team's schedule: %#v", raw)
		}
	}
	if len(report["items"].([]any)) != 1 {
		t.Errorf("visible items = %d, want 1", len(report["items"].([]any)))
	}
	// An unscoped actor still sees everything, which is what the worker relies on.
	report, _ = e.ScheduleCoverageReport(ctx)
	if len(report["items"].([]any)) != 2 {
		t.Errorf("unscoped items = %d, want 2", len(report["items"].([]any)))
	}
}

// The shift-notification step runs on every worker cycle, so when no schedule
// opted in it must not open a transaction or read the notifications table at
// all. This is a cost assertion, not a behaviour one: the previous version
// loaded every notification row every cycle regardless.
func TestShiftNotificationsCostNothingWhenDisabled(t *testing.T) {
	ctx := context.Background()
	ms := newMemStore()
	ms.seed("users", map[string]any{"id": "u_a", "name": "A", "username": "a"})
	e := crudEngine(ms)
	if _, err := e.CreateSchedule(ctx, map[string]any{
		"name": "Quiet",
		"rotation": map[string]any{
			"start_at":         utils.ToISO(utils.UTCNow().Add(-time.Hour)),
			"handoff_interval": 1, "handoff_unit": "hours",
			"participant_ids": []any{"u_a"},
		},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	ms.resetLoadLog()
	if n, err := e.ProcessScheduleShiftNotifications(ctx); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, want 0", n, err)
	}
	if loaded := ms.loadedCollections(); len(loaded) != 0 {
		t.Errorf("opened a transaction loading %v; want none", loaded)
	}
}

// And when a handoff does happen, it loads only the rows it needs — never the
// notifications table, whose size is unbounded.
func TestShiftNotificationsLoadOnlyInvolvedRows(t *testing.T) {
	ctx := context.Background()
	ms := newMemStore()
	ms.seed("users",
		map[string]any{"id": "u_a", "name": "A", "username": "a",
			"notification_targets": []any{map[string]any{"type": "telegram", "target": "111"}}},
		map[string]any{"id": "u_b", "name": "B", "username": "b",
			"notification_targets": []any{map[string]any{"type": "telegram", "target": "222"}}},
	)
	e := crudEngine(ms)
	sched, err := e.CreateSchedule(ctx, map[string]any{
		"name":                   "Loud",
		"notify_on_shift_change": true,
		"rotation": map[string]any{
			"start_at":         utils.ToISO(utils.UTCNow().Add(-30 * time.Minute)),
			"handoff_interval": 1, "handoff_unit": "hours",
			"participant_ids": []any{"u_a", "u_b"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if _, err := e.ProcessScheduleShiftNotifications(ctx); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	ms.mu.Lock()
	ms.data["schedules"][sched["id"].(string)]["rotation"].(map[string]any)["start_at"] =
		utils.ToISO(utils.UTCNow().Add(-90 * time.Minute))
	ms.mu.Unlock()

	ms.resetLoadLog()
	if n, err := e.ProcessScheduleShiftNotifications(ctx); err != nil || n != 1 {
		t.Fatalf("handoff: n=%d err=%v, want 1", n, err)
	}
	for _, spec := range ms.loadSpecs() {
		if spec.Collection == "notifications" {
			t.Error("loaded the notifications collection")
		}
		if spec.Filters["id"] == nil {
			t.Errorf("loaded %s unfiltered", spec.Collection)
		}
	}
}

// --- third-revision fixes: stale cache and roster movement ---

// cachedEngine wires the reference cache and its write-invalidating wrapper,
// the way engine.New does. The plain crudEngine has no cache, so it cannot
// exercise the pre-pass reading a stale copy.
func cachedEngine(ms *memStore) (*Engine, *refCache) {
	cache := newRefCache()
	return &Engine{
		store:    refInvalidatingStore{PostgreSQLStore: ms, cache: cache},
		refCache: cache,
	}, cache
}

func shiftEngineFixture(t *testing.T) (*Engine, *refCache, *memStore, string) {
	t.Helper()
	ctx := context.Background()
	ms := newMemStore()
	ms.seed("users",
		map[string]any{"id": "u_a", "name": "A", "username": "a",
			"notification_targets": []any{map[string]any{"type": "telegram", "target": "111"}}},
		map[string]any{"id": "u_b", "name": "B", "username": "b",
			"notification_targets": []any{map[string]any{"type": "telegram", "target": "222"}}},
		map[string]any{"id": "u_c", "name": "C", "username": "c",
			"notification_targets": []any{map[string]any{"type": "telegram", "target": "333"}}},
	)
	e, cache := cachedEngine(ms)
	sched, err := e.CreateSchedule(ctx, map[string]any{
		"name":                   "Loud",
		"notify_on_shift_change": true,
		"rotation": map[string]any{
			"start_at":         utils.ToISO(utils.UTCNow().Add(-30 * time.Minute)),
			"handoff_interval": 1,
			"handoff_unit":     "hours",
			"participant_ids":  []any{"u_a", "u_b"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if _, err := e.ProcessScheduleShiftNotifications(ctx); err != nil {
		t.Fatalf("baseline cycle: %v", err)
	}
	return e, cache, ms, sched["id"].(string)
}

// rewindRotation moves the rotation start back so the next evaluation lands in
// the next participant's slot.
func rewindRotation(ms *memStore, schedID string, d time.Duration) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.data["schedules"][schedID]["rotation"].(map[string]any)["start_at"] =
		utils.ToISO(utils.UTCNow().Add(-d))
}

// A committed handoff must not be sent twice, even though the pre-pass reads a
// cache: the write invalidates it, and a copy that is stale anyway (another
// replica's cycle) is caught by the re-check under the lock.
func TestShiftNotificationsNotResentFromStaleCache(t *testing.T) {
	ctx := context.Background()
	e, cache, ms, schedID := shiftEngineFixture(t)

	// Warm the cache, then hand over.
	if _, _, err := e.pendingShiftHandoffs(ctx, utils.UTCNow()); err != nil {
		t.Fatalf("warm pre-pass: %v", err)
	}
	stale, ok := cache.get("schedules", time.Now())
	if !ok {
		t.Fatal("cache did not hold the schedules copy")
	}
	staleCopy := map[string]map[string]any{}
	for id, item := range stale {
		staleCopy[id] = deepCopyItem(item)
	}

	rewindRotation(ms, schedID, 90*time.Minute)
	cache.invalidate("schedules")
	if n, err := e.ProcessScheduleShiftNotifications(ctx); err != nil || n != 1 {
		t.Fatalf("handoff: n=%d err=%v, want 1", n, err)
	}
	if _, ok := cache.get("schedules", time.Now()); ok {
		t.Error("the write did not invalidate the cached schedules copy")
	}

	// Now replay another replica's situation: a cached copy from before the
	// handoff, while the database already has it recorded.
	cache.put("schedules", staleCopy, time.Now())
	before := ms.count("notifications")
	if n, err := e.ProcessScheduleShiftNotifications(ctx); err != nil || n != 0 {
		t.Fatalf("stale-cache cycle: n=%d err=%v, want 0", n, err)
	}
	if after := ms.count("notifications"); after != before {
		t.Errorf("stale cache produced %d duplicate notification(s)", after-before)
	}
}

// If the roster moves between the pre-pass and the lock, the person who came
// on call was never loaded. The step must defer rather than record the new
// roster — recording it would drop their notification permanently.
func TestShiftNotificationDeferredWhenRosterMovedSincePrePass(t *testing.T) {
	ctx := context.Background()
	e, cache, ms, schedID := shiftEngineFixture(t)

	// Cached copy still shows u_a/u_b; the stored schedule hands over to u_c,
	// who is therefore absent from the users the transaction asks for.
	if _, _, err := e.pendingShiftHandoffs(ctx, utils.UTCNow()); err != nil {
		t.Fatalf("pre-pass: %v", err)
	}
	cached, ok := cache.get("schedules", time.Now())
	if !ok {
		t.Fatal("no cached copy")
	}
	staleCopy := map[string]map[string]any{}
	for id, item := range cached {
		staleCopy[id] = deepCopyItem(item)
	}
	// Stale copy: rotation already handed over to u_b, so the pre-pass asks
	// only for u_b …
	staleCopy[schedID]["rotation"].(map[string]any)["start_at"] =
		utils.ToISO(utils.UTCNow().Add(-90 * time.Minute))
	cache.put("schedules", staleCopy, time.Now())
	// … while the stored schedule has since been rewritten to hand over to u_c.
	ms.mu.Lock()
	rot := ms.data["schedules"][schedID]["rotation"].(map[string]any)
	rot["participant_ids"] = []any{"u_a", "u_c"}
	rot["start_at"] = utils.ToISO(utils.UTCNow().Add(-90 * time.Minute))
	ms.mu.Unlock()

	n, err := e.ProcessScheduleShiftNotifications(ctx)
	if err != nil {
		t.Fatalf("deferred cycle: %v", err)
	}
	if n != 0 {
		t.Fatalf("sent %d notifications for an unloaded recipient", n)
	}
	stored := ms.row("schedules", schedID)
	roster, _ := storedShiftRoster(stored)
	if len(roster) != 1 || roster[0] != "u_a" {
		t.Fatalf("roster recorded despite the deferral: %v", roster)
	}

	// The next cycle delivers what was deferred without anyone clearing the
	// cache by hand: the deferred transaction still saved the schedules
	// collection, and that invalidates the cached copy. This asserts the path
	// self-heals in production, not just when a test helps it along.
	if n, err := e.ProcessScheduleShiftNotifications(ctx); err != nil || n != 1 {
		t.Fatalf("recovery cycle: n=%d err=%v, want 1", n, err)
	}
	var target string
	ms.mu.Lock()
	for _, row := range ms.data["notifications"] {
		target = utils.StrVal(row, "target")
	}
	ms.mu.Unlock()
	if target != "333" {
		t.Errorf("notified target = %q, want u_c's 333", target)
	}
}
