package engine

import (
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
)

// Every way a chain is walked again must page again. Each of these used to run
// the steps, log "Notified users" and create no notification, because the
// notification key did not tell the second pass from the first.

func countNotifications(s *store.State) int { return len(s.Notifications) }

func TestRepeatPagesTheSamePersonAgain(t *testing.T) {
	e := newEngine()
	s := newState([]any{
		map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{"u1"}},
		map[string]any{"kind": "REPEAT", "from_position": 0, "max_repeat_count": 1, "cooldown_minutes": 0},
	})
	g := model.WrapAlertGroup(newGroup(0, 0))

	e.advanceGroupLocked(s, g, "2026-05-10T10:00:00+00:00")
	if got := countNotifications(s); got != 1 {
		t.Fatalf("first pass: %d notifications, want 1", got)
	}
	// The worker picks the group up again at next_run_at.
	e.advanceGroupLocked(s, g, "2026-05-10T10:00:05+00:00")
	if got := countNotifications(s); got != 2 {
		t.Errorf("after REPEAT: %d notifications, want 2 — the repeat paged nobody", got)
	}
}

func TestReprocessingTheSameStepDoesNotPageTwice(t *testing.T) {
	e := newEngine()
	s := newState([]any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{"u1"}}})
	first := model.WrapAlertGroup(newGroup(0, 0))
	again := model.WrapAlertGroup(newGroup(0, 0))

	e.advanceGroupLocked(s, first, "2026-05-10T10:00:00+00:00")
	e.advanceGroupLocked(s, again, "2026-05-10T10:00:00+00:00")
	if got := countNotifications(s); got != 1 {
		t.Errorf("%d notifications for one step execution, want 1", got)
	}
}

func TestUnacknowledgeRunsTheChainAgain(t *testing.T) {
	e := newEngine()
	s := newState([]any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{"u1"}}})
	g := model.WrapAlertGroup(newGroup(0, 0))
	actor := authz.Actor{Kind: authz.KindUser, ID: "usr_x", DisplayName: "alice"}

	e.advanceGroupLocked(s, g, "2026-05-10T10:00:00+00:00")
	if err := g.Acknowledge("2026-05-10T10:01:00+00:00", "ack", actor); err != nil {
		t.Fatal(err)
	}
	if err := g.Unacknowledge("2026-05-10T10:02:00+00:00", actor); err != nil {
		t.Fatal(err)
	}
	if g.CurrentStep() != 0 {
		t.Errorf("current_step = %d after unacknowledge, want 0", g.CurrentStep())
	}
	e.advanceGroupLocked(s, g, "2026-05-10T10:02:00+00:00")
	if got := countNotifications(s); got != 2 {
		t.Errorf("%d notifications after unacknowledge, want 2 — the group was open and nobody was paged", got)
	}
	if !logsContainMessage(g.Raw(), "Alert group unacknowledged by alice") {
		t.Error("unacknowledge log does not name who took the acknowledgement back")
	}
}

func TestExpiredSilenceReturnsTheGroupAndPages(t *testing.T) {
	e := newEngine()
	s := newState([]any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{"u1"}}})
	g := model.WrapAlertGroup(newGroup(0, 0))
	e.advanceGroupLocked(s, g, "2026-05-10T10:00:00+00:00")

	until := "2026-05-10T11:00:00+00:00"
	if err := g.Silence("2026-05-10T10:01:00+00:00", until, "silenced", 59, authz.SystemActor); err != nil {
		t.Fatal(err)
	}
	if g.NextRunAt() != until {
		t.Fatalf("next_run_at = %q, want the end of the silence %q", g.NextRunAt(), until)
	}
	before := time.Date(2026, 5, 10, 10, 59, 0, 0, time.UTC)
	after := time.Date(2026, 5, 10, 11, 0, 0, 0, time.UTC)
	if g.SilenceExpired(before) {
		t.Error("silence reported expired before silenced_until")
	}
	if !g.SilenceExpired(after) {
		t.Fatal("silence not expired at silenced_until")
	}
	g.EndSilence("2026-05-10T11:00:00+00:00")
	if g.Status() != model.StatusOpen {
		t.Errorf("status = %s after the silence ended, want open", g.Status())
	}
	e.advanceGroupLocked(s, g, "2026-05-10T11:00:00+00:00")
	if got := countNotifications(s); got != 2 {
		t.Errorf("%d notifications after the silence ended, want 2", got)
	}
}

func TestIndefiniteSilenceNeverExpires(t *testing.T) {
	g := model.WrapAlertGroup(newGroup(0, 0))
	if err := g.Silence("2026-05-10T10:00:00+00:00", "", "silenced", 0, authz.SystemActor); err != nil {
		t.Fatal(err)
	}
	if g.NextRunAt() != "" {
		t.Errorf("next_run_at = %q for an indefinite silence, want none", g.NextRunAt())
	}
	if g.SilenceExpired(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("indefinite silence reported expired")
	}
}

func logsContainMessage(group map[string]any, message string) bool {
	logs, _ := group["logs"].([]any)
	for _, l := range logs {
		if entry, ok := l.(map[string]any); ok && entry["message"] == message {
			return true
		}
	}
	return false
}
