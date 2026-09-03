//go:build loadtest

// Command loadtest is the BETA-022 capacity/chaos harness. It drives the engine
// against a real PostgreSQL with fixed load profiles and optional fault
// injection, then reports ingest→first-attempt latency percentiles and whether
// every notification survived. It is behind the `loadtest` build tag so it never
// compiles into the normal binary or `go test ./...`.
//
// Usage (see tests/run_load_test.sh, which provisions PostgreSQL):
//
//	go run -tags loadtest ./tests/loadtest \
//	  -alerts 2000 -rate 200 -workers 2 -recipients 3 -scenario baseline
//
// Scenarios:
//
//	baseline         steady load, healthy provider
//	provider_timeout ~20% of deliveries are slow (2s) — latency under a slow provider
//	worker_kill      one worker is killed mid-run — no notification may be lost
//	retry_storm      provider returns 500 for the first third — everything must still be delivered
//
// Exit code is non-zero when an acceptance criterion fails (p95 ≥ 10s, a lost or
// unexplained duplicate notification, or a backlog that never drains), so it
// works as a CI gate as well as a report.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// titlePrefix carries the ingest wall-clock nanoseconds so the stub can compute
// latency without a shared map: title is "ls:<unixnano>:<seq>".
const titlePrefix = "ls:"

type stub struct {
	mu        sync.Mutex
	firstSeen map[string]time.Time // alert_group_id → first delivery time
	count     map[string]int       // alert_group_id → deliveries (for double-delivery detection)
	latencies []time.Duration

	failUntil atomic.Int64 // unix nanos; deliveries before this return 500 (retry_storm)
	slowEvery atomic.Int64 // 1-in-N deliveries sleep slowDur (provider_timeout); 0 disables
	reqSeq    atomic.Int64
}

func newStub() *stub {
	return &stub{firstSeen: map[string]time.Time{}, count: map[string]int{}}
}

const slowDur = 2 * time.Second

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	// User webhook deliveries use the Alertmanager envelope
	// (toAlertmanagerPayload): the group id is groupKey and the embedded ingest
	// timestamp rides in groupLabels.alertname (defaulted from the alert title).
	gid := utils.StrVal(body, "groupKey")
	title := ""
	if gl, ok := body["groupLabels"].(map[string]any); ok {
		title = utils.StrVal(gl, "alertname")
	}

	if until := s.failUntil.Load(); until != 0 && now.UnixNano() < until {
		http.Error(w, "injected failure", http.StatusInternalServerError)
		return
	}
	if every := s.slowEvery.Load(); every > 0 && s.reqSeq.Add(1)%every == 0 {
		time.Sleep(slowDur)
	}

	s.mu.Lock()
	s.count[gid]++
	if _, ok := s.firstSeen[gid]; !ok {
		s.firstSeen[gid] = now
		if ingest, ok := parseIngestNanos(title); ok {
			s.latencies = append(s.latencies, now.Sub(time.Unix(0, ingest)))
		}
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func parseIngestNanos(title string) (int64, bool) {
	if len(title) < len(titlePrefix) || title[:len(titlePrefix)] != titlePrefix {
		return 0, false
	}
	rest := title[len(titlePrefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == ':' {
			n, err := strconv.ParseInt(rest[:i], 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

func main() {
	alerts := flag.Int("alerts", 1000, "total alerts to ingest")
	rate := flag.Float64("rate", 100, "ingest rate, alerts/sec")
	workers := flag.Int("workers", 2, "number of concurrent worker engines")
	recipients := flag.Int("recipients", 3, "users notified per alert")
	integrations := flag.Int("integrations", 1, "spread the alerts across this many integrations")
	scenario := flag.String("scenario", "baseline", "baseline|provider_timeout|worker_kill|retry_storm")
	drainTimeout := flag.Duration("drain-timeout", 60*time.Second, "max time to wait for the backlog to drain")
	flag.Parse()

	dsn := os.Getenv("NXS_ANOMALY_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("NXS_ANOMALY_DB_DSN")
	}
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "set NXS_ANOMALY_TEST_DATABASE_URL (or NXS_ANOMALY_DB_DSN)")
		os.Exit(2)
	}
	_ = os.Setenv("NXS_ANOMALY_DB_DSN", dsn)

	// worker_kill strands the killed worker's in-flight claims in 'delivering',
	// and only the reaper returns them. The default ClaimTimeout is floored at
	// five minutes (see defaultClaimTimeout), which is longer than any drain
	// budget a profile can reasonably carry — so with it the scenario can only
	// pass when the kill happens to land between deliveries, i.e. when it
	// strands nothing and never exercises the reclaim path it exists to test.
	// Deliveries here are a local httptest server, orders of magnitude below the
	// worst-case stage the default is derived from, so a short lease is safe.
	if *scenario == "worker_kill" && os.Getenv("NXS_ANOMALY_NOTIFICATION_CLAIM_TIMEOUT_SECONDS") == "" {
		_ = os.Setenv("NXS_ANOMALY_NOTIFICATION_CLAIM_TIMEOUT_SECONDS", "15")
	}

	ctx := context.Background()
	st, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		os.Exit(2)
	}
	defer st.Close()

	sh := newStub()
	srv := httptest.NewServer(sh)
	defer srv.Close()

	setupEng := engine.New(st)
	if err := setup(ctx, st, setupEng, srv.URL, *recipients, *integrations); err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		os.Exit(2)
	}
	integKeys := loadIntegrationKeys(ctx, setupEng, *integrations)

	// Configure the scenario's fault injection.
	switch *scenario {
	case "provider_timeout":
		sh.slowEvery.Store(5) // ~20%
	case "retry_storm":
		// Fail for the estimated first third of the ingest window.
		window := time.Duration(float64(*alerts) / *rate * float64(time.Second) / 3)
		sh.failUntil.Store(time.Now().Add(window).UnixNano())
	}

	// Start workers.
	workerCtx, cancelAll := context.WithCancel(ctx)
	var perWorkerCancel []context.CancelFunc
	var wg sync.WaitGroup
	// Every duplicate must be explained by a reclaim: a claim the reaper returned
	// had already been POSTed by the worker that died holding it, so re-sending it
	// is at-least-once working, not a defect. A duplicate with no reclaim behind
	// it is a double-claim bug. Counting reclaims is what lets the two be told
	// apart instead of forbidding both or excusing both.
	var reclaimed atomic.Int64
	for i := 0; i < *workers; i++ {
		wctx, wcancel := context.WithCancel(workerCtx)
		perWorkerCancel = append(perWorkerCancel, wcancel)
		e := engine.New(st)
		wg.Add(1)
		go func(e *engine.Engine, c context.Context) {
			defer wg.Done()
			for c.Err() == nil {
				res, err := e.RunWorkerCycle(c)
				if err != nil && c.Err() == nil {
					fmt.Fprintln(os.Stderr, "worker cycle:", err)
				}
				if n, ok := res["reclaimed_stale_claims"].(int); ok {
					reclaimed.Add(int64(n))
				}
				time.Sleep(2 * time.Millisecond)
			}
		}(e, wctx)
	}

	// Producer: ingest at the requested rate.
	start := time.Now()
	interval := time.Duration(float64(time.Second) / *rate)
	var ingestErrors int64
	for i := 0; i < *alerts; i++ {
		ts := time.Now()
		payload := map[string]any{
			"title":  fmt.Sprintf("%s%d:%d", titlePrefix, ts.UnixNano(), i),
			"labels": map[string]any{"seq": strconv.Itoa(i), "harness": "1"},
		}
		if _, err := setupEng.IngestAlert(ctx, integKeys[i%len(integKeys)], payload); err != nil {
			ingestErrors++
		}
		// worker_kill: take one worker down a third of the way in.
		if *scenario == "worker_kill" && i == *alerts/3 && len(perWorkerCancel) > 0 {
			perWorkerCancel[0]()
		}
		if d := interval - time.Since(ts); d > 0 {
			time.Sleep(d)
		}
	}
	ingestDone := time.Since(start)

	// Drain: keep workers running until every group has been delivered or timeout.
	//
	// All four in-flight statuses count, the two transient claims included.
	// 'retrying' used to be left out, and it is the status a notification sits in
	// for the whole of a retry attempt — so retry_storm could report a drained
	// backlog with 303 of 4500 still claimed for retry, and they stayed that way
	// once the workers were cancelled. A drain check blind to a claim status
	// cannot tell a finished backlog from a stalled one.
	inFlight := []string{"delivery_scheduled", "retry_scheduled", "delivering", "retrying"}
	drainDeadline := time.Now().Add(*drainTimeout)
	var drained bool
	for time.Now().Before(drainDeadline) {
		left := 0
		var err error
		for _, status := range inFlight {
			var n int
			if n, err = st.CountCollection(ctx, "notifications", map[string]any{"status": status}); err != nil {
				break
			}
			left += n
		}
		if err == nil && left == 0 {
			drained = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancelAll()
	wg.Wait()

	report := buildReport(ctx, st, sh, *alerts, *recipients, *scenario, ingestErrors, ingestDone, drained, reclaimed.Load())
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)

	if !report.Pass {
		os.Exit(1)
	}
}

type reportT struct {
	Scenario         string   `json:"scenario"`
	Alerts           int      `json:"alerts"`
	Recipients       int      `json:"recipients_per_alert"`
	ExpectedDeliv    int      `json:"expected_deliveries"`
	DeliveredGroups  int      `json:"delivered_groups"`
	TotalDeliveries  int      `json:"total_deliveries"`
	DoubleDeliveries int      `json:"double_deliveries"`
	DeliveredInStore int      `json:"delivered_in_store"`
	ReclaimedClaims  int64    `json:"reclaimed_stale_claims"`
	IngestErrors     int64    `json:"ingest_errors"`
	IngestSeconds    float64  `json:"ingest_seconds"`
	BacklogDrained   bool     `json:"backlog_drained"`
	P50Seconds       float64  `json:"latency_p50_seconds"`
	P95Seconds       float64  `json:"latency_p95_seconds"`
	P99Seconds       float64  `json:"latency_p99_seconds"`
	MaxSeconds       float64  `json:"latency_max_seconds"`
	Pass             bool     `json:"pass"`
	Failures         []string `json:"failures,omitempty"`
}

func buildReport(ctx context.Context, st store.PostgreSQLStore, sh *stub, alerts, recipients int, scenario string, ingestErrors int64, ingestDone time.Duration, drained bool, reclaimed int64) reportT {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	double := 0
	total := 0
	for _, n := range sh.count {
		total += n
		if n > recipients {
			double += n - recipients
		}
	}
	deliveredInStore, _ := st.CountCollection(ctx, "notifications", map[string]any{"status": "delivered"})

	lat := append([]time.Duration(nil), sh.latencies...)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pctl := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		idx := int(p * float64(len(lat)-1))
		return lat[idx].Seconds()
	}

	r := reportT{
		Scenario:         scenario,
		Alerts:           alerts,
		Recipients:       recipients,
		ExpectedDeliv:    alerts * recipients,
		DeliveredGroups:  len(sh.firstSeen),
		TotalDeliveries:  total,
		DoubleDeliveries: double,
		DeliveredInStore: deliveredInStore,
		ReclaimedClaims:  reclaimed,
		IngestErrors:     ingestErrors,
		IngestSeconds:    ingestDone.Seconds(),
		BacklogDrained:   drained,
		P50Seconds:       pctl(0.50),
		P95Seconds:       pctl(0.95),
		P99Seconds:       pctl(0.99),
	}
	if len(lat) > 0 {
		r.MaxSeconds = lat[len(lat)-1].Seconds()
	}

	// Acceptance criteria.
	//
	// Duplicates are judged against reclaims, not against zero. A worker killed
	// between the provider call and the save leaves a claim the reaper returns,
	// and the redelivery that follows is the at-least-once contract holding, not
	// a loss of exclusivity. Only a duplicate that no reclaim accounts for means
	// two workers delivered the same notification, and that is the bug worth
	// failing on. With no reclaims — every healthy profile — the bound is zero,
	// which is the criterion this replaced.
	if int64(r.DoubleDeliveries) > reclaimed {
		r.Failures = append(r.Failures, fmt.Sprintf("%d double deliveries, only %d reclaimed claims to account for them", r.DoubleDeliveries, reclaimed))
	}
	if !drained {
		r.Failures = append(r.Failures, "backlog did not drain within timeout")
	}
	if drained && r.DeliveredGroups != alerts {
		r.Failures = append(r.Failures, fmt.Sprintf("delivered %d/%d groups (lost notifications)", r.DeliveredGroups, alerts))
	}
	// The <10s p95 SLO is a steady-state target, so it is asserted only on the
	// healthy baseline profiles. The fault scenarios (provider_timeout,
	// retry_storm) deliberately park deliveries behind a slow provider or a retry
	// backoff — high latency for the affected fraction is the correct behaviour
	// there, and those profiles are judged on integrity instead: no loss, no
	// duplicates, and a backlog that drains (all checked above).
	if scenario == "baseline" && r.P95Seconds >= 10 {
		r.Failures = append(r.Failures, fmt.Sprintf("p95 %.2fs ≥ 10s", r.P95Seconds))
	}
	r.Pass = len(r.Failures) == 0
	return r
}

func setup(ctx context.Context, st store.PostgreSQLStore, eng *engine.Engine, stubURL string, recipients, integrations int) error {
	if err := st.ClearCollections(ctx, cleanupCollections); err != nil {
		return err
	}
	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "loadtest-chain",
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": []any{}}},
	})
	if err != nil {
		return err
	}
	userIDs := make([]any, 0, recipients)
	for i := 0; i < recipients; i++ {
		u, err := eng.CreateUser(ctx, map[string]any{
			"name":     fmt.Sprintf("Load User %d", i),
			"username": fmt.Sprintf("load-user-%d", i),
			"notification_targets": []any{
				map[string]any{"type": "webhook", "target": fmt.Sprintf("%s/u/%d", stubURL, i)},
			},
		})
		if err != nil {
			return err
		}
		userIDs = append(userIDs, u["id"])
	}
	if _, err := eng.UpdateEscalationChain(ctx, utils.StrVal(chain, "id"), map[string]any{
		"steps": []any{map[string]any{"kind": "NOTIFY_USER", "user_ids": userIDs}},
	}); err != nil {
		return err
	}
	// Ingest serialises per integration (see ingestLockKey) and the alert-group
	// lookup is scoped to one, so the number of integrations is the axis that
	// decides whether a profile measures one hot source or a fleet of them.
	// A single Alertmanager is one integration, which is why 1 stays the default.
	for i := 0; i < integrations; i++ {
		name := "loadtest-integration"
		if i > 0 {
			name = fmt.Sprintf("loadtest-integration-%d", i)
		}
		if _, err = eng.CreateIntegration(ctx, map[string]any{
			"name": name,
			"routes": []any{map[string]any{
				"name": "default", "match_type": "all", "is_default": true,
				"escalation_chain_id": chain["id"],
			}},
		}); err != nil {
			return err
		}
	}
	return nil
}

func loadIntegrationKeys(ctx context.Context, eng *engine.Engine, want int) []string {
	res, err := eng.ListCollectionPage(ctx, "integrations", map[string]any{"limit": want})
	if err != nil {
		fmt.Fprintln(os.Stderr, "list integrations:", err)
		os.Exit(2)
	}
	items, _ := res["items"].([]map[string]any)
	if len(items) < want {
		fmt.Fprintf(os.Stderr, "expected %d integrations, found %d\n", want, len(items))
		os.Exit(2)
	}
	keys := make([]string, 0, len(items))
	for _, it := range items {
		keys = append(keys, utils.StrVal(it, "key"))
	}
	return keys
}

// cleanupCollections mirrors the integration-test reset set.
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
	"grafana_plugins",
	"chatops_channels",
	"integrations",
	"schedules",
	"escalation_chains",
	"teams",
	"users",
}
