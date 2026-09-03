package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// readinessEngine builds an engine whose installation is fully healthy, so each
// test can break exactly one thing and assert that exactly that check reacts.
func readinessEngine(t *testing.T) (*Engine, *memStore) {
	t.Helper()
	st := newMemStore()
	e := New(st)
	// The reference cache would serve stale collections across the mutations the
	// tests make; readiness reads through refCollection, so disable it here.
	e.refCache = nil
	e.deliveryCfg.TelegramToken = "test-token"

	st.seed("users", map[string]any{
		"id": "usr1", "name": "Alice",
		"notification_targets": []any{
			map[string]any{"type": "telegram", "target": "@alice"},
		},
	})
	st.seed("escalation_chains", map[string]any{
		"id": "chain1", "name": "primary",
		"steps": []any{map[string]any{"kind": StepNotifyUser, "user_ids": []any{"usr1"}}},
	})
	st.seed("integrations", map[string]any{
		"id": "int1", "name": "prod", "key": "k1",
		"routes": []any{map[string]any{
			"id": "r1", "name": "default", "match_type": "all",
			"is_default": true, "escalation_chain_id": "chain1",
		}},
	})
	now := utils.UTCNow()
	st.metadata = map[string]any{
		metaWorkerHeartbeat: utils.ToISO(now.Add(-10 * time.Second)),
		metaLastBackupAt:    utils.ToISO(now.Add(-2 * time.Hour)),
	}
	return e, st
}

// check pulls one named check out of a report.
func check(t *testing.T, report map[string]any, key string) map[string]any {
	t.Helper()
	for _, raw := range anyList(report["checks"]) {
		c, ok := raw.(map[string]any)
		if ok && utils.StrVal(c, "key") == key {
			return c
		}
	}
	t.Fatalf("report has no check %q", key)
	return nil
}

func mustReadiness(t *testing.T, e *Engine) map[string]any {
	t.Helper()
	report, err := e.Readiness(context.Background())
	if err != nil {
		t.Fatalf("Readiness: %v", err)
	}
	return report
}

func TestReadinessHealthyInstallationIsReady(t *testing.T) {
	e, _ := readinessEngine(t)
	report := mustReadiness(t, e)

	if report["ready"] != true {
		t.Errorf("ready = %v, want true; report = %v", report["ready"], report)
	}
	if report["blockers"] != 0 {
		t.Errorf("blockers = %v, want 0", report["blockers"])
	}
	if fp := utils.StrVal(report, "blocker_fingerprint"); fp != "" {
		t.Errorf("fingerprint = %q, want empty when nothing blocks", fp)
	}
	for _, key := range []string{"database", "worker", "integrations", "routing", "notification_targets", "schedule_coverage", "backup"} {
		if sev := utils.StrVal(check(t, report, key), "severity"); sev == ReadinessBlocker {
			t.Errorf("check %s is a blocker in a healthy installation: %s",
				key, utils.StrVal(check(t, report, key), "detail"))
		}
	}
}

func TestReadinessRouteToChainWithoutStepsBlocks(t *testing.T) {
	e, st := readinessEngine(t)
	// The chain the only route points at loses its steps: alerts still arrive
	// and now page nobody, which is the failure this check exists for.
	st.seed("escalation_chains", map[string]any{
		"id": "chain1", "name": "primary", "steps": []any{},
	})

	report := mustReadiness(t, e)
	c := check(t, report, "routing")
	if utils.StrVal(c, "severity") != ReadinessBlocker {
		t.Fatalf("routing severity = %s, want blocker", utils.StrVal(c, "severity"))
	}
	items := anyList(c["items"])
	if len(items) != 1 || !strings.Contains(items[0].(string), "has no steps") {
		t.Errorf("items = %v, want one entry naming the empty chain", items)
	}
	if report["ready"] != false {
		t.Error("ready should be false while a route reaches nobody")
	}
}

func TestReadinessRouteWithoutChainBlocks(t *testing.T) {
	e, st := readinessEngine(t)
	st.seed("integrations", map[string]any{
		"id": "int1", "name": "prod", "key": "k1",
		"routes": []any{map[string]any{
			"id": "r1", "name": "default", "match_type": "all", "escalation_chain_id": "",
		}},
	})

	c := check(t, mustReadiness(t, e), "routing")
	if utils.StrVal(c, "severity") != ReadinessBlocker {
		t.Fatalf("severity = %s, want blocker", utils.StrVal(c, "severity"))
	}
	if items := anyList(c["items"]); len(items) != 1 || !strings.Contains(items[0].(string), "no escalation chain") {
		t.Errorf("items = %v, want the route named as chainless", items)
	}
}

// A user with a telegram target on a deployment without a bot token is the
// BETA-032 lie in readiness form: it looks configured and pages nobody.
func TestReadinessTargetOnUnconfiguredChannelBlocks(t *testing.T) {
	e, _ := readinessEngine(t)
	e.deliveryCfg.TelegramToken = ""

	c := check(t, mustReadiness(t, e), "notification_targets")
	if utils.StrVal(c, "severity") != ReadinessBlocker {
		t.Fatalf("severity = %s, want blocker", utils.StrVal(c, "severity"))
	}
	items := anyList(c["items"])
	if len(items) != 1 || !strings.Contains(items[0].(string), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("items = %v, want Alice named with the missing token", items)
	}
}

func TestReadinessUserWithNoTargetAtAllBlocks(t *testing.T) {
	e, st := readinessEngine(t)
	st.seed("users", map[string]any{"id": "usr1", "name": "Alice"})

	c := check(t, mustReadiness(t, e), "notification_targets")
	if utils.StrVal(c, "severity") != ReadinessBlocker {
		t.Fatalf("severity = %s, want blocker", utils.StrVal(c, "severity"))
	}
	if items := anyList(c["items"]); len(items) != 1 ||
		!strings.Contains(items[0].(string), "no notification target") {
		t.Errorf("items = %v, want Alice named as unreachable", items)
	}
}

// A personal policy is an alternative to legacy targets, not an extra
// requirement: someone reachable only through a policy is still reachable.
func TestReadinessPolicyOnlyUserIsReachable(t *testing.T) {
	e, st := readinessEngine(t)
	st.seed("users", map[string]any{
		"id": "usr1", "name": "Alice",
		"notification_policies": map[string]any{
			"default": []any{map[string]any{"channel": "telegram", "target": "@alice", "wait_minutes": 0}},
		},
	})

	if sev := utils.StrVal(check(t, mustReadiness(t, e), "notification_targets"), "severity"); sev == ReadinessBlocker {
		t.Errorf("policy-only user reported unreachable: %s",
			utils.StrVal(check(t, mustReadiness(t, e), "notification_targets"), "detail"))
	}
}

func TestReadinessChainNamingMissingUserBlocks(t *testing.T) {
	e, st := readinessEngine(t)
	st.seed("escalation_chains", map[string]any{
		"id": "chain1", "name": "primary",
		"steps": []any{map[string]any{"kind": StepNotifyUser, "user_ids": []any{"ghost"}}},
	})

	c := check(t, mustReadiness(t, e), "notification_targets")
	if utils.StrVal(c, "severity") != ReadinessBlocker {
		t.Fatalf("severity = %s, want blocker", utils.StrVal(c, "severity"))
	}
	if items := anyList(c["items"]); len(items) != 1 ||
		!strings.Contains(items[0].(string), "no longer exists") {
		t.Errorf("items = %v, want the ghost user reported", items)
	}
}

func TestReadinessWorkerHeartbeat(t *testing.T) {
	cases := []struct {
		name      string
		heartbeat any
		want      string
		detail    string
	}{
		{"never recorded", nil, ReadinessBlocker, "no worker cycle has ever been recorded"},
		{"fresh", utils.ToISO(utils.UTCNow().Add(-5 * time.Second)), ReadinessOK, "completed a cycle"},
		{"stale", utils.ToISO(utils.UTCNow().Add(-10 * time.Minute)), ReadinessBlocker, "escalation and delivery have stopped"},
		{"unreadable", "not-a-timestamp", ReadinessBlocker, "unreadable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, st := readinessEngine(t)
			if tc.heartbeat == nil {
				delete(st.metadata, metaWorkerHeartbeat)
			} else {
				st.metadata[metaWorkerHeartbeat] = tc.heartbeat
			}
			c := check(t, mustReadiness(t, e), "worker")
			if got := utils.StrVal(c, "severity"); got != tc.want {
				t.Errorf("severity = %s, want %s (detail: %s)", got, tc.want, utils.StrVal(c, "detail"))
			}
			if !strings.Contains(utils.StrVal(c, "detail"), tc.detail) {
				t.Errorf("detail = %q, want it to mention %q", utils.StrVal(c, "detail"), tc.detail)
			}
		})
	}
}

func TestReadinessBackupAge(t *testing.T) {
	cases := []struct {
		name   string
		at     any
		want   string
		detail string
	}{
		{"never reported", nil, ReadinessBlocker, "no backup has ever been reported"},
		{"recent", utils.ToISO(utils.UTCNow().Add(-time.Hour)), ReadinessOK, "was reported"},
		{"stale", utils.ToISO(utils.UTCNow().Add(-48 * time.Hour)), ReadinessBlocker, "limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, st := readinessEngine(t)
			if tc.at == nil {
				delete(st.metadata, metaLastBackupAt)
			} else {
				st.metadata[metaLastBackupAt] = tc.at
			}
			c := check(t, mustReadiness(t, e), "backup")
			if got := utils.StrVal(c, "severity"); got != tc.want {
				t.Errorf("severity = %s, want %s (detail: %s)", got, tc.want, utils.StrVal(c, "detail"))
			}
			if !strings.Contains(utils.StrVal(c, "detail"), tc.detail) {
				t.Errorf("detail = %q, want it to mention %q", utils.StrVal(c, "detail"), tc.detail)
			}
		})
	}
}

func TestReadinessDatabaseDownBlocks(t *testing.T) {
	e, st := readinessEngine(t)
	st.pingErr = errors.New("connection refused")

	c := check(t, mustReadiness(t, e), "database")
	if utils.StrVal(c, "severity") != ReadinessBlocker {
		t.Fatalf("severity = %s, want blocker", utils.StrVal(c, "severity"))
	}
	if !strings.Contains(utils.StrVal(c, "detail"), "connection refused") {
		t.Errorf("detail = %q, want the driver error surfaced", utils.StrVal(c, "detail"))
	}
}

func TestReportBackupRecordsTimestamp(t *testing.T) {
	e, st := readinessEngine(t)
	delete(st.metadata, metaLastBackupAt)

	if _, err := e.ReportBackup(context.Background(), map[string]any{"kind": "pg_dump"}); err != nil {
		t.Fatalf("ReportBackup: %v", err)
	}
	if sev := utils.StrVal(check(t, mustReadiness(t, e), "backup"), "severity"); sev != ReadinessOK {
		t.Errorf("backup check = %s after a report, want ok", sev)
	}
	if st.metadata["last_backup_kind"] != "pg_dump" {
		t.Errorf("kind = %v, want it recorded", st.metadata["last_backup_kind"])
	}
}

func TestReportBackupRejectsUnparseableTimestamp(t *testing.T) {
	e, _ := readinessEngine(t)
	if _, err := e.ReportBackup(context.Background(), map[string]any{"at": "yesterday"}); err == nil {
		t.Fatal("expected a validation error for a non-ISO timestamp")
	}
}

// The acknowledgement is a decision about the blockers that existed when it was
// made. If the set changes, it must stop applying — otherwise it is a permanent
// mute wearing the costume of a considered decision.
func TestReadinessAcknowledgementIsBoundToTheBlockersItAccepted(t *testing.T) {
	e, st := readinessEngine(t)
	ctx := context.Background()
	delete(st.metadata, metaLastBackupAt) // one blocker: backup never reported

	report, err := e.AcknowledgeReadiness(ctx, map[string]any{"reason": "pilot, backups start Monday"})
	if err != nil {
		t.Fatalf("AcknowledgeReadiness: %v", err)
	}
	if report["production_ready"] != true {
		t.Fatalf("production_ready = %v, want true right after acknowledging", report["production_ready"])
	}
	if report["ready"] != false {
		t.Error("ready must stay false: acknowledging does not fix anything")
	}

	// A second, different blocker appears.
	st.seed("escalation_chains", map[string]any{"id": "chain1", "name": "primary", "steps": []any{}})

	report = mustReadiness(t, e)
	if report["production_ready"] != false {
		t.Error("the old acknowledgement must not cover a blocker that appeared after it")
	}
	ack, _ := report["acknowledgement"].(map[string]any)
	if ack == nil || ack["current"] != false {
		t.Errorf("acknowledgement = %v, want it reported as no longer current", ack)
	}
}

func TestAcknowledgeReadinessRequiresReasonAndBlockers(t *testing.T) {
	e, st := readinessEngine(t)
	ctx := context.Background()
	delete(st.metadata, metaLastBackupAt)

	if _, err := e.AcknowledgeReadiness(ctx, map[string]any{"reason": "  "}); err == nil {
		t.Error("expected a validation error for a blank reason")
	}

	// Nothing blocking: there is nothing to accept, and pretending otherwise
	// would store an acknowledgement that silently covers future blockers.
	e2, _ := readinessEngine(t)
	if _, err := e2.AcknowledgeReadiness(ctx, map[string]any{"reason": "why not"}); err == nil {
		t.Error("expected an error when there are no blockers to acknowledge")
	}
}

func TestWorkerHeartbeatIsThrottled(t *testing.T) {
	e, st := readinessEngine(t)
	ctx := context.Background()
	delete(st.metadata, metaWorkerHeartbeat)

	e.recordWorkerHeartbeat(ctx)
	first := utils.StrVal(st.metadata, metaWorkerHeartbeat)
	if first == "" {
		t.Fatal("the first heartbeat should always be written")
	}
	// A second call moments later must not write again: the worker cycles every
	// few seconds and this is a database write.
	st.metadata[metaWorkerHeartbeat] = "sentinel"
	e.recordWorkerHeartbeat(ctx)
	if st.metadata[metaWorkerHeartbeat] != "sentinel" {
		t.Error("heartbeat was rewritten inside the throttle window")
	}
}
