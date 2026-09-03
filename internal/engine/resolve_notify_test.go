package engine

import (
	"testing"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// The resolve notification is the message that says the incident is over. When
// it does not arrive, nothing looks wrong: the group is resolved, the timeline
// is complete, and the only trace is that the people who were woken were never
// told they could stop.

// resolvedNotifications returns the notifications created for a resolve.
func resolvedNotifications(ms *memStore) []map[string]any {
	var out []map[string]any
	for _, row := range ms.data["notifications"] {
		if utils.StrVal(row, "reason") == resolveNotificationReason {
			out = append(out, row)
		}
	}
	return out
}

// wokenGroup ingests an alert and records that the group woke usr-1.
func wokenGroup(t *testing.T, e *Engine, ms *memStore, title string) string {
	t.Helper()
	group := ingestOne(t, e, title)
	id := group["id"].(string)
	ms.data["alert_groups"][id]["notified_user_ids"] = []any{"usr-1"}
	return id
}

func TestResolvingTellsThePeopleTheGroupWoke(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	e.deliveryCfg.NotifyOnResolve = true
	id := wokenGroup(t, e, ms, "disk full")

	if _, err := e.ResolveGroup(adminCtx(), id); err != nil {
		t.Fatalf("ResolveGroup: %v", err)
	}

	// The mutator loads only alert_groups. Before the users and the integration
	// were injected, every recipient came back unknown, nothing was created,
	// and resolve_notified_at was set anyway — so the message was suppressed
	// permanently rather than retried.
	if got := len(resolvedNotifications(ms)); got == 0 {
		t.Fatal("resolving notified nobody")
	}
}

func TestBulkResolvingAlsoTellsThem(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	e.deliveryCfg.NotifyOnResolve = true
	first := wokenGroup(t, e, ms, "disk full")
	second := wokenGroup(t, e, ms, "cpu hot")

	if _, err := e.BulkResolveGroups(adminCtx(), []string{first, second}); err != nil {
		t.Fatalf("BulkResolveGroups: %v", err)
	}

	// Bulk resolve is how a storm gets closed, which is exactly when the people
	// who were woken most want to hear that it is over.
	if got := len(resolvedNotifications(ms)); got < 2 {
		t.Errorf("bulk resolve created %d resolve notification(s) for 2 groups", got)
	}
}

func TestResolveNotificationStaysOffUnlessAskedFor(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms) // NotifyOnResolve defaults to false
	id := wokenGroup(t, e, ms, "disk full")

	if _, err := e.ResolveGroup(adminCtx(), id); err != nil {
		t.Fatalf("ResolveGroup: %v", err)
	}

	// Telling everyone twice per incident is a way to teach people to mute the
	// channel, so this stays opt-in.
	if got := len(resolvedNotifications(ms)); got != 0 {
		t.Errorf("created %d resolve notification(s) with the feature off", got)
	}
}
