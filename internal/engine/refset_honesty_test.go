package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"time"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// The hot paths used to read their reference collections as
//
//	schedsMap, _ := e.refCollection(ctx, "schedules")
//
// A failed read yields a nil map, and nothing downstream can tell a nil map from
// an empty one. The escalation step then finds no schedule, writes "No active
// user found in current schedule window" into the group's timeline, advances
// current_step and clears next_run_at — consuming the step for good. A database
// blip is rendered as the considered judgement that nobody was on call: nobody
// is paged, no error is returned, no metric moves, and the incident timeline
// carries a plausible-looking explanation.
//
// These tests assert the opposite: a reference collection that could not be read
// is a failure, and the alert group is left exactly as it was so the next cycle
// can try again.

var errRefRead = errors.New("read tcp 10.0.0.5:5432: connection reset by peer")

// refFailStore fails ListCollection for one named collection and otherwise
// behaves like memStore. ListDueAlertGroups is overridden too because memStore
// stubs it to nil, and the escalation path needs a due group to act on.
type refFailStore struct {
	*memStore
	failCollection string
	dueGroups      []map[string]any
}

func (f *refFailStore) ListCollection(ctx context.Context, collection string) ([]map[string]any, error) {
	if collection == f.failCollection {
		return nil, errRefRead
	}
	return f.memStore.ListCollection(ctx, collection)
}

func (f *refFailStore) ListDueAlertGroups(context.Context, string) ([]map[string]any, error) {
	return f.dueGroups, nil
}

// notifyScheduleChain is an escalation chain whose first step pages whoever is
// on call — the step whose failure mode this file is about.
func notifyScheduleChain() map[string]any {
	return map[string]any{
		"id":   "chain1",
		"name": "page on-call",
		"steps": []any{
			// "kind", not "type": advanceGroupLocked switches on step["kind"], and
			// a step keyed the other way silently matches nothing — which would
			// make the schedule_gap assertion below vacuous.
			map[string]any{"id": "s1", "kind": StepNotifySchedule, "schedule_id": "sched1"},
		},
	}
}

func dueGroupRow(t *testing.T) map[string]any {
	t.Helper()
	g := model.NewAlertGroup(model.NewAlertGroupParams{
		IntegrationID:     "int1",
		EscalationChainID: "chain1",
		DedupeKey:         "dk1",
		Title:             "disk full",
		Severity:          "high",
		Timestamp:         "2026-05-10T10:00:00+00:00",
	})
	raw := g.Raw()
	raw["id"] = "grp1"
	// Due a minute ago, so the escalation path picks it up.
	raw["next_run_at"] = utils.ToISO(utils.UTCNow().Add(-time.Minute))
	return raw
}

func newRefFailEngine(t *testing.T, failCollection string) (*Engine, *refFailStore) {
	t.Helper()
	ms := newMemStore()
	ms.seed("integrations", map[string]any{
		"id": "int1", "key": "k1", "name": "int", "type": "webhook",
		"escalation_chain_id": "chain1",
	})
	ms.seed("escalation_chains", notifyScheduleChain())
	ms.seed("schedules", map[string]any{
		"id": "sched1", "name": "primary", "timezone": "UTC",
		"shifts": []any{}, "rotation": nil,
	})
	ms.seed("users", map[string]any{"id": "u1", "username": "alice", "on_duty": true})
	group := dueGroupRow(t)
	ms.seed("alert_groups", group)

	fs := &refFailStore{memStore: ms, failCollection: failCollection, dueGroups: []map[string]any{group}}
	e := &Engine{
		store:       fs,
		deliveryCfg: DeliveryConfig{MaxRetries: 3},
		metrics:     noopMetrics{},
	}
	return e, fs
}

func TestEscalationFailsLoudlyWhenReferenceReadFails(t *testing.T) {
	e, fs := newRefFailEngine(t, "schedules")

	_, err := e.processDueEscalationsOnce(context.Background())
	if err == nil {
		t.Fatal("a failed read of the schedules table was swallowed; escalation continued against no schedule at all")
	}
	if !errors.Is(err, errRefRead) {
		t.Errorf("the underlying failure was not preserved: %v", err)
	}
	if !strings.Contains(err.Error(), "schedules") {
		t.Errorf("the error does not name the collection that failed: %v", err)
	}

	// The group must be untouched: same step, still due, and above all no
	// timeline entry claiming the schedule was empty.
	row := fs.row("alert_groups", "grp1")
	if row == nil {
		t.Fatal("alert group disappeared")
	}
	if got := utils.IntVal(row, "current_step"); got != 0 {
		t.Errorf("escalation step was consumed despite the failure: current_step=%d", got)
	}
	if utils.StrVal(row, "next_run_at") == "" {
		t.Error("next_run_at was cleared, so the group will never be retried")
	}
	for _, entry := range logEventTypes(row) {
		if entry == "schedule_gap" {
			t.Error("a database failure was recorded as 'schedule_gap' — the timeline now says nobody was on call")
		}
	}
}

func TestEscalationFailsLoudlyForEachReferenceCollection(t *testing.T) {
	// Every collection the escalation path pre-loads, not just schedules: users
	// and chains fail the same way (a missing chain silently ends escalation, a
	// missing user list pages nobody).
	for _, col := range []string{
		"chatops_channels", "mobile_devices", "users", "teams",
		"schedules", "integrations", "escalation_chains",
	} {
		t.Run(col, func(t *testing.T) {
			e, _ := newRefFailEngine(t, col)
			if _, err := e.processDueEscalationsOnce(context.Background()); err == nil {
				t.Fatalf("a failed read of %q was swallowed", col)
			}
		})
	}
}

func TestIngestFailsLoudlyWhenReferenceReadFails(t *testing.T) {
	// Ingest resolves routing against the same reference data. Accepting an alert
	// while unable to read the escalation chains would file it with no route and
	// return 200 to the sender.
	for _, col := range []string{
		"chatops_channels", "mobile_devices", "users", "teams",
		"schedules", "escalation_chains",
	} {
		t.Run(col, func(t *testing.T) {
			e, _ := newRefFailEngine(t, col)
			integration, err := e.store.GetItem(context.Background(), "integrations", "int1")
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.ingestPrepared(context.Background(), integration, []*preparedAlert{})
			if err == nil {
				t.Fatalf("ingest swallowed a failed read of %q", col)
			}
			if !errors.Is(err, errRefRead) {
				t.Errorf("underlying failure not preserved: %v", err)
			}
		})
	}
}

func TestRefSetStopsAtFirstFailure(t *testing.T) {
	// The sticky-error contract: once a get fails, later gets return nil without
	// hitting the store, and Err names the collection that actually failed.
	e, _ := newRefFailEngine(t, "users")
	rs := e.newRefSet(context.Background())
	_ = rs.get("teams")
	_ = rs.get("users")
	after := rs.get("schedules")
	if after != nil {
		t.Error("a get after a failure returned data; callers would act on a partial set")
	}
	err := rs.Err()
	if err == nil {
		t.Fatal("Err() was nil after a failed get")
	}
	if !strings.Contains(err.Error(), `"users"`) {
		t.Errorf("Err() blamed the wrong collection: %v", err)
	}
}

func TestRefSetIsNilErrorWhenAllReadsSucceed(t *testing.T) {
	e, _ := newRefFailEngine(t, "nothing-fails")
	rs := e.newRefSet(context.Background())
	if got := rs.get("users"); got == nil {
		t.Error("a successful get returned nil")
	}
	if err := rs.Err(); err != nil {
		t.Errorf("Err() non-nil despite every read succeeding: %v", err)
	}
}

// logEventTypes extracts the event_type of each timeline entry on a group row.
func logEventTypes(row map[string]any) []string {
	raw, _ := row["logs"].([]any)
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if m, ok := entry.(map[string]any); ok {
			out = append(out, utils.StrVal(m, "event_type"))
		}
	}
	return out
}

var _ store.PostgreSQLStore = (*refFailStore)(nil)
