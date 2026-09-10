package engine

import (
	"context"

	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/store"
)

// analytics_community.go is the community half of the analytics seam: every
// entry point that analytics_emit.go implements is present here doing nothing.
//
// The point is that the call sites are identical in both editions. Escalation,
// delivery and the worker call withAnalyticsOutbox and the emitters
// unconditionally; whether a lifecycle event is written is decided here and
// nowhere else. Removing the calls instead would give the two editions two
// different transaction write sets, and a bug reported against one would stop
// being reproducible against the other — which is the failure mode this whole
// arrangement exists to prevent.
//
// The kafka_outbox table stays in the schema in both editions on purpose (see
// migration 0012): keeping the schema identical is what makes moving from
// community to enterprise an image swap rather than a migration.

// analyticsEnabled is always false here: this build ships no producer.
//
// Nothing in the community tree calls it: both callers — analytics_emit.go and
// analytics_coverage.go — are tagged !community and the cut deletes them. It is
// kept anyway, because a seam that is complete on one side only is the thing
// this file exists to prevent: the next entry point added to the enterprise half
// should find its counterpart already here.
func (e *Engine) analyticsEnabled() bool { return false } //nolint:unused // the enterprise half is the caller

// withAnalyticsOutbox returns the save list unchanged, so no mutator widens its
// write set to a table nothing writes to.
func (e *Engine) withAnalyticsOutbox(cols []string) []string { return cols }

// outboxAppender returns a closure that drops the alert on the floor. Ingest
// still calls it once per accepted alert; that call is what keeps the ingest
// path the same shape in both editions.
func (e *Engine) outboxAppender(*store.State, map[string]any, string) func(alert map[string]any, g model.AlertGroup, hasGroup bool) {
	return func(map[string]any, model.AlertGroup, bool) {}
}

func (e *Engine) emitGroupEvent(*store.State, model.AlertGroup, string, string, map[string]any) {}

func (e *Engine) emitDeliveryAttempt(*store.State, model.Notification, map[string]any, deliveryOutcome, string) {
}

func (e *Engine) emitScheduleCoverage(context.Context, map[string]any) {}

// PublishKafkaOutbox reports that nothing was published. The worker calls it
// every cycle in both editions; here there is no producer to drain into.
func (e *Engine) PublishKafkaOutbox(context.Context) (int, error) { return 0, nil }

// AnalyticsSupported reports that this build ships no analytics stream at all,
// which is what lets the capabilities endpoint say "not in this edition"
// instead of "not configured".
func (e *Engine) AnalyticsSupported() bool { return false }
