package engine

import (
	"context"
	"log/slog"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// RetentionSweep deletes whatever has outlived its configured horizon and
// returns how many rows went, by category.
//
// It runs every worker cycle. Each category is independent: a failure in one is
// logged and the rest still run, because a retention sweep that stops at the
// first error is a sweep that silently stops running the day one table develops
// a problem — and the categories it never reached are the ones nobody notices.
//
// Categories with a zero horizon are skipped entirely and are absent from the
// result, which is what makes "nothing was configured" distinguishable from
// "nothing was old enough" in the metric.
func (e *Engine) RetentionSweep(ctx context.Context) map[string]int {
	p := e.deliveryCfg.Retention
	deleted := map[string]int{}

	run := func(category string, days int, fn func(cutoffISO string) (int, error)) {
		if days <= 0 {
			return
		}
		cutoff := utils.ToISO(utils.UTCNow().AddDate(0, 0, -days))
		n, err := fn(cutoff)
		if err != nil {
			// Error, not warning: data that should have gone is still here, and
			// on a compliance horizon that is a finding rather than noise.
			slog.Error("retention_sweep_failed", "category", category, "cutoff", cutoff, "error", err)
		}
		if n > 0 {
			deleted[category] = n
			e.sink().IncRetentionDeleted(category, n)
			// Info level on purpose: rows leaving the database should be
			// visible in the service log, not silent.
			slog.Info("retention_deleted", "category", category, "count", n,
				"cutoff", cutoff, "retention_days", days)
		}
	}

	run(RetentionAlertGroups, p.AlertGroupDays, func(cutoff string) (int, error) {
		return e.store.DeleteOldResolvedGroups(ctx, cutoff)
	})
	run(RetentionChatopsMessages, p.ChatopsMessageDays, func(cutoff string) (int, error) {
		return e.store.DeleteOldChatopsMessages(ctx, cutoff)
	})
	run(RetentionAudit, p.AuditDays, func(cutoff string) (int, error) {
		return e.store.PruneAuditEvents(ctx, cutoff)
	})
	run(RetentionNotifications, p.NotificationDays, func(cutoff string) (int, error) {
		return e.store.DeleteOldNotifications(ctx, cutoff)
	})
	run(RetentionDeliveryAttempts, p.DeliveryAttemptDays, func(cutoff string) (int, error) {
		return e.store.DeleteOldDeliveryAttempts(ctx, cutoff)
	})
	run(RetentionWebSessions, p.WebSessionDays, func(cutoff string) (int, error) {
		return e.store.DeleteOldWebSessions(ctx, cutoff)
	})
	return deleted
}

// ArchiveResolvedGroups deletes resolved groups older than ttlDays. Kept as an
// exported entry point because the API exposes it as a manual operation; the
// scheduled path goes through RetentionSweep.
func (e *Engine) ArchiveResolvedGroups(ctx context.Context, ttlDays int) (int, error) {
	if ttlDays <= 0 {
		return 0, nil
	}
	cutoff := utils.ToISO(utils.UTCNow().AddDate(0, 0, -ttlDays))
	return e.store.DeleteOldResolvedGroups(ctx, cutoff)
}

// ArchiveChatopsMessages is ArchiveResolvedGroups for the ChatOps mirror.
func (e *Engine) ArchiveChatopsMessages(ctx context.Context, ttlDays int) (int, error) {
	if ttlDays <= 0 {
		return 0, nil
	}
	cutoff := utils.ToISO(utils.UTCNow().AddDate(0, 0, -ttlDays))
	return e.store.DeleteOldChatopsMessages(ctx, cutoff)
}
