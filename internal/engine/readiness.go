package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// readiness.go answers one question for a pilot team: if an alert arrived right
// now, would anybody actually be paged?
//
// Every check here is derived from state the product already owns, so the answer
// cannot drift from reality the way a setup checklist someone ticks by hand can.
// The checks are deliberately about *reachability*, not about tidiness: an empty
// chain nothing points at is a warning, an empty chain an integration routes to
// is a blocker, because the second one silently swallows real pages.

// Severities. A blocker means alerts can be lost or nobody gets paged; a warning
// is worth fixing but does not break the alert path.
const (
	ReadinessOK      = "ok"
	ReadinessWarning = "warning"
	ReadinessBlocker = "blocker"
)

// Metadata keys backing the two facts readiness cannot derive from domain state.
const (
	metaLastBackupAt    = "last_backup_at"
	metaWorkerHeartbeat = "worker_heartbeat_at"
	metaReadinessAck    = "readiness_ack"
)

// workerHeartbeatMaxAge is how long the worker's heartbeat may be stale before
// readiness calls it dead.
//
// The heartbeat lives in the database rather than in process memory on purpose:
// the documented production topology runs the API and the worker as separate
// deployments, so an API replica has no in-process knowledge of worker cycles.
// Reading srv.metrics here would report "worker dead" on every split
// installation and "worker alive" on a single-process one — the answer would
// depend on the topology instead of on the worker.
const workerHeartbeatMaxAge = 3 * time.Minute

// workerHeartbeatInterval throttles the heartbeat write. The worker cycles every
// few seconds; persisting that would be a pointless write per cycle forever.
const workerHeartbeatInterval = 30 * time.Second

// backupMaxAge is the default staleness budget for the last reported backup. It
// matches the RPO in docs/BACKUP_RESTORE.md with room for a late nightly job;
// operators with a different schedule override it.
const backupMaxAge = 26 * time.Hour

// readinessCheck is one answerable question about the installation.
type readinessCheck struct {
	Key      string   // stable identifier, safe to match on
	Title    string   // human-readable question
	Severity string   // ok | warning | blocker
	Detail   string   // what was found, in one sentence
	Items    []string // the specific objects at fault, for the UI to link to
}

func (c readinessCheck) toMap() map[string]any {
	return map[string]any{
		"key":      c.Key,
		"title":    c.Title,
		"severity": c.Severity,
		"detail":   c.Detail,
		"items":    toAnySlice(c.Items),
	}
}

// Readiness reports whether this installation can actually page someone.
//
// It never fails the whole report because one check could not run: a check that
// errors becomes a blocker describing the error, because "we could not tell" is
// not the same as "everything is fine" and must not read like it.
func (e *Engine) Readiness(ctx context.Context) (map[string]any, error) {
	now := utils.UTCNow()
	meta, metaErr := e.store.GetMetadata(ctx)
	if meta == nil {
		meta = map[string]any{}
	}

	checks := []readinessCheck{
		e.checkDatabase(ctx),
		checkWorker(meta, metaErr, now),
		e.checkIntegrations(ctx),
		e.checkRouting(ctx),
		e.checkNotificationTargets(ctx),
		e.checkScheduleCoverage(ctx),
		e.checkTeamScoping(ctx),
		e.checkChannelPolicy(ctx),
		e.checkDataRetention(ctx),
		e.checkBackup(meta, metaErr, now),
	}

	blockers, warnings := 0, 0
	items := make([]any, 0, len(checks))
	for _, c := range checks {
		switch c.Severity {
		case ReadinessBlocker:
			blockers++
		case ReadinessWarning:
			warnings++
		}
		items = append(items, c.toMap())
	}

	fingerprint := blockerFingerprint(checks)
	ack := acknowledgement(meta)
	acknowledged := blockers > 0 && ack != nil && utils.StrVal(ack, "fingerprint") == fingerprint

	out := map[string]any{
		"checked_at": utils.ToISO(now),
		"checks":     items,
		"blockers":   blockers,
		"warnings":   warnings,
		// ready means nothing is in the way. production_ready is the weaker
		// claim the activation gate uses: either nothing is in the way, or a
		// named person accepted exactly these blockers.
		"ready":               blockers == 0,
		"production_ready":    blockers == 0 || acknowledged,
		"blocker_fingerprint": fingerprint,
	}
	if ack != nil {
		// Report a stale acknowledgement as such rather than hiding it: the
		// operator needs to see that the set of blockers changed under it.
		copyAck := map[string]any{}
		for k, v := range ack {
			copyAck[k] = v
		}
		copyAck["current"] = acknowledged
		out["acknowledgement"] = copyAck
	}
	return out, nil
}

// blockerFingerprint identifies the exact set of blockers an acknowledgement was
// made against. Accepting "one schedule has a gap" must not silently also accept
// a different blocker that appears tomorrow, so the fingerprint covers the
// offending items and not just the check keys.
func blockerFingerprint(checks []readinessCheck) string {
	var parts []string
	for _, c := range checks {
		if c.Severity != ReadinessBlocker {
			continue
		}
		sorted := append([]string(nil), c.Items...)
		sort.Strings(sorted)
		parts = append(parts, c.Key+"="+strings.Join(sorted, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, ";")))
	return hex.EncodeToString(sum[:])
}

func acknowledgement(meta map[string]any) map[string]any {
	ack, _ := meta[metaReadinessAck].(map[string]any)
	return ack
}

// ── individual checks ─────────────────────────────────────────────────────────

func (e *Engine) checkDatabase(ctx context.Context) readinessCheck {
	c := readinessCheck{Key: "database", Title: "Database reachable"}
	if err := e.store.Ping(ctx); err != nil {
		c.Severity = ReadinessBlocker
		c.Detail = "the database did not answer: " + err.Error()
		return c
	}
	c.Severity = ReadinessOK
	c.Detail = "the database answered."
	return c
}

func checkWorker(meta map[string]any, metaErr error, now time.Time) readinessCheck {
	c := readinessCheck{Key: "worker", Title: "Escalation worker running"}
	if metaErr != nil {
		c.Severity = ReadinessBlocker
		c.Detail = "could not read the worker heartbeat: " + metaErr.Error()
		return c
	}
	raw := utils.StrVal(meta, metaWorkerHeartbeat)
	if raw == "" {
		c.Severity = ReadinessBlocker
		c.Detail = "no worker cycle has ever been recorded — nothing will escalate, deliver or retry."
		return c
	}
	at, err := utils.ParseDatetime(raw)
	if err != nil {
		c.Severity = ReadinessBlocker
		c.Detail = "the worker heartbeat is unreadable: " + raw
		return c
	}
	age := now.Sub(at)
	if age > workerHeartbeatMaxAge {
		c.Severity = ReadinessBlocker
		c.Detail = fmt.Sprintf("the last worker cycle was %s ago (limit %s) — escalation and delivery have stopped.",
			roundedAge(age), workerHeartbeatMaxAge)
		return c
	}
	c.Severity = ReadinessOK
	c.Detail = "the worker completed a cycle " + roundedAge(age) + " ago."
	return c
}

func (e *Engine) checkIntegrations(ctx context.Context) readinessCheck {
	c := readinessCheck{Key: "integrations", Title: "An alert source is configured"}
	integrations, err := e.refCollection(ctx, "integrations")
	if err != nil {
		return failedCheck(c, err)
	}
	visible := e.visible(ctx, "integrations", integrations)
	if len(visible) == 0 {
		// A warning, not a blocker: an installation with no integration cannot
		// lose an alert, because no alert can arrive. It is an unfinished setup,
		// which is what the wizard is for.
		c.Severity = ReadinessWarning
		c.Detail = "no integration exists yet, so no alerts can arrive."
		return c
	}
	c.Severity = ReadinessOK
	c.Detail = fmt.Sprintf("%d integration(s) can receive alerts.", len(visible))
	return c
}

// checkRouting is the core question: does every route that can receive an alert
// end at a chain that actually does something?
func (e *Engine) checkRouting(ctx context.Context) readinessCheck {
	c := readinessCheck{Key: "routing", Title: "Every route reaches a chain with steps"}
	integrations, err := e.refCollection(ctx, "integrations")
	if err != nil {
		return failedCheck(c, err)
	}
	chains, err := e.refCollection(ctx, "escalation_chains")
	if err != nil {
		return failedCheck(c, err)
	}

	var broken []string
	for _, integ := range e.visible(ctx, "integrations", integrations) {
		name := strDefault(utils.StrVal(integ, "name"), utils.StrVal(integ, "id"))
		for _, raw := range anyList(integ["routes"]) {
			route, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			routeName := strDefault(utils.StrVal(route, "name"), "route")
			chainID := utils.StrVal(route, "escalation_chain_id")
			if chainID == "" {
				broken = append(broken, name+" → "+routeName+": no escalation chain")
				continue
			}
			chain := chains[chainID]
			if chain == nil {
				broken = append(broken, name+" → "+routeName+": escalation chain "+chainID+" does not exist")
				continue
			}
			if len(anyList(chain["steps"])) == 0 {
				broken = append(broken, name+" → "+routeName+": chain "+
					strDefault(utils.StrVal(chain, "name"), chainID)+" has no steps")
			}
		}
	}
	if len(broken) > 0 {
		c.Severity = ReadinessBlocker
		c.Detail = "alerts matching these routes would be received and then page nobody."
		c.Items = broken
		return c
	}
	c.Severity = ReadinessOK
	c.Detail = "every route ends at a chain with at least one step."
	return c
}

// checkNotificationTargets finds people a chain or schedule can select but no
// transport can reach — the failure mode where everything looks configured and
// the page silently goes nowhere.
func (e *Engine) checkNotificationTargets(ctx context.Context) readinessCheck {
	c := readinessCheck{Key: "notification_targets", Title: "Everyone on call can be reached"}
	users, err := e.refCollection(ctx, "users")
	if err != nil {
		return failedCheck(c, err)
	}
	chains, err := e.refCollection(ctx, "escalation_chains")
	if err != nil {
		return failedCheck(c, err)
	}
	schedules, err := e.refCollection(ctx, "schedules")
	if err != nil {
		return failedCheck(c, err)
	}

	pageable := map[string]bool{}
	for _, chain := range chains {
		for _, raw := range anyList(chain["steps"]) {
			step, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for _, id := range anyList(step["user_ids"]) {
				pageable[fmt.Sprintf("%v", id)] = true
			}
			if id := utils.StrVal(step, "user_id"); id != "" {
				pageable[id] = true
			}
		}
	}
	for _, sched := range schedules {
		if rot, ok := sched["rotation"].(map[string]any); ok {
			for _, id := range anyList(rot["participant_ids"]) {
				pageable[fmt.Sprintf("%v", id)] = true
			}
		}
		for _, key := range []string{"shifts", "overrides"} {
			for _, raw := range anyList(sched[key]) {
				if entry, ok := raw.(map[string]any); ok {
					if id := utils.StrVal(entry, "user_id"); id != "" {
						pageable[id] = true
					}
				}
			}
		}
	}

	var unreachable []string
	for id := range pageable {
		user := users[id]
		if user == nil {
			// A schedule or chain naming a user who no longer exists. Schedule
			// coverage reports this for schedules; a chain step can carry it too.
			unreachable = append(unreachable, id+": user no longer exists")
			continue
		}
		if reason := userUnreachableReason(user, e.deliveryCfg); reason != "" {
			unreachable = append(unreachable, strDefault(utils.StrVal(user, "name"), id)+": "+reason)
		}
	}
	sort.Strings(unreachable)

	if len(unreachable) > 0 {
		c.Severity = ReadinessBlocker
		c.Detail = "these people can be selected to page but no transport can reach them."
		c.Items = unreachable
		return c
	}
	c.Severity = ReadinessOK
	if len(pageable) == 0 {
		c.Detail = "no chain or schedule names anyone yet."
		c.Severity = ReadinessWarning
		return c
	}
	c.Detail = fmt.Sprintf("all %d people a chain or schedule can select have a working transport.", len(pageable))
	return c
}

// userUnreachableReason returns why a user cannot be paged, or "" when they can.
// A channel counts only when this deployment actually has a transport for it —
// a telegram target without a bot token is skipped at delivery time, so treating
// it as reachable here would repeat exactly the lie BETA-032 removed.
func userUnreachableReason(user map[string]any, cfg DeliveryConfig) string {
	var configured, unconfigured []string

	consider := func(channel, target string) {
		if channel == "" || target == "" {
			return
		}
		if gap := channelTransportGap(channel, cfg); gap != "" {
			unconfigured = append(unconfigured, channel+" ("+gap+")")
			return
		}
		configured = append(configured, channel)
	}

	for _, raw := range anyList(user["notification_targets"]) {
		if t, ok := raw.(map[string]any); ok {
			consider(utils.StrVal(t, "type"), utils.StrVal(t, "target"))
		}
	}
	if policies, ok := user["notification_policies"].(map[string]any); ok {
		for _, steps := range policies {
			for _, raw := range anyList(steps) {
				if s, ok := raw.(map[string]any); ok {
					consider(utils.StrVal(s, "channel"), utils.StrVal(s, "target"))
				}
			}
		}
	}

	if len(configured) > 0 {
		return ""
	}
	if len(unconfigured) > 0 {
		sort.Strings(unconfigured)
		return "only unconfigured channels: " + strings.Join(dedupe(unconfigured), ", ")
	}
	return "no notification target or policy"
}

// channelTransportGap mirrors internal/engine/delivery_dispatch.go: it returns
// the reason a channel has no transport in this deployment, or "" when it has
// one. Keeping the two in step matters — a divergence would make readiness
// promise a page that delivery then skips.
func channelTransportGap(channel string, cfg DeliveryConfig) string {
	// A proxy that could not be parsed is a missing transport like any other:
	// delivery fails every attempt on that channel (see newDeliveryClients), so
	// readiness must not count it as a way to reach anybody.
	if err := cfg.proxies.errs[channel]; err != nil {
		return "delivery proxy is misconfigured: " + err.Error()
	}
	switch channel {
	case "telegram":
		if cfg.TelegramToken == "" {
			return "NXS_ANOMALY_TELEGRAM_BOT_TOKEN is not set"
		}
	case "email":
		if cfg.SMTP.Host == "" {
			return "NXS_ANOMALY_SMTP_HOST is not set"
		}
	case "call":
		if len(cfg.AsteriskInstances) == 0 {
			return "no Asterisk instance is configured"
		}
	case "mobile":
		if cfg.MobilePushURL == "" {
			return "NXS_ANOMALY_MOBILE_PUSH_URL is not set"
		}
	}
	// log and webhook always have a transport; slack/mattermost carry their
	// webhook_url on the ChatOps channel, which is checked where it lives.
	return ""
}

// checkTeamScoping reports the boundary between teams, or the absence of one.
//
// Scoping is a deployment decision, not a defect either way: a single-team
// installation is a legitimate trust domain. What is not legitimate is not
// having decided — so this check states which mode is in force in plain words,
// and when the boundary is on, names the objects that fall outside it.
//
// An object with no team is visible and pageable by everyone while scoping is
// on. That is a hole in exactly the boundary somebody turned on for a reason,
// and it is silent: the UI shows the object, the audit shows nothing wrong.
func (e *Engine) checkTeamScoping(ctx context.Context) readinessCheck {
	c := readinessCheck{Key: "team_scoping", Title: "Team boundaries are decided"}
	if !editionHasTeamScoping {
		// Not a warning: a warning is a nudge to go and change something, and
		// there is nothing here for this reader to change. State the boundary
		// that is in force and name what would move it, rather than pointing at
		// a flag this build does not read — advice that does nothing is worse
		// than silence, because it costs the reader a trip to find that out.
		c.Severity = ReadinessOK
		c.Detail = "every operator sees and can page through every integration and schedule. " +
			"This edition has no team boundary; limiting visibility per team is an enterprise feature."
		return c
	}
	if !teamScopingEnabled() {
		// A warning rather than a pass: nothing is broken, but "every operator
		// can page every service" is a statement somebody should have made on
		// purpose, and this is where they see that they did.
		c.Severity = ReadinessWarning
		c.Detail = "team scoping is off: every operator sees and can page through every integration and schedule. " +
			"Correct for a single trust domain; set NXS_ANOMALY_TEAM_SCOPING=true for a multi-team install."
		return c
	}

	var unassigned []string
	for _, collection := range []string{"integrations", "schedules"} {
		items, err := e.refCollection(ctx, collection)
		if err != nil {
			return failedCheck(c, err)
		}
		for id, item := range items {
			if utils.StrVal(item, "team_id") == "" {
				unassigned = append(unassigned, collection+":"+strDefault(utils.StrVal(item, "name"), id))
			}
		}
	}
	sort.Strings(unassigned)

	if len(unassigned) > 0 {
		c.Severity = ReadinessBlocker
		c.Detail = fmt.Sprintf("team scoping is on, but %d object(s) belong to no team and are therefore visible to everyone.", len(unassigned))
		c.Items = unassigned
		return c
	}
	c.Severity = ReadinessOK
	c.Detail = "team scoping is on and every integration and schedule belongs to a team."
	return c
}

// teamScopingEnabled mirrors the server's own reading of the flag. Read here
// rather than passed in because the engine is constructed identically in the
// API and the worker, and a readiness report that depended on which process
// answered would be worse than one that reads the environment both share.
func teamScopingEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("NXS_ANOMALY_TEAM_SCOPING")), "true")
}

// checkScheduleCoverage reuses the standing coverage report rather than
// recomputing gaps, so the readiness page and the worker's metric can never
// disagree about which schedules have holes.
func (e *Engine) checkScheduleCoverage(ctx context.Context) readinessCheck {
	c := readinessCheck{Key: "schedule_coverage", Title: "On-call schedules are covered"}
	report, err := e.ScheduleCoverageReport(ctx)
	if err != nil {
		return failedCheck(c, err)
	}

	var blocking, warning []string
	for _, raw := range anyList(report["items"]) {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strDefault(utils.StrVal(item, "name"), utils.StrVal(item, "schedule_id"))
		var why []string
		if utils.BoolVal(item, "disabled", false) {
			why = append(why, "disabled")
		}
		if n := intVal(item["gap_count"]); n > 0 {
			why = append(why, fmt.Sprintf("%d coverage gap(s) in the next week", n))
		}
		if ghosts := anyList(item["unknown_users"]); len(ghosts) > 0 {
			why = append(why, fmt.Sprintf("%d participant(s) no longer exist", len(ghosts)))
		}
		if len(why) == 0 {
			continue
		}
		entry := name + ": " + strings.Join(why, ", ")
		attached := len(anyList(item["attached_to"])) > 0
		switch {
		case attached && !utils.BoolVal(item, "acknowledged", false):
			blocking = append(blocking, entry)
		case attached:
			// The chain opted in with allow_uncovered: a deliberate decision,
			// already acknowledged where it was made.
			warning = append(warning, entry+" (accepted by the chain)")
		default:
			warning = append(warning, entry+" (no chain uses it)")
		}
	}
	sort.Strings(blocking)
	sort.Strings(warning)

	if len(blocking) > 0 {
		c.Severity = ReadinessBlocker
		c.Detail = "an escalation chain pages through these schedules and they have holes."
		c.Items = blocking
		return c
	}
	if len(warning) > 0 {
		c.Severity = ReadinessWarning
		c.Detail = "these schedules have holes that no chain depends on right now."
		c.Items = warning
		return c
	}
	c.Severity = ReadinessOK
	c.Detail = "every schedule a chain depends on is covered."
	return c
}

func (e *Engine) checkBackup(meta map[string]any, metaErr error, now time.Time) readinessCheck {
	c := readinessCheck{Key: "backup", Title: "A recent backup was reported"}
	if metaErr != nil {
		return failedCheck(c, metaErr)
	}
	raw := utils.StrVal(meta, metaLastBackupAt)
	if raw == "" {
		c.Severity = ReadinessBlocker
		c.Detail = "no backup has ever been reported — see docs/BACKUP_RESTORE.md for how the backup job reports one."
		return c
	}
	at, err := utils.ParseDatetime(raw)
	if err != nil {
		c.Severity = ReadinessBlocker
		c.Detail = "the last reported backup timestamp is unreadable: " + raw
		return c
	}
	age := now.Sub(at)
	if age > backupMaxAge {
		c.Severity = ReadinessBlocker
		c.Detail = fmt.Sprintf("the last reported backup was %s ago (limit %s).", roundedAge(age), backupMaxAge)
		return c
	}
	c.Severity = ReadinessOK
	c.Detail = "the last backup was reported " + roundedAge(age) + " ago."
	return c
}

// ── writes ────────────────────────────────────────────────────────────────────

// ReportBackup records that a backup completed successfully. It is called by the
// backup job, not by a human: the product cannot see managed snapshots, WAL
// archives or dumps written to object storage, so the only honest source is the
// thing that took the backup.
func (e *Engine) ReportBackup(ctx context.Context, payload map[string]any) (map[string]any, error) {
	at := utils.ToISO(utils.UTCNow())
	if raw := utils.StrVal(payload, "at"); raw != "" {
		parsed, err := utils.ParseDatetime(raw)
		if err != nil {
			return nil, errValidation("at must be an ISO-8601 timestamp")
		}
		at = utils.ToISO(parsed.UTC())
	}
	patch := map[string]any{metaLastBackupAt: at}
	if kind := utils.StrVal(payload, "kind"); kind != "" {
		patch["last_backup_kind"] = kind
	}
	if _, err := e.store.PatchMetadata(ctx, patch); err != nil {
		return nil, err
	}
	e.audit(ctx, AuditUpdate, "backup", "last_backup", map[string]any{"at": at})
	return map[string]any{"last_backup_at": at}, nil
}

// AcknowledgeReadiness records that a named actor accepted the blockers that
// exist right now. The acknowledgement stores the fingerprint of that exact set,
// so it stops applying the moment the blockers change — an acknowledgement is a
// decision about known problems, not a permanent mute.
func (e *Engine) AcknowledgeReadiness(ctx context.Context, payload map[string]any) (map[string]any, error) {
	reason := strings.TrimSpace(utils.StrVal(payload, "reason"))
	if reason == "" {
		return nil, errValidation("reason is required: an acknowledgement without one tells the next person nothing")
	}
	report, err := e.Readiness(ctx)
	if err != nil {
		return nil, err
	}
	fingerprint := utils.StrVal(report, "blocker_fingerprint")
	if fingerprint == "" {
		return nil, errValidation("there are no blockers to acknowledge")
	}
	actor := authz.FromContext(ctx)
	ack := map[string]any{
		"at":          utils.ToISO(utils.UTCNow()),
		"actor":       actor.Describe(),
		"actor_id":    actor.ID,
		"reason":      reason,
		"fingerprint": fingerprint,
	}
	if _, err := e.store.PatchMetadata(ctx, map[string]any{metaReadinessAck: ack}); err != nil {
		return nil, err
	}
	e.audit(ctx, AuditUpdate, "readiness", "acknowledgement", ack)
	return e.Readiness(ctx)
}

// recordWorkerHeartbeat persists that a worker cycle completed, throttled to
// workerHeartbeatInterval. Called from RunWorkerCycle.
func (e *Engine) recordWorkerHeartbeat(ctx context.Context) {
	if time.Since(e.lastHeartbeat) < workerHeartbeatInterval {
		return
	}
	e.lastHeartbeat = time.Now()
	if _, err := e.store.PatchMetadata(ctx, map[string]any{
		metaWorkerHeartbeat: utils.ToISO(utils.UTCNow()),
	}); err != nil {
		// Not fatal to the cycle: a missed heartbeat degrades the readiness
		// report, it does not stop escalation or delivery.
		slog.Warn("worker_heartbeat_write_failed", "error", err)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// failedCheck turns an error into a blocker. "We could not determine this" is
// reported as a problem, never as a pass.
func failedCheck(c readinessCheck, err error) readinessCheck {
	c.Severity = ReadinessBlocker
	c.Detail = "this check could not run: " + err.Error()
	return c
}

// visible filters a reference collection through team scoping, so a scoped
// editor's readiness page describes their own boundary rather than the whole
// installation.
func (e *Engine) visible(ctx context.Context, collection string, items map[string]map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if err := e.authorizeItem(ctx, collection, item); err != nil {
			continue
		}
		out = append(out, item)
	}
	return out
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	out := ss[:0]
	for _, s := range ss {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func intVal(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// roundedAge renders a duration for a sentence a human reads, not for a metric.
func roundedAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

// checkChannelPolicy answers whether anything still points at a channel or a
// destination this installation refuses.
//
// A blocker, not a warning, and deliberately so. The delivery pipeline already
// refuses these — nothing leaks. What makes it a blocker is the other half:
// somebody's escalation path ends at an address that will never be contacted,
// and from the roster it looks like they are covered. A silent non-page is the
// exact failure this product exists to prevent, and "the policy caught it" is
// not a page.
func (e *Engine) checkChannelPolicy(ctx context.Context) readinessCheck {
	c := readinessCheck{Key: "channel_policy", Title: "Outbound channels match the installation's policy"}
	p := e.deliveryCfg.Channels
	if len(p.Blocked) == 0 && len(p.EgressAllowlist) == 0 {
		c.Severity = ReadinessOK
		c.Detail = "no outbound channel policy is configured: every channel this build supports may be used, to any destination. " +
			"Set NXS_ANOMALY_BLOCKED_CHANNELS and/or NXS_ANOMALY_EGRESS_ALLOWLIST to state a boundary."
		return c
	}

	var offending []string
	note := func(what, channel, target, why string) {
		if target != "" {
			offending = append(offending, fmt.Sprintf("%s → %s(%s): %s", what, channel, target, why))
			return
		}
		offending = append(offending, fmt.Sprintf("%s → %s: %s", what, channel, why))
	}
	// Judge a channel/target pair the same way the sanitizer does, so the
	// readiness report and the API cannot disagree about what is permitted.
	judge := func(what, channel, target string) {
		if err := p.validateTarget(channel, target); err != nil {
			note(what, channel, target, err.Error())
		}
	}

	users, err := e.refCollection(ctx, "users")
	if err != nil {
		return failedCheck(c, err)
	}
	for id, user := range users {
		who := "user " + strDefault(utils.StrVal(user, "username"), id)
		for _, raw := range anyList(user["notification_targets"]) {
			t, _ := raw.(map[string]any)
			judge(who, utils.StrVal(t, "type"), utils.StrVal(t, "target"))
		}
		policies, _ := user["notification_policies"].(map[string]any)
		for _, name := range []string{"default", "important"} {
			for _, raw := range anyList(policies[name]) {
				step, _ := raw.(map[string]any)
				judge(who+" policy "+name, utils.StrVal(step, "channel"), utils.StrVal(step, "target"))
			}
		}
	}

	chains, err := e.refCollection(ctx, "escalation_chains")
	if err != nil {
		return failedCheck(c, err)
	}
	for id, chain := range chains {
		what := "chain " + strDefault(utils.StrVal(chain, "name"), id)
		for _, raw := range anyList(chain["steps"]) {
			step, _ := raw.(map[string]any)
			switch utils.StrVal(step, "kind") {
			case StepTriggerWebhook:
				judge(what, "webhook", utils.StrVal(step, "webhook_url"))
			case StepCreateIssue:
				judge(what, "webhook", utils.StrVal(step, "url"))
			}
		}
	}

	channels, err := e.refCollection(ctx, "chatops_channels")
	if err != nil {
		return failedCheck(c, err)
	}
	for id, ch := range channels {
		what := "chatops channel " + strDefault(utils.StrVal(ch, "name"), id)
		// The platform is the channel code the policy blocks (slack,
		// mattermost, telegram); the webhook_url is the destination.
		judge(what, utils.StrVal(ch, "platform"), "")
		if u := utils.ResolveSecretRef(utils.StrVal(ch, "webhook_url")); u != "" {
			if ok, detail := p.DestinationAllowed(u); !ok {
				note(what, "webhook", u, detail)
			}
		}
	}

	sort.Strings(offending)
	stated := fmt.Sprintf("blocked channels: [%s]; egress allowlist: [%s]",
		strings.Join(p.BlockedList(), ","), p.EgressAllowlistRaw())
	if len(offending) > 0 {
		c.Severity = ReadinessBlocker
		c.Detail = fmt.Sprintf("%d configured destination(s) are refused by the outbound policy and will never be contacted, "+
			"while the roster shows them as ways to reach somebody. %s", len(offending), stated)
		c.Items = dedupe(offending)
		return c
	}
	c.Severity = ReadinessOK
	c.Detail = "every configured notification target, escalation webhook and ChatOps channel is permitted. " + stated
	return c
}

// checkDataRetention reports whether the installation has decided how long it
// keeps personal data.
//
// A warning, never a blocker, and it does not judge the numbers: the right
// horizon comes from the client's own retention policy and the law they operate
// under, and a product that picked one for them would be pretending to an
// authority it does not have. What it will not do is let "nobody chose" pass as
// a decision — which is what an all-zero configuration silently is.
func (e *Engine) checkDataRetention(_ context.Context) readinessCheck {
	c := readinessCheck{Key: "data_retention", Title: "Data retention is decided"}
	p := e.deliveryCfg.Retention
	horizons := []string{
		fmt.Sprintf("alert groups: %s", retentionWord(p.AlertGroupDays)),
		fmt.Sprintf("audit: %s", retentionWord(p.AuditDays)),
		fmt.Sprintf("notifications: %s", retentionWord(p.NotificationDays)),
		fmt.Sprintf("delivery attempts: %s", retentionWord(p.DeliveryAttemptDays)),
		fmt.Sprintf("chatops messages: %s", retentionWord(p.ChatopsMessageDays)),
		fmt.Sprintf("web sessions: %s", retentionWord(p.WebSessionDays)),
	}
	c.Items = horizons
	if p.Unset() {
		c.Severity = ReadinessWarning
		c.Detail = "no retention horizon is set for the audit trail, notifications, delivery attempts or web sessions: " +
			"they are kept indefinitely, including the personal data they carry. See docs/DATA_INVENTORY.md."
		return c
	}
	c.Severity = ReadinessOK
	c.Detail = "every category has a stated horizon; the worker's retention sweep reports what it deletes as nxs_anomaly_retention_deleted_total."
	return c
}

func retentionWord(days int) string {
	if days <= 0 {
		return "kept indefinitely"
	}
	return fmt.Sprintf("%d days", days)
}
