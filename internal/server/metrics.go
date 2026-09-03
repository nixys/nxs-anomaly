package server

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
)

// Metrics tracks prometheus counters and gauges for the service.
type Metrics struct {
	alertsIngested           prometheus.Counter
	notificationsDelivered   prometheus.Counter
	notificationsFailed      prometheus.Counter
	groupsArchived           prometheus.Counter
	staleClaimsReclaimed     prometheus.Counter
	workerCyclesTotal        prometheus.Counter
	workerCycleDuration      prometheus.Gauge
	workerLastCycleTimestamp prometheus.Gauge         // unix seconds of the last completed cycle
	deliveredByProvider      *prometheus.CounterVec   // successful deliveries, labeled by provider/channel
	dbUp                     prometheus.Gauge         // 1 when the last DB ping succeeded, else 0
	requestDuration          *prometheus.HistogramVec // labeled by handler category
	requestsTotal            *prometheus.CounterVec   // labeled by handler category + status code
	panicsRecovered          prometheus.Counter
	ingestDuration           *prometheus.HistogramVec // labeled by source (Item 5)
	deliveryErrorsByProvider *prometheus.CounterVec   // labeled by provider/channel
	deliveryDuration         *prometheus.HistogramVec // per-provider call latency, labeled by channel
	deadLetterEvents         *prometheus.CounterVec   // permanently-failed notifications, labeled by channel
	deliveryShortCircuited   *prometheus.CounterVec   // deliveries skipped by an open circuit breaker, by channel
	workerPanics             *prometheus.CounterVec   // panics recovered inside worker goroutines, by stage
	ingestErrors             *prometheus.CounterVec   // labeled by source
	cycleStageDuration       *prometheus.HistogramVec // worker-cycle stage latency, labeled by stage
	workerCyclesSkipped      prometheus.Counter       // cycles skipped because the previous one was still running
	// operational gauges — updated by worker cycle to avoid per-scrape DB queries (Item 2)
	alertGroupsOpen      prometheus.Gauge
	retryQueueDepth      prometheus.Gauge
	batchesPending       prometheus.Gauge
	kafkaOutboxDepth     prometheus.Gauge
	kafkaOutboxByTopic   *prometheus.GaugeVec   // outbox depth split by topic
	shiftNotifications   prometheus.Counter     // notifications sent for on-call handoffs
	notificationsSkipped *prometheus.CounterVec // notifications with no transport, by channel and reason
	retentionDeleted     *prometheus.CounterVec // rows deleted by the retention sweep, by category
	// schedule coverage — degraded schedules an escalation chain pages through.
	schedulesDegraded  prometheus.Gauge
	dutyOnCall         prometheus.Gauge
	sourcesSilent      prometheus.Gauge
	dutyWithoutCheckin prometheus.Gauge
	schedulesTotal     prometheus.Gauge
	// backlog-age gauges — how far behind the worker is (0 when caught up).
	oldestDueEscalationAge   prometheus.Gauge
	oldestPendingDeliveryAge prometheus.Gauge
	// DB connection pool gauges — refreshed per worker cycle.
	dbPoolAcquired      prometheus.Gauge
	dbPoolIdle          prometheus.Gauge
	dbPoolTotal         prometheus.Gauge
	dbPoolMax           prometheus.Gauge
	dbPoolEmptyAcquires prometheus.Gauge
	lastCycleAt         atomic.Value // string ISO timestamp
	cyclesCompleted     int64
	handler             http.Handler
}

func newMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	alertsIngested := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nxs_anomaly_alerts_ingested_total",
		Help: "Total number of ingested alerts.",
	})
	notificationsDelivered := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nxs_anomaly_notifications_delivered_total",
		Help: "Total number of delivered notifications.",
	})
	notificationsFailed := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nxs_anomaly_notifications_failed_total",
		Help: "Total number of permanently failed notifications.",
	})
	workerCyclesTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nxs_anomaly_worker_cycles_total",
		Help: "Total number of completed worker cycles.",
	})
	workerCycleDuration := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_worker_cycle_duration_seconds",
		Help: "Last worker cycle duration in seconds.",
	})
	workerLastCycleTimestamp := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_worker_last_cycle_timestamp_seconds",
		Help: "Unix timestamp (seconds) of the last completed worker cycle. Use time() - this for stall detection; the duration gauge is a duration, not a timestamp.",
	})
	deliveredByProvider := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nxs_anomaly_notifications_delivered_by_provider_total",
		Help: "Successfully delivered notifications per provider/channel. Paired with nxs_anomaly_delivery_errors_by_provider_total this gives the per-provider success ratio SLI.",
	}, []string{"provider"})
	dbUp := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_db_up",
		Help: "1 when the last database ping from this process succeeded, 0 otherwise.",
	})
	groupsArchived := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nxs_anomaly_groups_archived_total",
		Help: "Total number of resolved alert groups archived by the worker.",
	})
	staleClaimsReclaimed := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nxs_anomaly_stale_claims_reclaimed_total",
		Help: "Total notifications reclaimed from a stale 'delivering'/'retrying' claim by the reaper (a non-zero rate indicates worker crashes or claim contention).",
	})
	alertGroupsOpen := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_alert_groups_open",
		Help: "Number of currently open alert groups.",
	})
	retryQueueDepth := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_notifications_retry_queue_depth",
		Help: "Current number of notifications pending retry.",
	})
	batchesPending := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_notification_batches_pending",
		Help: "Current number of open notification batches.",
	})
	kafkaOutboxDepth := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_kafka_outbox_depth",
		Help: "Current number of undelivered events in the Kafka outbox.",
	})
	kafkaOutboxByTopic := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nxs_anomaly_kafka_outbox_depth_by_topic",
		Help: "Current number of undelivered Kafka outbox events per topic.",
	}, []string{"topic"})
	notificationsSkipped := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nxs_anomaly_notifications_skipped_total",
		Help: "Notifications that had no transport to go through, by channel and reason (not_configured, experimental). Never counted as delivered.",
	}, []string{"channel", "reason"})
	retentionDeleted := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nxs_anomaly_retention_deleted_total",
		Help: "Rows deleted by the retention sweep, by category (alert_groups, audit_events, chatops_messages, notifications, delivery_attempts, web_sessions). A category with a configured horizon and a flat counter is a sweep that has stopped running.",
	}, []string{"category"})
	shiftNotifications := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nxs_anomaly_shift_notifications_total",
		Help: "Total notifications sent because an on-call shift started.",
	})
	schedulesDegraded := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_schedules_degraded",
		Help: "Schedules an escalation chain pages through that have coverage gaps, are disabled, or name deleted users, without the step accepting that.",
	})
	schedulesTotal := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_schedules_total",
		Help: "Total number of schedules.",
	})
	sourcesSilent := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_sources_silent",
		Help: "Integrations with a declared heartbeat that have not reported within it. Silence from a source looks exactly like health until this is watched.",
	})
	dutyOnCall := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_duty_on_call",
		Help: "People a schedule currently puts on call.",
	})
	dutyWithoutCheckin := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_duty_without_checkin",
		Help: "People currently on call who have not confirmed the shift. Coverage says somebody is assigned; this says nobody answered.",
	})
	oldestDueEscalationAge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_oldest_due_escalation_age_seconds",
		Help: "Age of the oldest alert group past its next_run_at; rises when escalation falls behind.",
	})
	oldestPendingDeliveryAge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_oldest_pending_delivery_age_seconds",
		Help: "Age of the oldest delivery_scheduled notification; rises when delivery falls behind.",
	})
	dbPoolAcquired := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_db_pool_acquired_conns",
		Help: "Connections currently acquired from the pgx pool.",
	})
	dbPoolIdle := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_db_pool_idle_conns",
		Help: "Idle connections in the pgx pool.",
	})
	dbPoolTotal := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_db_pool_total_conns",
		Help: "Total connections currently in the pgx pool.",
	})
	dbPoolMax := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_db_pool_max_conns",
		Help: "Maximum connections the pgx pool may open.",
	})
	dbPoolEmptyAcquires := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nxs_anomaly_db_pool_empty_acquire_total",
		Help: "Cumulative acquires that had to wait because the pool was exhausted.",
	})
	requestDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nxs_anomaly_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds by handler category.",
		Buckets: prometheus.DefBuckets,
	}, []string{"handler"})
	requestsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nxs_anomaly_http_requests_total",
		Help: "Total HTTP requests by handler category and response status code.",
	}, []string{"handler", "code"})
	panicsRecovered := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nxs_anomaly_http_panics_recovered_total",
		Help: "Total HTTP handler panics recovered by the recovery middleware.",
	})
	ingestDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nxs_anomaly_ingest_duration_seconds",
		Help:    "Ingest handler latency in seconds by source.",
		Buckets: prometheus.DefBuckets,
	}, []string{"source"})
	deliveryErrorsByProvider := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nxs_anomaly_delivery_errors_by_provider_total",
		Help: "Total delivery errors per notification provider/channel.",
	}, []string{"provider"})
	deliveryDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nxs_anomaly_notification_delivery_duration_seconds",
		Help:    "Notification provider call latency in seconds by channel.",
		Buckets: prometheus.DefBuckets,
	}, []string{"channel"})
	deadLetterEvents := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nxs_anomaly_dead_letter_events_total",
		Help: "Total notifications that became permanently failed, by channel.",
	}, []string{"channel"})
	deliveryShortCircuited := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nxs_anomaly_delivery_short_circuited_total",
		Help: "Total deliveries skipped because the channel's circuit breaker was open.",
	}, []string{"channel"})
	workerPanics := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nxs_anomaly_worker_panics_total",
		Help: "Total panics recovered inside worker goroutines, by stage (deliveries, retries, dead_letter).",
	}, []string{"stage"})
	workerCyclesSkipped := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nxs_anomaly_worker_cycles_skipped_total",
		Help: "Total worker cycles skipped because the previous cycle was still running.",
	})
	ingestErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nxs_anomaly_ingest_errors_total",
		Help: "Total ingest errors by source (integration key not found, validation, internal).",
	}, []string{"source"})
	cycleStageDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nxs_anomaly_worker_cycle_stage_duration_seconds",
		Help:    "Worker-cycle stage latency in seconds (escalation, batches, deliveries, retries, archival, kafka).",
		Buckets: prometheus.DefBuckets,
	}, []string{"stage"})

	reg.MustRegister(
		alertsIngested,
		notificationsDelivered,
		notificationsFailed,
		groupsArchived,
		staleClaimsReclaimed,
		workerCyclesTotal,
		workerCycleDuration,
		workerLastCycleTimestamp,
		deliveredByProvider,
		dbUp,
		alertGroupsOpen,
		retryQueueDepth,
		batchesPending,
		kafkaOutboxDepth,
		kafkaOutboxByTopic,
		schedulesDegraded,
		dutyOnCall,
		sourcesSilent,
		dutyWithoutCheckin,
		schedulesTotal,
		shiftNotifications,
		notificationsSkipped,
		retentionDeleted,
		oldestDueEscalationAge,
		oldestPendingDeliveryAge,
		requestDuration,
		requestsTotal,
		panicsRecovered,
		ingestDuration,
		deliveryErrorsByProvider,
		deliveryDuration,
		deadLetterEvents,
		deliveryShortCircuited,
		workerPanics,
		ingestErrors,
		cycleStageDuration,
		workerCyclesSkipped,
		dbPoolAcquired,
		dbPoolIdle,
		dbPoolTotal,
		dbPoolMax,
		dbPoolEmptyAcquires,
	)

	return &Metrics{
		alertsIngested:           alertsIngested,
		notificationsDelivered:   notificationsDelivered,
		notificationsFailed:      notificationsFailed,
		groupsArchived:           groupsArchived,
		staleClaimsReclaimed:     staleClaimsReclaimed,
		workerCyclesTotal:        workerCyclesTotal,
		workerCycleDuration:      workerCycleDuration,
		workerLastCycleTimestamp: workerLastCycleTimestamp,
		deliveredByProvider:      deliveredByProvider,
		dbUp:                     dbUp,
		requestDuration:          requestDuration,
		requestsTotal:            requestsTotal,
		panicsRecovered:          panicsRecovered,
		ingestDuration:           ingestDuration,
		deliveryErrorsByProvider: deliveryErrorsByProvider,
		deliveryDuration:         deliveryDuration,
		deadLetterEvents:         deadLetterEvents,
		deliveryShortCircuited:   deliveryShortCircuited,
		workerPanics:             workerPanics,
		ingestErrors:             ingestErrors,
		cycleStageDuration:       cycleStageDuration,
		workerCyclesSkipped:      workerCyclesSkipped,
		alertGroupsOpen:          alertGroupsOpen,
		retryQueueDepth:          retryQueueDepth,
		batchesPending:           batchesPending,
		kafkaOutboxDepth:         kafkaOutboxDepth,
		kafkaOutboxByTopic:       kafkaOutboxByTopic,
		shiftNotifications:       shiftNotifications,
		notificationsSkipped:     notificationsSkipped,
		retentionDeleted:         retentionDeleted,
		schedulesDegraded:        schedulesDegraded,
		dutyOnCall:               dutyOnCall,
		sourcesSilent:            sourcesSilent,
		dutyWithoutCheckin:       dutyWithoutCheckin,
		schedulesTotal:           schedulesTotal,
		oldestDueEscalationAge:   oldestDueEscalationAge,
		oldestPendingDeliveryAge: oldestPendingDeliveryAge,
		dbPoolAcquired:           dbPoolAcquired,
		dbPoolIdle:               dbPoolIdle,
		dbPoolTotal:              dbPoolTotal,
		dbPoolMax:                dbPoolMax,
		dbPoolEmptyAcquires:      dbPoolEmptyAcquires,
		handler:                  promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
	}
}

// updateProcessGauges refreshes the gauges that describe THIS process rather than
// the cluster-wide backlog: DB reachability and its own pgx pool. Split out of
// updateOperationalGauges because an API process runs no worker cycle, and without
// a refresher its nxs_anomaly_db_up would sit at 0 forever — the DatabaseUnavailable
// alert reads that gauge from every process, API included, and would fire falsely.
func (m *Metrics) updateProcessGauges(s store.PostgreSQLStore) {
	ctx := context.Background()
	// DB reachability, exported so alerting can fire on an unreachable database
	// rather than only on the symptoms (a stalled worker, a growing backlog).
	if err := s.Ping(ctx); err == nil {
		m.dbUp.Set(1)
	} else {
		m.dbUp.Set(0)
	}
	ps := s.PoolStats()
	m.dbPoolAcquired.Set(float64(ps.AcquiredConns))
	m.dbPoolIdle.Set(float64(ps.IdleConns))
	m.dbPoolTotal.Set(float64(ps.TotalConns))
	m.dbPoolMax.Set(float64(ps.MaxConns))
	m.dbPoolEmptyAcquires.Set(float64(ps.EmptyAcquireCount))
}

// updateOperationalGauges refreshes the 4 cached operational gauges from the DB.
// Called once per worker cycle so Prometheus scrapes read cached values instead of
// hitting the DB on every scrape.
func (m *Metrics) updateOperationalGauges(s store.PostgreSQLStore) {
	ctx := context.Background()
	m.updateProcessGauges(s)
	if n, err := s.CountCollection(ctx, "alert_groups", map[string]any{"status": "open"}); err == nil {
		m.alertGroupsOpen.Set(float64(n))
	}
	if n, err := s.CountCollection(ctx, "notifications", map[string]any{"status": "retry_scheduled"}); err == nil {
		m.retryQueueDepth.Set(float64(n))
	}
	if n, err := s.CountCollection(ctx, "notification_batches", map[string]any{"status": "open"}); err == nil {
		m.batchesPending.Set(float64(n))
	}
	if n, err := s.CountCollection(ctx, "kafka_outbox", map[string]any{}); err == nil {
		m.kafkaOutboxDepth.Set(float64(n))
	}
	if byTopic, err := s.CountKafkaOutboxByTopic(ctx); err == nil {
		// Reset first so a topic that drained to zero is cleared rather than stuck
		// at its last scraped value.
		m.kafkaOutboxByTopic.Reset()
		for topic, n := range byTopic {
			m.kafkaOutboxByTopic.WithLabelValues(topic).Set(float64(n))
		}
	}
	if age, err := s.OldestDueEscalationAgeSeconds(ctx); err == nil {
		m.oldestDueEscalationAge.Set(age)
	}
	if age, err := s.OldestPendingDeliveryAgeSeconds(ctx); err == nil {
		m.oldestPendingDeliveryAge.Set(age)
	}
}

func (m *Metrics) recordCycle(d time.Duration) {
	now := time.Now()
	m.workerCycleDuration.Set(d.Seconds())
	m.workerLastCycleTimestamp.Set(float64(now.Unix()))
	m.workerCyclesTotal.Inc()
	atomic.AddInt64(&m.cyclesCompleted, 1)
	m.lastCycleAt.Store(utils.ToISO(now.UTC()))
}

func (m *Metrics) incDeliveredByProvider(provider string) {
	m.deliveredByProvider.WithLabelValues(provider).Inc()
}

// recordStageDurations observes per-stage worker-cycle latency. durations maps a
// stage name (escalation, batches, deliveries, retries, archival, kafka) to its
// duration in milliseconds, as returned by Engine.RunWorkerCycle.
func (m *Metrics) recordStageDurations(durations map[string]float64) {
	for stage, ms := range durations {
		m.cycleStageDuration.WithLabelValues(stage).Observe(ms / 1000)
	}
}

func (m *Metrics) incAlerts(n int) { m.alertsIngested.Add(float64(n)) }

func (m *Metrics) incDelivered(n int64) { m.notificationsDelivered.Add(float64(n)) }
func (m *Metrics) incFailed(n int64)    { m.notificationsFailed.Add(float64(n)) }
func (m *Metrics) incArchived(n int64)  { m.groupsArchived.Add(float64(n)) }
func (m *Metrics) incStaleClaimsReclaimed(n int64) {
	m.staleClaimsReclaimed.Add(float64(n))
}
func (m *Metrics) incDeliveryError(provider string) {
	m.deliveryErrorsByProvider.WithLabelValues(provider).Inc()
}
func (m *Metrics) incWorkerCycleSkipped() { m.workerCyclesSkipped.Inc() }

// --- engine.MetricsSink implementation ---

// ObserveDeliveryLatency records a provider call's duration, labeled by channel.
func (m *Metrics) ObserveDeliveryLatency(channel string, seconds float64) {
	m.deliveryDuration.WithLabelValues(channel).Observe(seconds)
}

// IncDeadLetter counts a notification that became permanently failed.
func (m *Metrics) IncDeadLetter(channel string) {
	m.deadLetterEvents.WithLabelValues(channel).Inc()
}

// IncDeliveryShortCircuited counts a delivery skipped by an open circuit breaker.
func (m *Metrics) IncDeliveryShortCircuited(channel string) {
	m.deliveryShortCircuited.WithLabelValues(channel).Inc()
}

// IncNotificationSkipped counts a notification that never reached a provider
// because the channel has no transport configured here.
func (m *Metrics) IncNotificationSkipped(channel, reason string) {
	m.notificationsSkipped.WithLabelValues(channel, reason).Inc()
}

// IncRetentionDeleted counts rows the retention sweep removed, by category.
func (m *Metrics) IncRetentionDeleted(category string, n int) {
	m.retentionDeleted.WithLabelValues(category).Add(float64(n))
}

// IncShiftNotifications counts on-call handoff notifications.
func (m *Metrics) IncShiftNotifications(n int) {
	m.shiftNotifications.Add(float64(n))
}

// SetScheduleCoverage publishes the standing schedule-coverage check from the
// worker cycle, so a schedule that drifted into gaps after its chain was saved
// is visible to alerting rather than only to whoever opens the UI.
func (m *Metrics) SetScheduleCoverage(degraded, total int) {
	m.schedulesDegraded.Set(float64(degraded))
	m.schedulesTotal.Set(float64(total))
}

// SetDutyAttendance publishes how many people on call have not confirmed their
// shift. A schedule can be covered on paper while nobody has said yes, and that
// gap was previously visible nowhere.
func (m *Metrics) SetDutyAttendance(missing, onCall int) {
	m.dutyWithoutCheckin.Set(float64(missing))
	m.dutyOnCall.Set(float64(onCall))
}

// SetSourcesSilent publishes how many sources have gone quiet past their
// declared heartbeat.
func (m *Metrics) SetSourcesSilent(n int) {
	m.sourcesSilent.Set(float64(n))
}

// IncWorkerPanic counts a panic recovered inside a worker goroutine, by stage.
func (m *Metrics) IncWorkerPanic(stage string) {
	m.workerPanics.WithLabelValues(stage).Inc()
}
func (m *Metrics) incIngestError(source string) {
	m.ingestErrors.WithLabelValues(source).Inc()
}
