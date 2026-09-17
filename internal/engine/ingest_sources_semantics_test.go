package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

func groupsOf(ms *memStore) []map[string]any {
	var out []map[string]any
	for _, g := range ms.data["alert_groups"] {
		out = append(out, g)
	}
	return out
}

func openGroups(ms *memStore) []map[string]any {
	var out []map[string]any
	for _, g := range groupsOf(ms) {
		if utils.StrVal(g, "status") == "open" {
			out = append(out, g)
		}
	}
	return out
}

func TestAlertmanagerWithoutSeverityIsUnknownNotItsStatus(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	if _, err := e.IngestAlertmanager(context.Background(), "key-1", map[string]any{
		"status": "firing",
		"alerts": []any{map[string]any{"status": "firing", "fingerprint": "f1",
			"labels": map[string]any{"alertname": "NoSev"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := utils.StrVal(groupsOf(ms)[0], "severity"); got != "unknown" {
		t.Errorf("severity = %q, want unknown — the envelope status is not a severity", got)
	}
}

// Without fingerprints, resolving one instance must close only that instance.
func TestAlertmanagerWithoutFingerprintKeepsInstancesApart(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	envelope := func(statusA string) map[string]any {
		return map[string]any{"groupKey": "{}:{alertname=\"NoFp\"}", "alerts": []any{
			map[string]any{"status": statusA, "labels": map[string]any{"alertname": "NoFp", "instance": "a"}},
			map[string]any{"status": "firing", "labels": map[string]any{"alertname": "NoFp", "instance": "b"}},
		}}
	}
	if _, err := e.IngestAlertmanager(context.Background(), "key-1", envelope("firing")); err != nil {
		t.Fatal(err)
	}
	if got := len(groupsOf(ms)); got != 2 {
		t.Fatalf("%d groups for two instances, want 2", got)
	}
	if _, err := e.IngestAlertmanager(context.Background(), "key-1", envelope("resolved")); err != nil {
		t.Fatal(err)
	}
	if got := len(groupsOf(ms)); got != 2 {
		t.Errorf("%d groups after resolving one instance, want 2 — no new incident for the one still firing", got)
	}
	if got := len(openGroups(ms)); got != 1 {
		t.Errorf("%d open groups, want 1 (instance b)", got)
	}
}

func TestPagerDutyErrorSeverityIsKept(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	if _, err := e.IngestPagerDuty(context.Background(), "key-1", map[string]any{
		"event_action": "trigger", "dedup_key": "d1",
		"payload": map[string]any{"summary": "disk", "source": "db-1", "severity": "error"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := utils.StrVal(groupsOf(ms)[0], "severity"); got != "error" {
		t.Errorf("severity = %q, want error", got)
	}
}

// Events v2: a trigger without a dedup_key gets one, and it closes the event.
func TestPagerDutyTriggerWithoutKeyReturnsOneThatResolves(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	res, err := e.IngestPagerDuty(context.Background(), "key-1", map[string]any{
		"event_action": "trigger",
		"payload":      map[string]any{"summary": "no key", "source": "web-1", "severity": "critical"},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := utils.StrVal(res, "dedup_key")
	if key == "" {
		t.Fatal("dedup_key is empty; the sender has nothing to resolve with")
	}
	if _, err := e.IngestPagerDuty(context.Background(), "key-1", map[string]any{
		"event_action": "resolve", "dedup_key": key,
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(openGroups(ms)); got != 0 {
		t.Errorf("%d open groups after resolving with the returned key, want 0", got)
	}
	if _, err := e.IngestPagerDuty(context.Background(), "key-1", map[string]any{"event_action": "resolve"}); !errors.Is(err, ErrValidation) {
		t.Errorf("resolve without dedup_key = %v, want a validation error", err)
	}
}

func TestSeverityIsNormalizedAndTheSpellingKept(t *testing.T) {
	for raw, want := range map[string]string{"P1": "critical", "HIGH": "error", "warn": "warning", "wobbly": "unknown", "info": "info"} {
		ms := ingestStore()
		e := crudEngine(ms)
		if _, err := e.IngestAlert(context.Background(), "key-1", map[string]any{
			"title": "sev", "severity": raw, "dedupe_key": "sev",
		}); err != nil {
			t.Fatal(err)
		}
		g := groupsOf(ms)[0]
		if got := utils.StrVal(g, "severity"); got != want {
			t.Errorf("severity %q stored as %q, want %q", raw, got, want)
		}
		labels, _ := g["labels"].(map[string]any)
		if raw != want && labels["severity_raw"] != raw {
			t.Errorf("severity %q: severity_raw = %v, want the original spelling", raw, labels["severity_raw"])
		}
	}
}

func TestZeroEndsAtIsNoEnd(t *testing.T) {
	ms := ingestStore()
	e := crudEngine(ms)
	if _, err := e.IngestGrafanaAlerting(context.Background(), "key-1", map[string]any{
		"status": "firing",
		"alerts": []any{map[string]any{"status": "firing", "fingerprint": "g1", "endsAt": "0001-01-01T00:00:00Z",
			"labels": map[string]any{"alertname": "HighCPU"}}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, a := range ms.data["alerts"] {
		if a["ends_at"] != nil {
			t.Errorf("ends_at = %v, want null for a firing alert", a["ends_at"])
		}
	}
}
