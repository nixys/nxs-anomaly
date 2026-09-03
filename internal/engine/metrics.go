package engine

// MetricsSink receives engine-level metric events emitted from the delivery
// pipeline. All methods must be safe for concurrent use — ObserveDeliveryLatency
// is called from the bounded delivery goroutines. The default sink (noopMetrics)
// discards everything, so the engine works unchanged when no sink is wired (e.g.
// the standalone run-worker, which has no Prometheus registry).
type MetricsSink interface {
	// ObserveDeliveryLatency records the wall-clock duration of a single provider
	// call, labeled by channel (webhook, telegram, email, ...).
	ObserveDeliveryLatency(channel string, seconds float64)
	// IncDeadLetter counts a notification that became permanently failed.
	IncDeadLetter(channel string)
	// IncDeliveryShortCircuited counts a delivery skipped because the channel's
	// circuit breaker was open.
	IncDeliveryShortCircuited(channel string)
	// IncNotificationSkipped counts a notification that had no transport to go
	// through, by channel and reason. A rising count means a channel is
	// configured on paper only — nobody is being told anything through it.
	IncNotificationSkipped(channel, reason string)
	// IncShiftNotifications counts notifications sent because someone's on-call
	// shift started. Separate from alert deliveries: these are scheduled
	// messages, and mixing them into the alert counters would distort both.
	IncShiftNotifications(n int)
	// SetScheduleCoverage reports how many schedules an escalation chain pages
	// through are currently degraded (gaps, disabled, or naming deleted users)
	// without the step having accepted that, out of the total number of
	// schedules.
	SetScheduleCoverage(degraded, total int)
	// IncWorkerPanic counts a panic recovered inside a worker goroutine, labeled
	// by stage (deliveries, retries, dead_letter). A non-zero value means a task
	// crashed but the worker process survived.
	IncWorkerPanic(stage string)
	// SetDutyAttendance reports how many of the people currently on call have
	// not confirmed the shift, out of how many are on call. Coverage answers
	// whether somebody is assigned; this answers whether they said yes.
	SetDutyAttendance(missing, onCall int)
	// IncRetentionDeleted counts rows the retention sweep deleted, by category
	// (alert_groups, audit_events, notifications, …). A retention horizon that
	// nobody can see working is indistinguishable from one that silently stopped
	// running, which is the failure this counter exists to make visible.
	IncRetentionDeleted(category string, n int)
	// SetSourcesSilent reports how many integrations with a declared heartbeat
	// have gone quiet past it. Every other signal starts with an alert arriving;
	// this is the one that starts with none arriving.
	SetSourcesSilent(n int)
}

// noopMetrics is the default sink; every method is a no-op.
type noopMetrics struct{}

func (noopMetrics) ObserveDeliveryLatency(string, float64) {}
func (noopMetrics) IncDeadLetter(string)                   {}
func (noopMetrics) IncDeliveryShortCircuited(string)       {}
func (noopMetrics) IncNotificationSkipped(string, string)  {}
func (noopMetrics) IncShiftNotifications(int)              {}
func (noopMetrics) SetScheduleCoverage(int, int)           {}
func (noopMetrics) IncWorkerPanic(string)                  {}
func (noopMetrics) SetDutyAttendance(int, int)             {}
func (noopMetrics) IncRetentionDeleted(string, int)        {}
func (noopMetrics) SetSourcesSilent(int)                   {}

// sink returns the engine's metrics sink, or a no-op when none was wired (e.g.
// an Engine built directly in tests). Keeps all call sites nil-safe.
func (e *Engine) sink() MetricsSink {
	if e.metrics == nil {
		return noopMetrics{}
	}
	return e.metrics
}

// SetMetricsSink wires a metrics sink into the engine. Passing nil restores the
// no-op sink. Must be called during setup, before the worker loop starts.
func (e *Engine) SetMetricsSink(m MetricsSink) {
	if m == nil {
		m = noopMetrics{}
	}
	e.metrics = m
}
