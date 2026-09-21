package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

var cleanupCollections = []string{
	"notification_delivery_attempts",
	"notification_batches",
	"notifications",
	"chatops_messages",
	"mobile_sessions",
	"mobile_devices",
	"kafka_outbox",
	"alerts",
	"alert_groups",
	"grafana_notification_policies",
	"grafana_channel_filters",
	"grafana_heartbeats",
	"chatops_channels",
	"integrations",
	"schedules",
	"escalation_chains",
	"teams",
	"users",
}

func TestAlertmanagerFiringAndResolve(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	seeded, err := eng.SeedDemo(ctx, false)
	if err != nil {
		t.Fatalf("seed demo failed: %v", err)
	}
	key := utils.StrVal(seeded["integration"].(map[string]any), "key")

	alertmanager := map[string]any{
		"receiver":          "platform",
		"status":            "firing",
		"groupLabels":       map[string]any{"alertname": "HighLatency"},
		"commonLabels":      map[string]any{"service": "api", "severity": "critical"},
		"commonAnnotations": map[string]any{"summary": "API latency is high"},
		"externalURL":       "https://alertmanager.example.test",
		"groupKey":          "{}:{alertname=\"HighLatency\"}",
		"alerts": []any{
			map[string]any{
				"status":       "firing",
				"labels":       map[string]any{"alertname": "HighLatency", "service": "api", "severity": "critical"},
				"annotations":  map[string]any{"summary": "API latency is high", "description": "p95 latency exceeded"},
				"startsAt":     "2026-05-09T01:00:00Z",
				"endsAt":       "0001-01-01T00:00:00Z",
				"generatorURL": "https://prometheus.example.test/graph",
				"fingerprint":  "am-fingerprint-1",
			},
		},
	}
	amResult, err := eng.IngestAlertmanager(ctx, key, alertmanager)
	if err != nil {
		t.Fatalf("alertmanager firing ingest failed: %v", err)
	}
	if amResult["processed"].(int) != 1 {
		t.Fatalf("alertmanager processed = %#v", amResult["processed"])
	}

	alertmanager["status"] = "resolved"
	alertmanager["alerts"].([]any)[0].(map[string]any)["status"] = "resolved"
	alertmanager["alerts"].([]any)[0].(map[string]any)["endsAt"] = "2026-05-09T01:05:00Z"
	resolved, err := eng.IngestAlertmanager(ctx, key, alertmanager)
	if err != nil {
		t.Fatalf("alertmanager resolved ingest failed: %v", err)
	}
	result0 := resolved["results"].([]any)[0].(map[string]any)
	if result0["result"] != "resolved" {
		t.Fatalf("resolved result = %#v", result0["result"])
	}
}

func TestBulkResolveGroups(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	now := utils.ToISO(time.Now().UTC())
	groups := []map[string]any{
		{"id": "bulk-group-1", "status": "open", "alert_count": 1, "created_at": now, "updated_at": now},
		{"id": "bulk-group-2", "status": "open", "alert_count": 1, "created_at": now, "updated_at": now},
	}
	for _, group := range groups {
		if err := st.UpsertItem(ctx, "alert_groups", group); err != nil {
			t.Fatalf("upsert alert group failed: %v", err)
		}
	}

	result, err := eng.BulkResolveGroups(ctx, []string{"bulk-group-1", "bulk-group-2"})
	if err != nil {
		t.Fatalf("bulk resolve failed: %v", err)
	}
	if got := len(result["resolved"].([]string)); got != 2 {
		t.Fatalf("resolved count = %d, result=%#v", got, result)
	}
	for _, id := range []string{"bulk-group-1", "bulk-group-2"} {
		group, err := st.GetItem(ctx, "alert_groups", id)
		if err != nil {
			t.Fatalf("get resolved group failed: %v", err)
		}
		if utils.StrVal(group, "status") != "resolved" {
			t.Fatalf("group %s status = %#v", id, group)
		}
		if utils.StrVal(group, "resolved_at") == "" {
			t.Fatalf("group %s missing resolved_at: %#v", id, group)
		}
	}
}

func TestArchiveResolvedGroupsDeletesRelatedRows(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	oldResolvedAt := utils.ToISO(time.Now().UTC().Add(-60 * 24 * time.Hour))
	now := utils.ToISO(time.Now().UTC())
	group := map[string]any{
		"id":          "archive-group",
		"status":      "resolved",
		"resolved_at": oldResolvedAt,
		"alert_count": 1,
		"created_at":  now,
		"updated_at":  now,
	}
	alert := map[string]any{
		"id":             "archive-alert",
		"alert_group_id": "archive-group",
		"status":         "resolved",
		"received_at":    now,
		"created_at":     now,
		"updated_at":     now,
	}
	notification := map[string]any{
		"id":             "archive-notification",
		"alert_group_id": "archive-group",
		"status":         "delivered",
		"retry_count":    0,
		"created_at":     now,
		"updated_at":     now,
	}
	attempt := map[string]any{
		"id":              "archive-attempt",
		"notification_id": "archive-notification",
		"attempt":         1,
		"status":          "delivered",
		"created_at":      now,
		"updated_at":      now,
	}
	for _, item := range []struct {
		collection string
		value      map[string]any
	}{
		{"alert_groups", group},
		{"alerts", alert},
		{"notifications", notification},
		{"notification_delivery_attempts", attempt},
	} {
		if err := st.UpsertItem(ctx, item.collection, item.value); err != nil {
			t.Fatalf("upsert %s failed: %v", item.collection, err)
		}
	}

	archived, err := eng.ArchiveResolvedGroups(ctx, 30)
	if err != nil {
		t.Fatalf("archive resolved groups failed: %v", err)
	}
	if archived != 1 {
		t.Fatalf("archived count = %d", archived)
	}
	assertMissingItem(t, ctx, st, "alert_groups", "archive-group")
	assertMissingItem(t, ctx, st, "alerts", "archive-alert")
	assertMissingItem(t, ctx, st, "notifications", "archive-notification")
	assertMissingItem(t, ctx, st, "notification_delivery_attempts", "archive-attempt")
}

func TestLegacyPoolIngest(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	seeded, err := eng.SeedDemo(ctx, false)
	if err != nil {
		t.Fatalf("seed demo failed: %v", err)
	}
	key := utils.StrVal(seeded["integration"].(map[string]any), "key")

	legacy, err := eng.IngestLegacyPool(ctx, key, map[string]any{
		"triggerMessage":   "CPU load is too high",
		"monitoringURL":    "https://monitor.example.test",
		"isEmergencyAlert": true,
		"alertChannel":     "telegram,call",
	})
	if err != nil {
		t.Fatalf("legacy pool ingest failed: %v", err)
	}
	if legacy["message"] != "success" {
		t.Fatalf("legacy response = %#v", legacy)
	}
}

func TestChatopsCommands(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	// The engine now checks the role itself rather than trusting the HTTP layer
	// to have done it, so a command has to say who is running it. A context with
	// no actor carries no role, and no role may act on an alert group — which is
	// the right answer for a caller that never identified itself.
	actorCtx := authz.NewContext(ctx, authz.Actor{
		ID: "usr-chatops", Kind: authz.KindUser, DisplayName: "responder", Role: authz.RoleResponder,
	})

	channel, err := eng.CreateChatopsChannel(ctx, map[string]any{
		"name":     "ops-slack",
		"platform": "slack",
	})
	if err != nil {
		t.Fatalf("create chatops channel failed: %v", err)
	}
	channelID := utils.StrVal(channel, "id")

	now := utils.ToISO(time.Now().UTC())
	groupID := "chatops-cmd-group"
	if err := st.UpsertItem(ctx, "alert_groups", map[string]any{
		"id":          groupID,
		"status":      "open",
		"alert_count": 1,
		"created_at":  now,
		"updated_at":  now,
	}); err != nil {
		t.Fatalf("upsert alert group failed: %v", err)
	}

	// /status should list the open group
	statusMsg, err := eng.PostChatopsCommand(actorCtx, map[string]any{
		"channel_id": channelID,
		"command":    "/status",
	})
	if err != nil {
		t.Fatalf("status command failed: %v", err)
	}
	resp := statusMsg["response"].(map[string]any)
	openGroups, _ := resp["open_alert_groups"].([]map[string]any)
	found := false
	for _, g := range openGroups {
		if utils.StrVal(g, "id") == groupID {
			found = true
		}
	}
	if !found {
		t.Fatalf("/status did not list group %s: %#v", groupID, resp)
	}

	// /ack should acknowledge the group
	ackMsg, err := eng.PostChatopsCommand(actorCtx, map[string]any{
		"channel_id": channelID,
		"command":    "/ack " + groupID,
	})
	if err != nil {
		t.Fatalf("ack command failed: %v", err)
	}
	ackGroup := ackMsg["response"].(map[string]any)["alert_group"].(map[string]any)
	if utils.StrVal(ackGroup, "status") != "acknowledged" {
		t.Fatalf("/ack: group status = %s", utils.StrVal(ackGroup, "status"))
	}

	// /resolve should resolve the group
	resolveMsg, err := eng.PostChatopsCommand(actorCtx, map[string]any{
		"channel_id": channelID,
		"command":    "/resolve " + groupID,
	})
	if err != nil {
		t.Fatalf("resolve command failed: %v", err)
	}
	resolveGroup := resolveMsg["response"].(map[string]any)["alert_group"].(map[string]any)
	if utils.StrVal(resolveGroup, "status") != "resolved" {
		t.Fatalf("/resolve: group status = %s", utils.StrVal(resolveGroup, "status"))
	}

	// /ack on a now-resolved group must be rejected by the model guard
	// (model.Acknowledge → ErrAcknowledgeResolved), not silently re-acknowledged.
	if _, err := eng.PostChatopsCommand(actorCtx, map[string]any{
		"channel_id": channelID,
		"command":    "/ack " + groupID,
	}); err == nil {
		t.Fatal("/ack on resolved group should be rejected, got nil error")
	}
}

func TestNotificationBatchingAndRetry(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	var attempts int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		if atomic.LoadInt32(&attempts) == 1 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer hook.Close()

	user, err := eng.CreateUser(ctx, map[string]any{
		"name":                 "Batch User",
		"username":             "batch-user",
		"notification_targets": []any{map[string]any{"type": "slack", "target": hook.URL}},
	})
	if err != nil {
		t.Fatalf("create batching user failed: %v", err)
	}
	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "batch-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{user["id"]}}},
	})
	if err != nil {
		t.Fatalf("create batching chain failed: %v", err)
	}
	integration, err := eng.CreateIntegration(ctx, map[string]any{
		"name":     "batch-integration",
		"group_by": []any{"alertname", "service"},
		"routes": []any{map[string]any{
			"name":                "all",
			"match_type":          "all",
			"is_default":          true,
			"escalation_chain_id": chain["id"],
		}},
		"notification_policy": map[string]any{
			"channels":               []any{"slack"},
			"batch_timeout_seconds":  60,
			"batch_deadline_seconds": 120,
		},
	})
	if err != nil {
		t.Fatalf("create batching integration failed: %v", err)
	}
	key := utils.StrVal(integration, "key")

	batched, err := eng.IngestAlert(ctx, key, map[string]any{
		"title":   "Batch me",
		"message": "Batch delivery should flush through worker",
		"labels":  map[string]any{"alertname": "BatchMe", "service": "api", "severity": "critical"},
	})
	if err != nil {
		t.Fatalf("batched ingest failed: %v", err)
	}
	group := batched["group"].(map[string]any)
	batches, err := st.ListCollection(ctx, "notification_batches")
	if err != nil || len(batches) == 0 {
		t.Fatalf("expected notification batch, got len=%d err=%v", len(batches), err)
	}
	past := utils.ToISO(time.Now().UTC().Add(-time.Minute))
	for _, batch := range batches {
		if utils.StrVal(batch, "alert_group_id") != utils.StrVal(group, "id") {
			continue
		}
		batch["flush_at"] = past
		batch["deadline_at"] = past
		if err := st.UpsertItem(ctx, "notification_batches", batch); err != nil {
			t.Fatalf("upsert batch failed: %v", err)
		}
	}

	cycle, err := eng.RunWorkerCycle(ctx)
	if err != nil {
		t.Fatalf("worker cycle failed: %v", err)
	}
	if cycle["flushed_notification_batches"].(int) < 1 {
		t.Fatalf("expected flushed batch, got %#v", cycle)
	}
	notifs, err := st.ListCollection(ctx, "notifications")
	if err != nil {
		t.Fatalf("list notifications failed: %v", err)
	}
	var retry map[string]any
	for _, n := range notifs {
		if utils.StrVal(n, "alert_group_id") == utils.StrVal(group, "id") && utils.StrVal(n, "channel") == "slack" {
			retry = n
			break
		}
	}
	if retry == nil || utils.StrVal(retry, "status") != "retry_scheduled" {
		t.Fatalf("expected retry_scheduled slack notification, got %#v", retry)
	}
	retry["next_retry_at"] = utils.ToISO(time.Now().UTC().Add(-time.Second))
	if err := st.UpsertItem(ctx, "notifications", retry); err != nil {
		t.Fatalf("upsert retry notification failed: %v", err)
	}
	retryCycle, err := eng.RunWorkerCycle(ctx)
	if err != nil {
		t.Fatalf("retry worker cycle failed: %v", err)
	}
	if retryCycle["retried_notifications"].(int) < 1 {
		t.Fatalf("expected retry, got %#v", retryCycle)
	}
	if atomic.LoadInt32(&attempts) < 2 {
		t.Fatalf("expected at least two webhook attempts, got %d", attempts)
	}

	history, err := eng.GetHistory(ctx, map[string]any{"channel": "slack", "limit": 10, "offset": 0})
	if err != nil {
		t.Fatalf("history failed: %v", err)
	}
	if history["count"].(int) < 1 {
		t.Fatalf("expected history entries, got %#v", history)
	}
}

func TestNotificationRetryExhaustionMarksFailed(t *testing.T) {
	t.Setenv("NXS_ANOMALY_NOTIFICATION_MAX_RETRIES", "2")
	t.Setenv("NXS_ANOMALY_NOTIFICATION_RETRY_DELAYS", "1")

	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	var attempts int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "still down", http.StatusServiceUnavailable)
	}))
	defer hook.Close()

	user, err := eng.CreateUser(ctx, map[string]any{
		"name":                 "Retry Exhausted",
		"username":             "retry-exhausted",
		"notification_targets": []any{map[string]any{"type": "slack", "target": hook.URL}},
	})
	if err != nil {
		t.Fatalf("create retry user failed: %v", err)
	}
	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "retry-exhaustion-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{user["id"]}}},
	})
	if err != nil {
		t.Fatalf("create retry chain failed: %v", err)
	}
	integration, err := eng.CreateIntegration(ctx, map[string]any{
		"name":     "retry-exhaustion-integration",
		"group_by": []any{"alertname", "service"},
		"routes": []any{map[string]any{
			"name":                "all",
			"match_type":          "all",
			"is_default":          true,
			"escalation_chain_id": chain["id"],
		}},
		"notification_policy": map[string]any{"channels": []any{"slack"}},
	})
	if err != nil {
		t.Fatalf("create retry integration failed: %v", err)
	}

	ingested, err := eng.IngestAlert(ctx, utils.StrVal(integration, "key"), map[string]any{
		"title":   "Retry exhaustion",
		"message": "Notification should eventually fail",
		"labels":  map[string]any{"alertname": "RetryExhaustion", "service": "api", "severity": "critical"},
	})
	if err != nil {
		t.Fatalf("retry exhaustion ingest failed: %v", err)
	}
	groupID := utils.StrVal(ingested["group"].(map[string]any), "id")

	firstCycle, err := eng.RunWorkerCycle(ctx)
	if err != nil {
		t.Fatalf("first retry exhaustion cycle failed: %v", err)
	}
	if firstCycle["delivered_notifications"].(int) != 0 {
		t.Fatalf("expected no delivered notifications, got %#v", firstCycle)
	}

	notifications, err := st.ListCollection(ctx, "notifications")
	if err != nil {
		t.Fatalf("list retry notifications failed: %v", err)
	}
	var retry map[string]any
	for _, n := range notifications {
		if utils.StrVal(n, "alert_group_id") == groupID && utils.StrVal(n, "channel") == "slack" {
			retry = n
			break
		}
	}
	if retry == nil || utils.StrVal(retry, "status") != "retry_scheduled" {
		t.Fatalf("expected retry_scheduled notification, got %#v", retry)
	}
	retry["next_retry_at"] = utils.ToISO(time.Now().UTC().Add(-time.Second))
	if err := st.UpsertItem(ctx, "notifications", retry); err != nil {
		t.Fatalf("upsert retry notification failed: %v", err)
	}

	secondCycle, err := eng.RunWorkerCycle(ctx)
	if err != nil {
		t.Fatalf("second retry exhaustion cycle failed: %v", err)
	}
	if secondCycle["failed_notifications"].(int) < 1 {
		t.Fatalf("expected failed notification in cycle, got %#v", secondCycle)
	}
	failed, err := st.GetItem(ctx, "notifications", utils.StrVal(retry, "id"))
	if err != nil {
		t.Fatalf("get failed notification failed: %v", err)
	}
	if utils.StrVal(failed, "status") != "failed" {
		t.Fatalf("notification did not fail after retry exhaustion: %#v", failed)
	}
	if got := utils.IntVal(failed, "retry_count"); got != 2 {
		t.Fatalf("retry_count = %d, notification=%#v", got, failed)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Fatalf("attempt count = %d", attempts)
	}
}

func TestDockerHealthcheckUsesCompiledBinary(t *testing.T) {
	data, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile failed: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "FROM python:") {
		t.Fatalf("Dockerfile still uses Python base image")
	}
	if !strings.Contains(text, "go build") {
		t.Fatalf("Dockerfile should build the Go binary")
	}
	if !strings.Contains(text, `CMD ["/usr/local/bin/nxs-anomaly"`) {
		t.Fatalf("Dockerfile should run the compiled nxs-anomaly binary")
	}
}

func newIntegrationEngine(t *testing.T, ctx context.Context) (store.PostgreSQLStore, *engine.Engine) {
	t.Helper()
	dsn := os.Getenv("NXS_ANOMALY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NXS_ANOMALY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	t.Setenv("NXS_ANOMALY_DB_DSN", dsn)
	st, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		t.Fatalf("new PostgreSQL store failed: %v", err)
	}
	assertExclusiveDatabase(t, ctx, st)
	return st, engine.New(st)
}

// foreignWorkerHeartbeatMaxAge is how fresh the heartbeat in the database has to
// be for us to conclude that somebody else's worker is polling it right now. The
// worker persists its heartbeat every 30s (engine.workerHeartbeatInterval), so
// twice that clears a heartbeat left behind by an earlier local run while still
// catching a worker that is actually alive.
const foreignWorkerHeartbeatMaxAge = 60 * time.Second

var (
	exclusiveDatabaseOnce   sync.Once
	exclusiveDatabaseReason string
)

// assertExclusiveDatabase refuses to run the suite against a database that a
// live worker is already polling.
//
// The suite needs the database to itself. It seeds notifications and then runs
// worker cycles in-process, expecting its own httptest stand-ins to receive the
// deliveries. A deployed worker pointed at the same database claims those
// notifications first and delivers them from wherever it runs, so the stand-ins
// are never called. What that looks like from here is seven unrelated delivery
// tests failing — "push relay was never called", a notification stuck in
// `delivering`, a policy that never advanced — with nothing in the output
// connecting them to the real cause. Diagnosing it from scratch costs an
// afternoon; the heartbeat the worker already writes for readiness makes it a
// one-line check.
//
// Checked once per process, before the first test touches any data: after that
// the suite's own cycles have written the heartbeat and the signal is gone.
func assertExclusiveDatabase(t *testing.T, ctx context.Context, st store.PostgreSQLStore) {
	t.Helper()
	exclusiveDatabaseOnce.Do(func() {
		exclusiveDatabaseReason = foreignWorkerReason(ctx, st)
	})
	if exclusiveDatabaseReason != "" {
		t.Fatal(exclusiveDatabaseReason)
	}
}

// foreignWorkerReason returns why the database is not ours to use, or "" if it
// is. Split out so the verdict can be computed once and reported by every test:
// a t.Fatal inside sync.Once would only reach whichever test happened to run
// first, and the rest would go on to fail obscurely.
func foreignWorkerReason(ctx context.Context, st store.PostgreSQLStore) string {
	meta, err := st.GetMetadata(ctx)
	if err != nil {
		// Not a reason to refuse: a database too fresh to answer this is
		// exactly the empty one we want.
		return ""
	}
	raw := utils.StrVal(meta, "worker_heartbeat_at")
	if raw == "" {
		return ""
	}
	at, err := utils.ParseDatetime(raw)
	if err != nil {
		return ""
	}
	age := utils.UTCNow().Sub(at)
	if age >= foreignWorkerHeartbeatMaxAge {
		return ""
	}
	return fmt.Sprintf(`refusing to run the integration suite against this database.

A worker wrote its heartbeat %s ago, so something is polling this database right
now. The suite clears collections and expects to be the only worker; a live one
claims the notifications it seeds and delivers them elsewhere, which shows up as
unrelated delivery tests failing.

Point NXS_ANOMALY_TEST_DATABASE_URL at a database of its own, or run
tests/run_postgres_integration.sh with no DSN set and it will start one.`,
		age.Round(time.Second))
}

func clearStore(t *testing.T, ctx context.Context, st store.PostgreSQLStore) {
	t.Helper()
	if err := st.ClearCollections(ctx, cleanupCollections); err != nil {
		t.Fatalf("clear collections failed: %v", err)
	}
	// The audit trail is append-only and outside the collections, so it
	// survived every reset and accumulated across the whole suite. Tests that
	// assert on absolute event counts then passed or failed depending on which
	// tests ran before them. Pruning past the present empties it.
	if _, err := st.PruneAuditEvents(ctx, utils.ToISO(utils.UTCNow().Add(time.Hour))); err != nil {
		t.Fatalf("clear audit events failed: %v", err)
	}
}

func assertMissingItem(t *testing.T, ctx context.Context, st store.PostgreSQLStore, collection, id string) {
	t.Helper()
	item, err := st.GetItem(ctx, collection, id)
	if err != nil {
		t.Fatalf("get %s/%s failed: %v", collection, id, err)
	}
	if item != nil {
		t.Fatalf("expected %s/%s to be deleted, got %#v", collection, id, item)
	}
}

// TestSoftDeleteIntegration verifies that soft-deleting an integration:
//  1. Removes it from ListCollection and ListCollectionPage results.
//  2. Makes IngestAlert return "integration key not found".
//  3. Still allows GetItem to retrieve the record (by ID).
func TestSoftDeleteIntegration(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "soft-delete-chain",
		"steps": []any{map[string]any{"kind": "WAIT", "delay_minutes": 5}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "soft-delete-test",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	integID := utils.StrVal(integ, "id")
	integKey := utils.StrVal(integ, "key")

	// 1. Integration appears in list before deletion.
	listed, err := eng.ListCollectionPage(ctx, "integrations", map[string]any{"limit": 100})
	if err != nil {
		t.Fatalf("list before delete: %v", err)
	}
	found := false
	for _, item := range listed["items"].([]map[string]any) {
		if utils.StrVal(item, "id") == integID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("integration not found in list before deletion")
	}

	// 2. Soft-delete via DeleteEntity.
	_, err = eng.DeleteEntity(ctx, "integrations", integID)
	if err != nil {
		t.Fatalf("delete integration: %v", err)
	}

	// 3. Integration absent from list after deletion.
	listed, err = eng.ListCollectionPage(ctx, "integrations", map[string]any{"limit": 100})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	for _, item := range listed["items"].([]map[string]any) {
		if utils.StrVal(item, "id") == integID {
			t.Fatalf("soft-deleted integration still appears in list")
		}
	}

	// 4. IngestAlert returns not-found for the deleted integration key.
	_, err = eng.IngestAlert(ctx, integKey, map[string]any{"title": "test"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error after soft-delete, got: %v", err)
	}

	// 5. GetItem still returns the row (soft-delete keeps the record).
	raw, err := st.GetItem(ctx, "integrations", integID)
	if err != nil {
		t.Fatalf("GetItem after delete: %v", err)
	}
	if raw == nil {
		t.Fatal("GetItem returned nil: soft-deleted row should still exist in DB")
	}
	if utils.StrVal(raw, "deleted_at") == "" {
		t.Fatal("deleted_at not set after soft-delete")
	}
}

// TestChatopsMessagesArchival verifies that ArchiveChatopsMessages removes old messages
// while leaving recent ones intact.
func TestChatopsMessagesArchival(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	now := utils.ToISO(time.Now().UTC())
	old := utils.ToISO(time.Now().UTC().AddDate(0, 0, -40)) // 40 days ago

	msgs := []map[string]any{
		{"id": "msg-old-1", "direction": "outbound", "created_at": old},
		{"id": "msg-old-2", "direction": "outbound", "created_at": old},
		{"id": "msg-recent", "direction": "outbound", "created_at": now},
	}
	for _, m := range msgs {
		if err := st.UpsertItem(ctx, "chatops_messages", m); err != nil {
			t.Fatalf("upsert chatops_message %s: %v", m["id"], err)
		}
	}

	// Archive with 30-day TTL — should remove the two old messages.
	removed, err := eng.ArchiveChatopsMessages(ctx, 30)
	if err != nil {
		t.Fatalf("ArchiveChatopsMessages: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed, got %d", removed)
	}

	// The recent message must still exist.
	item, err := st.GetItem(ctx, "chatops_messages", "msg-recent")
	if err != nil {
		t.Fatalf("GetItem msg-recent: %v", err)
	}
	if item == nil {
		t.Fatal("recent chatops_message was unexpectedly deleted")
	}

	// Old messages must be gone.
	for _, id := range []string{"msg-old-1", "msg-old-2"} {
		item, err := st.GetItem(ctx, "chatops_messages", id)
		if err != nil {
			t.Fatalf("GetItem %s: %v", id, err)
		}
		if item != nil {
			t.Fatalf("old chatops_message %s should have been archived", id)
		}
	}
}

// TestDeadLetterWebhookFiredOnFailure verifies that sendDeadLetterEvent posts to
// the configured URL when a notification is permanently failed.
func TestDeadLetterWebhookFiredOnFailure(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	var mu sync.Mutex
	var received []map[string]any
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			mu.Lock()
			received = append(received, body)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer dlSrv.Close()

	t.Setenv("NXS_ANOMALY_DEAD_LETTER_WEBHOOK_URL", dlSrv.URL)
	t.Setenv("NXS_ANOMALY_NOTIFICATION_MAX_RETRIES", "1") // exhaust on first failure

	eng := engine.New(st)

	chain, _ := eng.CreateEscalationChain(ctx, map[string]any{
		"name": "dl-chain",
		"steps": []any{map[string]any{
			"kind":     "NOTIFY_USER",
			"user_ids": []any{},
		}},
	})
	user, _ := eng.CreateUser(ctx, map[string]any{
		"name":     "DL User",
		"username": "dl-user",
		"notification_targets": []any{
			map[string]any{"type": "webhook", "target": "http://127.0.0.1:1/fail"},
		},
	})
	integ, _ := eng.CreateIntegration(ctx, map[string]any{
		"name": "dl-integration",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})

	// Update chain to notify the user.
	if _, err := eng.UpdateEscalationChain(ctx, utils.StrVal(chain, "id"), map[string]any{
		"steps": []any{map[string]any{
			"kind":     "NOTIFY_USER",
			"user_ids": []any{user["id"]},
		}},
	}); err != nil {
		t.Fatalf("update chain: %v", err)
	}

	_, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{
		"title": "dead-letter test",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// Run worker cycles until the dead-letter event arrives or we time out.
	var event map[string]any
	deadline := time.Now().Add(testDeadline(5 * time.Second))
	for time.Now().Before(deadline) {
		if _, err := eng.RunWorkerCycle(ctx); err != nil {
			t.Fatalf("worker cycle: %v", err)
		}
		mu.Lock()
		if len(received) > 0 {
			event = received[0]
		}
		mu.Unlock()
		if event != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if event == nil {
		t.Fatal("dead letter webhook was not called for permanently failed notification")
	}
	if event["event"] != "notification.failed" {
		t.Errorf("expected event=notification.failed, got %v", event["event"])
	}
	if utils.StrVal(event, "channel") != "webhook" {
		t.Errorf("expected channel=webhook, got %v", event["channel"])
	}
}

// ── Пункт 9: BulkSilenceGroups ───────────────────────────────────────────────

func TestBulkSilenceGroups(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	now := utils.ToISO(time.Now().UTC())
	for _, id := range []string{"silence-1", "silence-2", "silence-already-resolved"} {
		status := "open"
		if id == "silence-already-resolved" {
			status = "resolved"
		}
		if err := st.UpsertItem(ctx, "alert_groups", map[string]any{
			"id": id, "status": status, "alert_count": 1,
			"created_at": now, "updated_at": now,
		}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}

	result, err := eng.BulkSilenceGroups(ctx,
		[]string{"silence-1", "silence-2", "silence-already-resolved", "silence-missing"},
		60)
	if err != nil {
		t.Fatalf("BulkSilenceGroups: %v", err)
	}

	silenced, _ := result["silenced"].([]string)
	if len(silenced) != 2 {
		t.Fatalf("expected 2 silenced, got %d: %v", len(silenced), result)
	}

	for _, id := range []string{"silence-1", "silence-2"} {
		g, _ := st.GetItem(ctx, "alert_groups", id)
		if utils.StrVal(g, "status") != "silenced" {
			t.Errorf("group %s status = %v, want silenced", id, g["status"])
		}
	}

	notFound, _ := result["not_found"].([]string)
	if len(notFound) != 1 || notFound[0] != "silence-missing" {
		t.Errorf("expected not_found=[silence-missing], got %v", notFound)
	}

	skipped, _ := result["skipped"].([]string)
	if len(skipped) != 1 || skipped[0] != "silence-already-resolved" {
		t.Errorf("expected skipped=[silence-already-resolved], got %v", skipped)
	}
}

// ── Пункт 10: GetHistory filters ─────────────────────────────────────────────

func TestGetHistoryFilters(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	now := utils.ToISO(time.Now().UTC())
	past := utils.ToISO(time.Now().UTC().AddDate(0, 0, -2))

	// Create two integrations so we can filter by integration_id.
	chain, _ := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "history-chain",
		"steps": []any{map[string]any{"kind": "WAIT", "delay_minutes": 5}},
	})
	mkInteg := func(name string) map[string]any {
		integ, err := eng.CreateIntegration(ctx, map[string]any{
			"name": name,
			"routes": []any{map[string]any{
				"name": "default", "match_type": "all", "is_default": true,
				"escalation_chain_id": chain["id"],
			}},
		})
		if err != nil {
			t.Fatalf("create integration %s: %v", name, err)
		}
		return integ
	}
	integA := mkInteg("hist-integ-a")
	integB := mkInteg("hist-integ-b")

	// Seed alert groups directly for speed.
	groups := []map[string]any{
		{"id": "hg-1", "integration_id": utils.StrVal(integA, "id"),
			"status": "resolved", "severity": "critical", "alert_ids": []any{}, "alert_count": 1,
			"resolved_at": past, "created_at": past, "updated_at": now, "last_received_at": now},
		{"id": "hg-2", "integration_id": utils.StrVal(integB, "id"),
			"status": "open", "severity": "warning", "alert_ids": []any{}, "alert_count": 1,
			"created_at": now, "updated_at": now, "last_received_at": now},
	}
	for _, g := range groups {
		if err := st.UpsertItem(ctx, "alert_groups", g); err != nil {
			t.Fatalf("upsert group: %v", err)
		}
	}

	// Filter by integration_id — only hg-1.
	res, err := eng.GetHistory(ctx, map[string]any{
		"integration": utils.StrVal(integA, "id"),
		"limit":       50,
	})
	if err != nil {
		t.Fatalf("GetHistory by integration: %v", err)
	}
	if res["total"].(int) != 1 {
		t.Errorf("by integration: expected total=1, got %v", res["total"])
	}

	// Filter by severity=critical — only hg-1.
	res, _ = eng.GetHistory(ctx, map[string]any{"severity": "critical", "limit": 50})
	if res["total"].(int) != 1 {
		t.Errorf("by severity: expected total=1, got %v", res["total"])
	}

	// Filter by status=open — only hg-2.
	res, _ = eng.GetHistory(ctx, map[string]any{"status": "open", "limit": 50})
	if res["total"].(int) != 1 {
		t.Errorf("by status: expected total=1, got %v", res["total"])
	}

	// No filters — both groups.
	res, _ = eng.GetHistory(ctx, map[string]any{"limit": 50})
	if total := res["total"].(int); total < 2 {
		t.Errorf("no filters: expected >=2, got %d", total)
	}

	// Pagination: limit=1 → count=1 but total>=2.
	res, _ = eng.GetHistory(ctx, map[string]any{"limit": 1})
	if len(res["items"].([]map[string]any)) != 1 {
		t.Errorf("pagination limit=1: expected 1 item, got %v", res["items"])
	}
	if res["total"].(int) < 2 {
		t.Errorf("pagination: total should be >=2, got %v", res["total"])
	}
}

// ── Пункт 11: DebugRoute ─────────────────────────────────────────────────────

func TestDebugRoute(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	user, _ := eng.CreateUser(ctx, map[string]any{
		"name": "Debug User", "username": "debug-user",
		"notification_targets": []any{map[string]any{"type": "log"}},
	})
	chain, _ := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "debug-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{user["id"]}}},
	})
	integ, _ := eng.CreateIntegration(ctx, map[string]any{
		"name": "debug-integ",
		"routes": []any{
			map[string]any{
				"name":                "critical",
				"match_type":          "labels",
				"labels":              map[string]any{"severity": "critical"},
				"is_default":          false,
				"escalation_chain_id": chain["id"],
			},
			map[string]any{
				"name":                "default",
				"match_type":          "all",
				"is_default":          true,
				"escalation_chain_id": chain["id"],
			},
		},
	})

	// Test: unknown key returns not-found error.
	_, err := eng.DebugRoute(ctx, "invalid-key", map[string]any{"title": "test"})
	if err == nil {
		t.Fatal("expected error for unknown integration key")
	}

	// Test: label route is selected for severity=critical.
	result, err := eng.DebugRoute(ctx, utils.StrVal(integ, "key"), map[string]any{
		"title":  "High CPU",
		"labels": map[string]any{"severity": "critical"},
	})
	if err != nil {
		t.Fatalf("DebugRoute: %v", err)
	}
	if result["route"] == nil {
		t.Fatal("route is nil")
	}
	route, _ := result["route"].(map[string]any)
	if route["name"] != "critical" {
		t.Errorf("expected route=critical, got %v", route["name"])
	}
	if result["dedupe_key"] == nil || result["dedupe_key"] == "" {
		t.Error("dedupe_key should be set")
	}

	// notification_preview should list the user from the chain step.
	preview, _ := result["notification_preview"].([]map[string]any)
	if len(preview) == 0 {
		t.Error("notification_preview should be non-empty for NOTIFY_USER step")
	}
	if len(preview) > 0 && utils.StrVal(preview[0], "username") != "debug-user" {
		t.Errorf("expected username=debug-user in preview, got %v", preview[0])
	}

	// Test: default route selected when labels don't match critical.
	result2, err := eng.DebugRoute(ctx, utils.StrVal(integ, "key"), map[string]any{
		"title":  "Disk Warning",
		"labels": map[string]any{"severity": "warning"},
	})
	if err != nil {
		t.Fatalf("DebugRoute default: %v", err)
	}
	route2, _ := result2["route"].(map[string]any)
	if route2["name"] != "default" {
		t.Errorf("expected default route, got %v", route2["name"])
	}
}

// TestIngestDoesNotRewriteUnrelatedGroups verifies the dirty-tracking in
// UpdateCollections: ingesting an alert for one integration must not rewrite
// alert group rows that belong to other integrations, even though the whole
// alert_groups collection is loaded into the mutator state. The row version
// is observed via the PostgreSQL xmin system column, which changes on every
// physical row update.
func TestIngestDoesNotRewriteUnrelatedGroups(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	createIntegration := func(name string) string {
		t.Helper()
		integ, err := eng.CreateIntegration(ctx, map[string]any{
			"name": name,
			"routes": []any{map[string]any{
				"name": "all", "match_type": "all", "is_default": true,
			}},
		})
		if err != nil {
			t.Fatalf("create integration %s failed: %v", name, err)
		}
		return utils.StrVal(integ, "key")
	}
	keyA := createIntegration("dirty-track-a")
	keyB := createIntegration("dirty-track-b")

	resA, err := eng.IngestAlert(ctx, keyA, map[string]any{
		"title": "alert-a", "labels": map[string]any{"alertname": "a"},
	})
	if err != nil {
		t.Fatalf("ingest A failed: %v", err)
	}
	groupAID := utils.StrVal(resA["group"].(map[string]any), "id")

	resB, err := eng.IngestAlert(ctx, keyB, map[string]any{
		"title": "alert-b", "labels": map[string]any{"alertname": "b"},
	})
	if err != nil {
		t.Fatalf("ingest B failed: %v", err)
	}
	groupBID := utils.StrVal(resB["group"].(map[string]any), "id")

	conn, err := pgx.Connect(ctx, os.Getenv("NXS_ANOMALY_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx) //nolint:errcheck
	rowVersion := func(id string) string {
		t.Helper()
		var xmin string
		err := conn.QueryRow(ctx,
			"SELECT xmin::text FROM nxs_anomaly_alert_groups WHERE id=$1", id,
		).Scan(&xmin)
		if err != nil {
			t.Fatalf("read xmin for %s failed: %v", id, err)
		}
		return xmin
	}
	groupAVersion := rowVersion(groupAID)
	groupBVersion := rowVersion(groupBID)

	// Second alert for B attaches to the existing group B; group A is loaded
	// into the mutator state but must not be written back.
	if _, err := eng.IngestAlert(ctx, keyB, map[string]any{
		"title": "alert-b", "labels": map[string]any{"alertname": "b"},
	}); err != nil {
		t.Fatalf("second ingest B failed: %v", err)
	}

	if got := rowVersion(groupBID); got == groupBVersion {
		t.Fatalf("group B row version unchanged (%s); expected rewrite on attach", got)
	}
	if got := rowVersion(groupAID); got != groupAVersion {
		t.Fatalf("group A row was rewritten by unrelated ingest: xmin %s -> %s", groupAVersion, got)
	}
}

// TestDueGroupsIndexMatchesWorkerQuery verifies migration 0016: the partial
// index used by ListDueAlertGroups must cover the same status predicate as the
// query (NOT IN ('resolved','silenced')), not just status = 'open'.
func TestDueGroupsIndexMatchesWorkerQuery(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()

	conn, err := pgx.Connect(ctx, os.Getenv("NXS_ANOMALY_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx) //nolint:errcheck

	var indexDef string
	err = conn.QueryRow(ctx,
		"SELECT indexdef FROM pg_indexes WHERE indexname='nxs_anomaly_alert_groups_due_idx'",
	).Scan(&indexDef)
	if err != nil {
		t.Fatalf("read due idx definition failed: %v", err)
	}
	for _, want := range []string{"resolved", "silenced", "next_run_at"} {
		if !strings.Contains(indexDef, want) {
			t.Errorf("due idx definition missing %q: %s", want, indexDef)
		}
	}
}

// TestDeliveryCycleWritesDeliveryAttempts pins the save-only contract for
// notification_delivery_attempts: the delivery mutators no longer load the
// collection (it is append-only there), and attempt rows must still be
// persisted for every delivery attempt.
func TestDeliveryCycleWritesDeliveryAttempts(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "attempts-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{}}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	user, err := eng.CreateUser(ctx, map[string]any{
		"name":     "Attempts User",
		"username": "attempts-user",
		"notification_targets": []any{
			map[string]any{"type": "webhook", "target": okSrv.URL},
		},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "attempts-integration",
		"routes": []any{map[string]any{
			"name": "default", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	if _, err := eng.UpdateEscalationChain(ctx, utils.StrVal(chain, "id"), map[string]any{
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{user["id"]}}},
	}); err != nil {
		t.Fatalf("update chain: %v", err)
	}

	if _, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{
		"title": "attempts test",
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	deadline := time.Now().Add(testDeadline(5 * time.Second))
	for time.Now().Before(deadline) {
		if _, err := eng.RunWorkerCycle(ctx); err != nil {
			t.Fatalf("worker cycle: %v", err)
		}
		n, err := st.CountCollection(ctx, "notification_delivery_attempts", nil)
		if err != nil {
			t.Fatalf("count attempts: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no delivery attempt rows were persisted by the worker cycle")
}

// TestAlertmanagerEnvelopeSingleTransaction verifies in-envelope batch ingest:
// alerts sharing a fingerprint within ONE envelope must land in one group, and
// a resolve event later in the same envelope must resolve the group created by
// an earlier alert of that envelope.
func TestAlertmanagerEnvelopeBatchSemantics(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "am-envelope-batch",
		"routes": []any{map[string]any{
			"name": "all", "match_type": "all", "is_default": true,
		}},
	})
	if err != nil {
		t.Fatalf("create integration failed: %v", err)
	}
	key := utils.StrVal(integ, "key")

	amAlert := func(status, fingerprint string) map[string]any {
		return map[string]any{
			"status":      status,
			"labels":      map[string]any{"alertname": "EnvelopeBatch", "severity": "critical"},
			"annotations": map[string]any{"summary": "envelope batch test"},
			"fingerprint": fingerprint,
		}
	}

	// Two firing alerts with the same fingerprint in one envelope → one group.
	res, err := eng.IngestAlertmanager(ctx, key, map[string]any{
		"receiver": "x", "status": "firing",
		"alerts": []any{amAlert("firing", "env-fp-1"), amAlert("firing", "env-fp-1")},
	})
	if err != nil {
		t.Fatalf("envelope ingest failed: %v", err)
	}
	if res["processed"].(int) != 2 {
		t.Fatalf("processed = %#v, want 2", res["processed"])
	}
	results := res["results"].([]any)
	group0 := results[0].(map[string]any)["group"].(map[string]any)
	group1 := results[1].(map[string]any)["group"].(map[string]any)
	if utils.StrVal(group0, "id") != utils.StrVal(group1, "id") {
		t.Fatalf("same-fingerprint alerts created two groups: %s vs %s",
			utils.StrVal(group0, "id"), utils.StrVal(group1, "id"))
	}
	if got := utils.IntVal(group1, "alert_count"); got != 2 {
		t.Fatalf("alert_count = %d, want 2", got)
	}
	if n, err := st.CountCollection(ctx, "alert_groups", map[string]any{"dedupe_key": "env-fp-1"}); err != nil || n != 1 {
		t.Fatalf("groups with dedupe env-fp-1 = %d (err %v), want 1", n, err)
	}

	// Firing then resolved for the same fingerprint within one envelope:
	// the resolve event must find the group created earlier in the envelope.
	res2, err := eng.IngestAlertmanager(ctx, key, map[string]any{
		"receiver": "x", "status": "firing",
		"alerts": []any{amAlert("firing", "env-fp-2"), amAlert("resolved", "env-fp-2")},
	})
	if err != nil {
		t.Fatalf("fire+resolve envelope ingest failed: %v", err)
	}
	results2 := res2["results"].([]any)
	last := results2[1].(map[string]any)
	if last["result"] != "resolved" {
		t.Fatalf("second alert result = %#v, want resolved", last["result"])
	}
	resolvedGroup := last["group"].(map[string]any)
	if utils.StrVal(resolvedGroup, "status") != "resolved" {
		t.Fatalf("group status = %s, want resolved", utils.StrVal(resolvedGroup, "status"))
	}
}

// TestNotifyWakeWakesWaiter verifies the LISTEN/NOTIFY worker wake-up: a
// blocked WaitForWake must return true well before its timeout when
// NotifyWake fires.
func TestNotifyWakeWakesWaiter(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()

	// First call establishes the LISTEN connection (returns false on timeout).
	st.WaitForWake(ctx, 50*time.Millisecond)

	go func() {
		time.Sleep(150 * time.Millisecond)
		if err := st.NotifyWake(ctx); err != nil {
			t.Errorf("notify wake failed: %v", err)
		}
	}()

	start := time.Now()
	woke := st.WaitForWake(ctx, testDeadline(10*time.Second))
	elapsed := time.Since(start)
	if !woke {
		t.Fatal("WaitForWake timed out instead of being woken by NotifyWake")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("WaitForWake took %v, expected sub-second wake-up", elapsed)
	}
}

// TestDeadLetterFiredOnBatchContextLoss verifies that a batched notification
// whose delivery context cannot be rebuilt at flush time (no stored payload,
// no user — the escalation-webhook shape) is marked failed by
// ProcessNotificationBatches and produces a dead-letter event.
func TestDeadLetterFiredOnBatchContextLoss(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	var mu sync.Mutex
	var received []map[string]any
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			mu.Lock()
			received = append(received, body)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer dlSrv.Close()
	t.Setenv("NXS_ANOMALY_DEAD_LETTER_WEBHOOK_URL", dlSrv.URL)

	eng := engine.New(st)

	past := utils.ToISO(time.Now().UTC().Add(-time.Minute))
	now := utils.ToISO(time.Now().UTC())
	batch := map[string]any{
		"id":                 "nbat-ctx-loss",
		"batch_key":          "ctx-loss-key",
		"status":             "open",
		"flush_at":           past,
		"deadline_at":        past,
		"notification_count": 1,
		"created_at":         now,
		"updated_at":         now,
	}
	if err := st.UpsertItem(ctx, "notification_batches", batch); err != nil {
		t.Fatalf("upsert batch: %v", err)
	}
	ntf := map[string]any{
		"id":                "ntf-ctx-loss",
		"alert_group_id":    nil,
		"user_id":           nil,
		"channel":           "slack",
		"target":            "http://127.0.0.1:1/slack",
		"status":            "batched",
		"batch_id":          "nbat-ctx-loss",
		"batch_key":         "ctx-loss-key",
		"reason":            "batch context loss test",
		"retry_count":       0,
		"created_at":        now,
		"updated_at":        now,
		"waiting_for_batch": true,
	}
	if err := st.UpsertItem(ctx, "notifications", ntf); err != nil {
		t.Fatalf("upsert notification: %v", err)
	}

	var event map[string]any
	deadline := time.Now().Add(testDeadline(5 * time.Second))
	for time.Now().Before(deadline) {
		if _, err := eng.RunWorkerCycle(ctx); err != nil {
			t.Fatalf("worker cycle: %v", err)
		}
		mu.Lock()
		if len(received) > 0 {
			event = received[0]
		}
		mu.Unlock()
		if event != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if event == nil {
		t.Fatal("no dead-letter event received for batch context loss")
	}
	if event["event"] != "notification.failed" {
		t.Fatalf("dead-letter event = %#v", event["event"])
	}
	if utils.StrVal(event, "last_error") != "batch delivery context not found" {
		t.Fatalf("last_error = %#v", event["last_error"])
	}
	stored, err := st.GetItem(ctx, "notifications", "ntf-ctx-loss")
	if err != nil || stored == nil {
		t.Fatalf("get notification: %v / %v", stored, err)
	}
	if utils.StrVal(stored, "status") != "failed" {
		t.Fatalf("stored status = %s, want failed", utils.StrVal(stored, "status"))
	}
}

// TestArchivalDeletesLargeBacklogInBatches verifies that DeleteOldResolvedGroups
// handles a backlog larger than one archival batch (1000): previously all ids
// went into a single IN list, which would exceed PostgreSQL's 65535-parameter
// limit on a large enough backlog.
func TestArchivalDeletesLargeBacklogInBatches(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	conn, err := pgx.Connect(ctx, os.Getenv("NXS_ANOMALY_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx) //nolint:errcheck

	const backlog = 1100
	resolvedAt := utils.ToISO(time.Now().UTC().Add(-48 * time.Hour))
	if _, err := conn.Exec(ctx,
		`INSERT INTO nxs_anomaly_alert_groups (id, data, status, resolved_at)
		 SELECT 'arch-'||g,
		        jsonb_build_object('id', 'arch-'||g, 'status', 'resolved', 'resolved_at', $1::text),
		        'resolved', $1::timestamptz
		 FROM generate_series(1, $2::int) g`, resolvedAt, backlog); err != nil {
		t.Fatalf("seed expired groups: %v", err)
	}

	cutoff := utils.ToISO(time.Now().UTC().Add(-24 * time.Hour))
	deleted, err := st.DeleteOldResolvedGroups(ctx, cutoff)
	if err != nil {
		t.Fatalf("archival failed: %v", err)
	}
	if deleted != backlog {
		t.Fatalf("deleted = %d, want %d", deleted, backlog)
	}
	remaining, err := st.CountCollection(ctx, "alert_groups", nil)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining groups = %d, want 0", remaining)
	}
}

// TestMobileDashboardScopedReads covers GetMobileDashboard end-to-end: session
// lookup by token hash, the user's active groups (relevance via notification and via chain)
// and the on-call section, after the dashboard moved from full-collection
// reads to point/typed-index queries.
func TestMobileDashboardScopedReads(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	user, err := eng.CreateUser(ctx, map[string]any{
		"name": "Dash User", "username": "dash-user",
		"notification_targets": []any{map[string]any{"type": "log", "target": ""}},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID := utils.StrVal(user, "id")

	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "dash-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{userID}}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "dash-integration",
		"routes": []any{map[string]any{
			"name": "all", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}

	now := time.Now().UTC()
	if _, err := eng.CreateSchedule(ctx, map[string]any{
		"name": "dash-schedule",
		"shifts": []any{map[string]any{
			"user_id":  userID,
			"start_at": utils.ToISO(now.Add(-time.Hour)),
			"end_at":   utils.ToISO(now.Add(time.Hour)),
		}},
	}); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	device, err := eng.RegisterMobileDevice(ctx, map[string]any{
		"user_id": userID, "platform": "ios", "push_token": "pt", "device_name": "phone",
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	session, err := eng.CreateMobileSession(ctx, map[string]any{
		"user_id": userID, "device_id": device["id"],
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token := utils.StrVal(session, "token")
	_, sessUser, err := eng.AuthenticateMobileSession(ctx, token)
	if err != nil || utils.StrVal(sessUser, "id") != userID {
		t.Fatalf("authenticate session: user=%v err=%v", sessUser, err)
	}

	res, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{
		"title": "dash alert", "labels": map[string]any{"alertname": "dash"},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	groupID := utils.StrVal(res["group"].(map[string]any), "id")

	dash, err := eng.GetMobileDashboard(ctx, userID)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	gotUser, _ := dash["user"].(map[string]any)
	if utils.StrVal(gotUser, "id") != userID {
		t.Fatalf("dashboard user = %#v, want %s", dash["user"], userID)
	}
	groups, _ := dash["assigned_alert_groups"].([]map[string]any)
	found := false
	for _, g := range groups {
		if utils.StrVal(g, "id") == groupID {
			found = true
		}
	}
	if !found {
		t.Fatalf("dashboard does not contain group %s: %#v", groupID, groups)
	}
	oncall, _ := dash["on_call"].([]map[string]any)
	if len(oncall) != 1 || utils.StrVal(oncall[0], "schedule_name") != "dash-schedule" {
		t.Fatalf("on_call = %#v, want dash-schedule entry", oncall)
	}

	if _, u, err := eng.AuthenticateMobileSession(ctx, "nxm_bogus"); err != nil || u != nil {
		t.Fatalf("unknown session token authenticated: user=%v err=%v", u, err)
	}

	// Resolve the group: it must disappear from the dashboard.
	if _, err := eng.ResolveGroup(ctx, groupID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	dash2, err := eng.GetMobileDashboard(ctx, userID)
	if err != nil {
		t.Fatalf("dashboard after resolve: %v", err)
	}
	if groups2, _ := dash2["assigned_alert_groups"].([]map[string]any); len(groups2) != 0 {
		t.Fatalf("resolved group still on dashboard: %#v", groups2)
	}
}

// TestUnresolveWakesWorker verifies that UnresolveGroup fires the
// LISTEN/NOTIFY worker wake-up: the transition sets next_run_at = now, so the
// worker must not wait out its poll interval.
func TestUnresolveWakesWorker(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	now := utils.ToISO(time.Now().UTC())
	group := map[string]any{
		"id": "wake-group-1", "status": "resolved", "alert_count": 1,
		"resolved_at": now, "created_at": now, "updated_at": now,
	}
	if err := st.UpsertItem(ctx, "alert_groups", group); err != nil {
		t.Fatalf("upsert group: %v", err)
	}

	// First call establishes the LISTEN connection (returns false on timeout).
	st.WaitForWake(ctx, 50*time.Millisecond)

	go func() {
		time.Sleep(150 * time.Millisecond)
		if _, err := eng.UnresolveGroup(ctx, "wake-group-1"); err != nil {
			t.Errorf("unresolve failed: %v", err)
		}
	}()

	start := time.Now()
	woke := st.WaitForWake(ctx, testDeadline(10*time.Second))
	if !woke {
		t.Fatal("WaitForWake timed out: UnresolveGroup did not notify the worker")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("wake took %v, expected sub-second", elapsed)
	}
}

// TestChatopsOncallAndDebugRoutePreview covers the /oncall chatops command and
// the DebugRoute preview after both moved off full-collection reads.
func TestChatopsOncallAndDebugRoutePreview(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	user, err := eng.CreateUser(ctx, map[string]any{"name": "Oncall User", "username": "oncall-user"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID := utils.StrVal(user, "id")
	now := time.Now().UTC()
	sched, err := eng.CreateSchedule(ctx, map[string]any{
		"name": "oncall-schedule",
		"shifts": []any{map[string]any{
			"user_id":  userID,
			"start_at": utils.ToISO(now.Add(-time.Hour)),
			"end_at":   utils.ToISO(now.Add(time.Hour)),
		}},
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	channel, err := eng.CreateChatopsChannel(ctx, map[string]any{"name": "oncall-slack", "platform": "slack"})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	msg, err := eng.PostChatopsCommand(ctx, map[string]any{
		"channel_id": utils.StrVal(channel, "id"),
		"command":    "/oncall " + utils.StrVal(sched, "id"),
	})
	if err != nil {
		t.Fatalf("oncall command: %v", err)
	}
	resp, _ := msg["response"].(map[string]any)
	if text := utils.StrVal(resp, "text"); text != "On-call now: oncall-user" {
		t.Fatalf("oncall text = %q", text)
	}

	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "debug-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{userID}}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "debug-integration",
		"routes": []any{map[string]any{
			"name": "all", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}

	preview, err := eng.DebugRoute(ctx, utils.StrVal(integ, "key"), map[string]any{
		"title": "debug alert", "labels": map[string]any{"alertname": "debug"},
	})
	if err != nil {
		t.Fatalf("debug route: %v", err)
	}
	previews, _ := preview["notification_preview"].([]map[string]any)
	found := false
	for _, p := range previews {
		if utils.StrVal(p, "user_id") == userID {
			found = true
		}
	}
	if !found {
		t.Fatalf("notification_preview lacks user %s: %#v", userID, previews)
	}
}

// TestMultiWorkerNoDuplicateDelivery verifies that several worker replicas
// claiming and delivering concurrently against the same database deliver each
// notification exactly once. This is the guarantee behind the lock-free save
// (P1.2) and write-all (P2.1) changes, which rely on the per-row claim
// (FOR UPDATE SKIP LOCKED) — not an advisory lock — for exclusivity.
func TestMultiWorkerNoDuplicateDelivery(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	var hits int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	const n = 40
	now := utils.ToISO(utils.UTCNow())
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("ntf-mw-%02d", i)
		if err := st.UpsertItem(ctx, "notifications", map[string]any{
			// alert_group_id left unset (NULL) to satisfy the notifications→
			// alert_groups FK without seeding a group — webhook delivery needs only
			// the payload, not group context.
			"id":              id,
			"channel":         "webhook",
			"target":          ts.URL,
			"status":          "delivery_scheduled",
			"idempotency_key": id,
			"payload":         map[string]any{"integration_id": "int-mw"},
			"created_at":      now,
			"updated_at":      now,
		}); err != nil {
			t.Fatalf("seed notification %s: %v", id, err)
		}
	}

	// Several engines share the store; each gets its own workerID via engine.New.
	const workers = 4
	engs := []*engine.Engine{eng}
	for i := 1; i < workers; i++ {
		engs = append(engs, engine.New(st))
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, e := range engs {
		wg.Add(1)
		go func(e *engine.Engine) {
			defer wg.Done()
			<-start
			// Drain: keep claiming/delivering until nothing is left to claim.
			for {
				out, err := e.ProcessNotificationDeliveries(ctx)
				if err != nil {
					t.Errorf("ProcessNotificationDeliveries: %v", err)
					return
				}
				if len(out) == 0 {
					return
				}
			}
		}(e)
	}
	close(start) // release all workers together to maximize claim contention
	wg.Wait()

	if got := atomic.LoadInt64(&hits); got != n {
		t.Errorf("webhook hits = %d, want %d (duplicate or missing delivery)", got, n)
	}
	delivered := 0
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("ntf-mw-%02d", i)
		item, err := st.GetItem(ctx, "notifications", id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if item != nil && utils.StrVal(item, "status") == "delivered" {
			delivered++
		}
	}
	if delivered != n {
		t.Errorf("delivered notifications = %d, want %d", delivered, n)
	}
}

// TestBacklogAgeMetrics exercises the lag gauges' store queries (P2.E) against a
// real database: a group whose next_run_at is in the past reports a positive
// escalation age, and the pending-delivery query runs cleanly.
func TestBacklogAgeMetrics(t *testing.T) {
	ctx := context.Background()
	st, _ := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	// Caught up: both ages are zero.
	if age, err := st.OldestDueEscalationAgeSeconds(ctx); err != nil || age != 0 {
		t.Fatalf("escalation age (empty) = %v, err %v; want 0", age, err)
	}
	if age, err := st.OldestPendingDeliveryAgeSeconds(ctx); err != nil || age != 0 {
		t.Fatalf("pending delivery age (empty) = %v, err %v; want 0", age, err)
	}

	past := utils.ToISO(utils.UTCNow().Add(-10 * time.Minute))
	if err := st.UpsertItem(ctx, "alert_groups", map[string]any{
		"id": "ag-lag", "status": "open", "next_run_at": past,
		"created_at": past, "updated_at": past,
	}); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	age, err := st.OldestDueEscalationAgeSeconds(ctx)
	if err != nil {
		t.Fatalf("escalation age: %v", err)
	}
	if age < 60 { // ~600s expected; assert clearly positive
		t.Errorf("escalation age = %v, want a positive backlog (~600s)", age)
	}
}

// TestMultiWorkerShardedEscalation verifies that escalation sharding (per-shard
// advisory lock) lets multiple worker replicas escalate different integrations
// concurrently while advancing each due group exactly once — no double-processing
// and no starvation.
func TestMultiWorkerShardedEscalation(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	const n = 4 // distinct integrations → distinct escalation shards
	var groupIDs []string
	for i := 0; i < n; i++ {
		chain, err := eng.CreateEscalationChain(ctx, map[string]any{
			"name": fmt.Sprintf("shard-chain-%d", i),
			// Two WAITs: ingest stops at the first; resuming advances to the second
			// (re-scheduling next_run_at) and appends one "escalation_resumed" log.
			"steps": []any{
				map[string]any{"kind": "WAIT", "delay_minutes": 5},
				map[string]any{"kind": "WAIT", "delay_minutes": 5},
			},
		})
		if err != nil {
			t.Fatalf("create chain %d: %v", i, err)
		}
		integ, err := eng.CreateIntegration(ctx, map[string]any{
			"name":     fmt.Sprintf("shard-integ-%d", i),
			"group_by": []any{"alertname"},
			"routes": []any{map[string]any{
				"name": "all", "match_type": "all", "is_default": true,
				"escalation_chain_id": chain["id"],
			}},
		})
		if err != nil {
			t.Fatalf("create integration %d: %v", i, err)
		}
		ing, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{
			"title":  fmt.Sprintf("shard alert %d", i),
			"labels": map[string]any{"alertname": fmt.Sprintf("Shard%d", i)},
		})
		if err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
		gid := utils.StrVal(ing["group"].(map[string]any), "id")
		groupIDs = append(groupIDs, gid)
		// Backdate next_run_at so the group is due for escalation now.
		grp, err := st.GetItem(ctx, "alert_groups", gid)
		if err != nil {
			t.Fatalf("get group %d: %v", i, err)
		}
		grp["next_run_at"] = utils.ToISO(time.Now().UTC().Add(-time.Minute))
		if err := st.UpsertItem(ctx, "alert_groups", grp); err != nil {
			t.Fatalf("backdate group %d: %v", i, err)
		}
	}

	// Several engines (distinct workerIDs, shared store) escalate concurrently.
	const workers = 4
	engs := []*engine.Engine{eng}
	for i := 1; i < workers; i++ {
		engs = append(engs, engine.New(st))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, e := range engs {
		wg.Add(1)
		go func(e *engine.Engine) {
			defer wg.Done()
			<-start
			if _, err := e.ProcessDueEscalations(ctx); err != nil {
				t.Errorf("ProcessDueEscalations: %v", err)
			}
		}(e)
	}
	close(start) // release all workers together to race for shards
	wg.Wait()

	// Each group must have advanced exactly once: a single "escalation_resumed"
	// log entry. >1 means two workers processed the same shard (lock failed); 0
	// means the group was starved.
	for _, gid := range groupIDs {
		grp, err := st.GetItem(ctx, "alert_groups", gid)
		if err != nil {
			t.Fatalf("get %s: %v", gid, err)
		}
		logs, _ := grp["logs"].([]any)
		resumed := 0
		for _, l := range logs {
			if m, ok := l.(map[string]any); ok && utils.StrVal(m, "type") == "escalation_resumed" {
				resumed++
			}
		}
		if resumed != 1 {
			t.Errorf("group %s: escalation_resumed=%d, want exactly 1", gid, resumed)
		}
	}
}

// TestScheduleRotationRoundTripAndPreview proves Schedule v2 survives the
// PostgreSQL round trip: the rotation is stored as JSONB and read back by the
// on-call and preview paths, overrides can be edited and deleted, and a
// schedule with gaps cannot be attached to a chain by accident.
func TestScheduleRotationRoundTripAndPreview(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	alice, err := eng.CreateUser(ctx, map[string]any{"name": "Rota Alice", "username": "rota-alice"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	bob, err := eng.CreateUser(ctx, map[string]any{"name": "Rota Bob", "username": "rota-bob"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	start := time.Now().UTC().Add(-time.Hour)

	sched, err := eng.CreateSchedule(ctx, map[string]any{
		"name":     "rotation-schedule",
		"timezone": "Europe/Berlin",
		"rotation": map[string]any{
			"start_at":         utils.ToISO(start),
			"handoff_interval": 1,
			"handoff_unit":     "days",
			"participant_ids":  []any{alice["id"], bob["id"]},
		},
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	schedID := utils.StrVal(sched, "id")

	stored, err := eng.GetItem(ctx, "schedules", schedID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	rot, ok := stored["rotation"].(map[string]any)
	if !ok || utils.StrVal(rot, "handoff_unit") != "days" {
		t.Fatalf("rotation not persisted: %#v", stored["rotation"])
	}

	oncall, err := eng.GetScheduleOncall(ctx, schedID, "")
	if err != nil {
		t.Fatalf("on-call: %v", err)
	}
	if utils.StrVal(oncall, "source") != "rotation" {
		t.Fatalf("source = %v, want rotation", oncall["source"])
	}
	ids, _ := oncall["user_ids"].([]string)
	if len(ids) != 1 || ids[0] != utils.StrVal(alice, "id") {
		t.Fatalf("on-call user_ids = %v, want alice", ids)
	}

	preview, err := eng.PreviewSchedule(ctx, schedID, "", "")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if gaps, _ := preview["gaps"].([]any); len(gaps) != 0 {
		t.Fatalf("gaps = %v, want none", gaps)
	}
	if segments, _ := preview["segments"].([]any); len(segments) < 27 {
		t.Fatalf("segments = %d, want ~28 daily handoffs", len(segments))
	}

	// Override lifecycle: create, edit, delete, all persisted.
	created, err := eng.CreateScheduleOverride(ctx, schedID, map[string]any{
		"user_id": bob["id"],
		"until":   utils.ToISO(time.Now().UTC().Add(2 * time.Hour)),
		"reason":  "alice at the dentist",
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}
	overrideID := utils.StrVal(created["override"].(map[string]any), "id")

	oncall, err = eng.GetScheduleOncall(ctx, schedID, "")
	if err != nil {
		t.Fatalf("on-call during override: %v", err)
	}
	if utils.StrVal(oncall, "source") != "override" {
		t.Fatalf("source during override = %v", oncall["source"])
	}

	if _, err := eng.UpdateScheduleOverride(ctx, schedID, overrideID, map[string]any{
		"reason": "dentist, extended",
		"until":  utils.ToISO(time.Now().UTC().Add(4 * time.Hour)),
	}); err != nil {
		t.Fatalf("update override: %v", err)
	}
	stored, _ = eng.GetItem(ctx, "schedules", schedID)
	overrides, _ := stored["overrides"].([]any)
	if len(overrides) != 1 {
		t.Fatalf("overrides = %#v", stored["overrides"])
	}
	if got := utils.StrVal(overrides[0].(map[string]any), "reason"); got != "dentist, extended" {
		t.Fatalf("override reason = %q", got)
	}

	if _, err := eng.DeleteScheduleOverride(ctx, schedID, overrideID); err != nil {
		t.Fatalf("delete override: %v", err)
	}
	stored, _ = eng.GetItem(ctx, "schedules", schedID)
	if overrides, _ := stored["overrides"].([]any); len(overrides) != 0 {
		t.Fatalf("overrides after delete = %#v", overrides)
	}

	// Coverage gate: an empty schedule is refused by a chain until accepted.
	empty, err := eng.CreateSchedule(ctx, map[string]any{"name": "empty-schedule"})
	if err != nil {
		t.Fatalf("create empty schedule: %v", err)
	}
	if _, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "gap-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_SCHEDULE", "schedule_id": empty["id"]}},
	}); err == nil {
		t.Fatal("expected uncovered schedule to be refused")
	}
	if _, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name": "gap-chain",
		"steps": []any{map[string]any{
			"kind": "NOTIFY_SCHEDULE", "schedule_id": empty["id"], "allow_uncovered": true,
		}},
	}); err != nil {
		t.Fatalf("acknowledged attach failed: %v", err)
	}
	if _, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "covered-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_SCHEDULE", "schedule_id": schedID}},
	}); err != nil {
		t.Fatalf("covered schedule refused: %v", err)
	}
}

// TestScheduleCoverageDriftAndShiftNotifications covers the three things the
// write-time gate cannot: a schedule that drifts into gaps after its chain was
// saved, a participant who is deleted afterwards, and the handoff notification
// that has to survive a real store round trip.
func TestScheduleCoverageDriftAndShiftNotifications(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	alice, err := eng.CreateUser(ctx, map[string]any{
		"name": "Drift Alice", "username": "drift-alice",
		"notification_targets": []any{map[string]any{"type": "webhook", "target": "http://127.0.0.1:1/hook"}},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	bob, err := eng.CreateUser(ctx, map[string]any{
		"name": "Drift Bob", "username": "drift-bob",
		"notification_targets": []any{map[string]any{"type": "webhook", "target": "http://127.0.0.1:1/hook"}},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	sched, err := eng.CreateSchedule(ctx, map[string]any{
		"name":                   "drift-schedule",
		"notify_on_shift_change": true,
		"rotation": map[string]any{
			"start_at":         utils.ToISO(time.Now().UTC().Add(-30 * time.Minute)),
			"handoff_interval": 1,
			"handoff_unit":     "hours",
			"participant_ids":  []any{alice["id"], bob["id"]},
		},
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	schedID := utils.StrVal(sched, "id")

	if _, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "drift-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_SCHEDULE", "schedule_id": schedID}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}

	// Baseline: the chain saved cleanly, so nothing is degraded.
	report, err := eng.ScheduleCoverageReport(ctx)
	if err != nil {
		t.Fatalf("coverage report: %v", err)
	}
	if report["degraded_attached"].(int) != 0 {
		t.Fatalf("degraded at baseline: %#v", report["items"])
	}

	// First cycle records the roster silently.
	if n, err := eng.ProcessScheduleShiftNotifications(ctx); err != nil || n != 0 {
		t.Fatalf("baseline shift cycle: n=%d err=%v", n, err)
	}
	// Move the rotation start back so the next cycle observes a handoff.
	if _, err := eng.UpdateSchedule(ctx, schedID, map[string]any{
		"rotation": map[string]any{
			"start_at":         utils.ToISO(time.Now().UTC().Add(-90 * time.Minute)),
			"handoff_interval": 1,
			"handoff_unit":     "hours",
			"participant_ids":  []any{alice["id"], bob["id"]},
		},
	}); err != nil {
		t.Fatalf("update schedule: %v", err)
	}
	sent, err := eng.ProcessScheduleShiftNotifications(ctx)
	if err != nil {
		t.Fatalf("handoff cycle: %v", err)
	}
	if sent != 1 {
		t.Fatalf("shift notifications = %d, want 1", sent)
	}
	notifications, err := st.ListCollection(ctx, "notifications")
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	var shiftNtf map[string]any
	for _, n := range notifications {
		if utils.StrVal(n, "reason") == "on-call shift started" {
			shiftNtf = n
		}
	}
	if shiftNtf == nil {
		t.Fatalf("no shift notification stored: %#v", notifications)
	}
	if got := shiftNtf["alert_group_id"]; got != nil && got != "" {
		t.Errorf("shift notification carries a group: %v", got)
	}

	// Drift 1: a participant leaves. Deleting someone still on the rotation is
	// refused (the slot would page nobody); taking them off the rotation is the
	// explicit way, and the schedule keeps the rest.
	if _, err := eng.DeleteEntity(ctx, "users", utils.StrVal(bob, "id")); !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("delete user on the rotation = %v, want a conflict", err)
	}
	if _, err := eng.UpdateSchedule(ctx, schedID, map[string]any{
		"rotation": map[string]any{
			"start_at":         utils.ToISO(time.Now().UTC().Add(-90 * time.Minute)),
			"handoff_interval": 1,
			"handoff_unit":     "hours",
			"participant_ids":  []any{alice["id"]},
		},
	}); err != nil {
		t.Fatalf("remove participant: %v", err)
	}
	if _, err := eng.DeleteEntity(ctx, "users", utils.StrVal(bob, "id")); err != nil {
		t.Fatalf("delete user after removing them from the rotation: %v", err)
	}
	stored, err := eng.GetItem(ctx, "schedules", schedID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	rot := stored["rotation"].(map[string]any)
	ids, _ := utils.CoerceStringList(rot["participant_ids"])
	if len(ids) != 1 || ids[0] != utils.StrVal(alice, "id") {
		t.Errorf("participants after delete = %v", ids)
	}

	// Drift 2: disable the schedule the chain pages through.
	if _, err := eng.UpdateSchedule(ctx, schedID, map[string]any{"enabled": false}); err != nil {
		t.Fatalf("disable schedule: %v", err)
	}
	report, err = eng.ScheduleCoverageReport(ctx)
	if err != nil {
		t.Fatalf("coverage report: %v", err)
	}
	if report["degraded_attached"].(int) != 1 {
		t.Fatalf("degraded_attached = %v after drift, want 1", report["degraded_attached"])
	}
}

// TestChannelHonestyEndToEnd proves the delivery contract against a real
// database: a channel with no transport ends up skipped (not delivered, not
// retried), a configured one ends up delivered with the provider's own code
// recorded, and both are visible in the attempts table.
func TestChannelHonestyEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	var relayCalls int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&relayCalls, 1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"queued":true,"token":"should-be-redacted"}`))
	}))
	defer relay.Close()

	user, err := eng.CreateUser(ctx, map[string]any{
		"name": "Honest User", "username": "honest-user",
		"notification_targets": []any{map[string]any{"type": "log"}},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID := utils.StrVal(user, "id")

	// A device with no relay configured: its notification must be skipped.
	if _, err := eng.RegisterMobileDevice(ctx, map[string]any{
		"user_id": userID, "platform": "ios", "push_token": "tok-1", "device_name": "phone",
	}); err != nil {
		t.Fatalf("register device: %v", err)
	}
	// A chatops channel with no webhook: same.
	if _, err := eng.CreateChatopsChannel(ctx, map[string]any{
		"name": "#sre", "platform": "slack", "user_id": userID,
	}); err != nil {
		t.Fatalf("create chatops channel: %v", err)
	}

	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "honesty-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{userID}}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "honesty-integration",
		"routes": []any{map[string]any{
			"name": "all", "match_type": "all", "is_default": true,
			"escalation_chain_id": chain["id"],
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	if _, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{
		"title": "honesty alert", "labels": map[string]any{"alertname": "honesty"},
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := eng.RunWorkerCycle(ctx); err != nil {
		t.Fatalf("worker cycle: %v", err)
	}

	notifications, err := st.ListCollection(ctx, "notifications")
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	byChannel := map[string]map[string]any{}
	for _, n := range notifications {
		byChannel[utils.StrVal(n, "channel")] = n
	}

	mobileNtf := byChannel["mobile"]
	if mobileNtf == nil {
		t.Fatalf("no mobile notification created: %#v", notifications)
	}
	if got := utils.StrVal(mobileNtf, "status"); got != "skipped" {
		t.Errorf("mobile status = %q, want skipped (delivered would be a lie)", got)
	}
	if got := utils.StrVal(mobileNtf, "provider_status"); got != "not_configured" {
		t.Errorf("mobile provider_status = %q", got)
	}
	if mobileNtf["next_retry_at"] != nil {
		t.Errorf("a skip must not schedule a retry: %v", mobileNtf["next_retry_at"])
	}
	chatopsNtf := byChannel["chatops"]
	if chatopsNtf == nil || utils.StrVal(chatopsNtf, "status") != "skipped" {
		t.Errorf("chatops notification = %#v, want skipped", chatopsNtf)
	}
	if got := utils.StrVal(byChannel["log"], "status"); got != "delivered" {
		t.Errorf("log status = %q, want delivered", got)
	}

	// Now configure the relay and page the same device again: this time a real
	// provider answers, so the notification is genuinely delivered. Delivery
	// config is read from the environment at construction, so this is a second
	// engine on the same store rather than a setter that exists only for tests.
	t.Setenv("NXS_ANOMALY_MOBILE_PUSH_URL", relay.URL)
	eng = engine.New(st)
	if _, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"), map[string]any{
		"title": "honesty alert 2", "labels": map[string]any{"alertname": "honesty-2"},
	}); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if _, err := eng.RunWorkerCycle(ctx); err != nil {
		t.Fatalf("second worker cycle: %v", err)
	}
	if atomic.LoadInt32(&relayCalls) == 0 {
		t.Fatal("push relay was never called")
	}

	notifications, _ = st.ListCollection(ctx, "notifications")
	var deliveredMobile map[string]any
	for _, n := range notifications {
		if utils.StrVal(n, "channel") == "mobile" && utils.StrVal(n, "status") == "delivered" {
			deliveredMobile = n
		}
	}
	if deliveredMobile == nil {
		t.Fatalf("no delivered mobile notification after configuring the relay")
	}
	if got := utils.StrVal(deliveredMobile, "provider_status"); got != "mobile_push" {
		t.Errorf("provider_status = %q, want mobile_push", got)
	}

	attempts, err := st.ListCollection(ctx, "notification_delivery_attempts")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	var pushAttempt map[string]any
	for _, a := range attempts {
		if utils.StrVal(a, "notification_id") == utils.StrVal(deliveredMobile, "id") {
			pushAttempt = a
		}
	}
	if pushAttempt == nil {
		t.Fatalf("no delivery attempt recorded for the push")
	}
	if code, ok := pushAttempt["provider_code"].(float64); !ok || int(code) != http.StatusAccepted {
		t.Errorf("provider_code = %v, want 202", pushAttempt["provider_code"])
	}
	if _, ok := pushAttempt["duration_ms"]; !ok {
		t.Error("attempt has no duration_ms")
	}
	if resp := utils.StrVal(pushAttempt, "provider_response"); strings.Contains(resp, "should-be-redacted") {
		t.Errorf("provider response was stored unredacted: %q", resp)
	}
	// A skipped channel also leaves an attempt row, so "why did nobody get
	// paged" is answerable from the diagnostics alone.
	var skipAttempt map[string]any
	for _, a := range attempts {
		if utils.StrVal(a, "status") == "skipped" {
			skipAttempt = a
		}
	}
	if skipAttempt == nil {
		t.Error("no attempt row recorded for a skipped notification")
	}
}

// testTimeoutScale multiplies every wall-clock deadline in the suite.
//
// The deadlines are tuned for a PostgreSQL sitting next to the test process. A
// database one network hop away is enough to blow them: measured against a
// cluster PostgreSQL over a forwarded port, a single query costs ~6.6ms instead
// of ~0.6ms, and the whole suite takes 81s instead of 22s. Tests then fail on
// the clock rather than on behaviour, and the failure reads like a product bug.
// CI never sees this because it runs PostgreSQL as a co-located service, while
// a production deployment talks to a managed instance — so the environment the
// suite is least prepared for is the one the product actually runs in.
//
// Set NXS_ANOMALY_TEST_TIMEOUT_SCALE (e.g. "4") to stretch them.
func testTimeoutScale() float64 {
	raw := os.Getenv("NXS_ANOMALY_TEST_TIMEOUT_SCALE")
	if raw == "" {
		return 1
	}
	scale, err := strconv.ParseFloat(raw, 64)
	if err != nil || scale <= 0 {
		return 1
	}
	return scale
}

// testDeadline scales one deadline. Always use it instead of a bare duration
// literal for a "wait until this becomes true" bound.
func testDeadline(d time.Duration) time.Duration {
	return time.Duration(float64(d) * testTimeoutScale())
}
