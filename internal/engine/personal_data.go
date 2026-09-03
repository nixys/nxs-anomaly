package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// personal_data.go implements the two operations an installation needs in order
// to answer a person about their own data: produce everything held about them,
// and erase it.
//
// Both are administrative and both are audited — including the erasure, which
// deliberately leaves a record saying that it happened. "We deleted the evidence
// that we deleted the evidence" is not a defensible position, and the record it
// leaves names no personal datum: an id, an actor, a timestamp, and counts.
//
// See docs/DATA_INVENTORY.md for the catalogue these two operate over.

// Audit actions for the two operations. Named as their own actions rather than
// folded into update/delete so a filter can find them.
const (
	AuditExport = "export_personal_data"
	AuditErase  = "erase_personal_data"
)

// personalFields are the fields of a user record that identify a person, as
// opposed to describing their role in the installation.
//
// Listed explicitly rather than derived, and reused by both the erasure and its
// verification: if the two disagreed, the verification would be checking a
// different question from the one the erasure answered.
var personalFields = []string{"name", "username", "email", "phone", "telegram_id", "oidc_subject"}

// ExportUserData returns everything this installation holds about one person.
//
// Assembled by reading, never by summarising: the point of an export is that the
// person can see what is actually stored, so each section carries the records as
// they are, not a description of them. The one thing deliberately not included is
// the password hash — it is not information about the person, it is a credential,
// and putting it in an exported file would create a new place for it to leak.
// Whether one is set is reported instead.
func (e *Engine) ExportUserData(ctx context.Context, userID string) (map[string]any, error) {
	user, err := e.store.GetItem(ctx, "users", userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errNotFound("user " + userID + " not found")
	}

	out := map[string]any{
		"exported_at": utils.ToISO(utils.UTCNow()),
		"user_id":     userID,
		"user":        user,
	}

	_, hasPassword, err := e.store.GetUserPasswordHash(ctx, userID)
	if err != nil {
		return nil, err
	}
	// The hash itself never leaves. See the doc comment.
	out["has_local_password"] = hasPassword

	if teams, err := e.store.ListTeamIDsForUser(ctx, userID); err == nil {
		out["team_ids"] = toAnySlice(teams)
	} else {
		return nil, err
	}

	sessions, err := e.store.ListWebSessions(ctx, userID)
	if err != nil {
		return nil, err
	}
	sessionItems := make([]any, 0, len(sessions))
	for _, s := range sessions {
		// The token hash is omitted for the same reason as the password hash.
		sessionItems = append(sessionItems, map[string]any{
			"id":         s.ID,
			"created_at": utils.ToISO(s.CreatedAt),
			"expires_at": utils.ToISO(s.ExpiresAt),
			"request_ip": s.RequestIP,
			"user_agent": s.UserAgent,
		})
	}
	out["web_sessions"] = sessionItems

	// Collections that carry a user_id column.
	for _, collection := range []string{
		"notifications", "notification_policy_runs",
		"mobile_devices", "mobile_sessions", "chatops_channels",
	} {
		items, err := e.store.ListItemsIn(ctx, collection, "user_id", []any{userID})
		if err != nil {
			return nil, err
		}
		out[collection] = toAnyMaps(items)
		if collection != "notifications" {
			continue
		}
		// Delivery attempts hang off the notifications rather than off the
		// person, so they are fetched by the ids just read.
		ids := make([]any, 0, len(items))
		for _, n := range items {
			ids = append(ids, utils.StrVal(n, "id"))
		}
		attempts, err := e.store.ListItemsIn(ctx, "notification_delivery_attempts", "notification_id", ids)
		if err != nil {
			return nil, err
		}
		out["notification_delivery_attempts"] = toAnyMaps(attempts)
	}

	// Schedules are not keyed by user; a person appears inside the rotation and
	// shift documents. Reported as the schedules they appear in rather than as
	// the whole document, which would export other people's assignments too.
	schedules, err := e.refCollection(ctx, "schedules")
	if err != nil {
		return nil, err
	}
	var scheduleNames []string
	for id, sched := range schedules {
		if scheduleMentionsUser(sched, userID) {
			scheduleNames = append(scheduleNames, strDefault(utils.StrVal(sched, "name"), id))
		}
	}
	sort.Strings(scheduleNames)
	out["schedules"] = toAnySlice(scheduleNames)

	// Two passes over the audit trail: what this person did, and what was done
	// to them. They are different questions and a person asking about their data
	// is owed both.
	actions, _, err := e.store.ListAuditEvents(ctx, map[string]any{"actor_id": userID}, auditExportLimit, 0)
	if err != nil {
		return nil, err
	}
	about, _, err := e.store.ListAuditEvents(ctx,
		map[string]any{"entity_type": "user", "entity_id": userID}, auditExportLimit, 0)
	if err != nil {
		return nil, err
	}
	out["audit_events_by_user"] = toAnyMaps(actions)
	out["audit_events_about_user"] = toAnyMaps(about)
	out["audit_export_limit"] = auditExportLimit

	e.audit(ctx, AuditExport, "user", userID, map[string]any{
		"notifications": len(out["notifications"].([]any)),
		"web_sessions":  len(sessionItems),
	})
	return out, nil
}

// auditExportLimit bounds each of the two audit sections. An export is a
// document a human reads, and an unbounded one on a long-lived installation is a
// request that times out rather than an answer. The limit is reported in the
// export so a truncated section is visible as truncated.
const auditExportLimit = 5000

// EraseUser removes the personal data held about one person, leaving the record
// of what happened in the installation intact.
//
// The shape of the operation is the whole design, so it is worth stating plainly:
//
//	Deleted outright  — credentials, browser sessions (which carry the client
//	                    IP), mobile devices (push tokens) and mobile sessions,
//	                    ChatOps channels bound to the person.
//	Pseudonymised     — the user record itself (the roster entry stays, so every
//	                    foreign key still resolves and no history becomes an
//	                    orphan), the delivery addresses on their notifications
//	                    and attempts, and the actor name and IP on their audit
//	                    events.
//	Untouched         — every event, notification and attempt as a fact: what
//	                    happened, when, in what order, and which opaque id it
//	                    belonged to.
//
// Deleting the user row instead was the obvious alternative and is worse. It
// orphans the alert history ("somebody was paged" with no way to tell whether it
// was one person or five), and it destroys the audit record of actions this
// person took on other people's data — which is the record an audit trail exists
// to keep. See PseudonymiseAuditActor for the same argument about the trail.
//
// The returned report is a verification, not a promise: after the writes, the
// data is read back and searched for the original identifiers. See
// verifyErasure.
func (e *Engine) EraseUser(ctx context.Context, userID string) (map[string]any, error) {
	user, err := e.store.GetItem(ctx, "users", userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errNotFound("user " + userID + " not found")
	}
	if utils.StrVal(user, "erased_at") != "" {
		// Idempotent rather than an error: the second request is usually a
		// retry, and refusing it would leave the caller unable to tell whether
		// the first one worked.
		// The record now holds the pseudonym, so it is what
		// originalIdentifiers would otherwise report as a "personal value still
		// present" — which is how a correct second run would fail its own
		// verification. Excluded explicitly.
		report, err := e.verifyErasure(ctx, userID, originalIdentifiers(user, erasurePseudonym(userID)))
		if err != nil {
			return nil, err
		}
		report["already_erased"] = true
		return report, nil
	}

	originals := originalIdentifiers(user, "")
	pseudonym := erasurePseudonym(userID)
	ts := utils.ToISO(utils.UTCNow())
	counts := map[string]any{}

	// 1. The user record. Rewritten in place so every reference to this id
	//    still resolves; the roster entry survives as a tombstone that can be
	//    seen but not signed in as and not paged.
	_, err = e.store.UpdateCollections(ctx, []string{"users"}, []string{"users"},
		func(state *store.State) (any, error) {
			current := state.Users[userID]
			if current == nil {
				return nil, errNotFound("user " + userID + " not found")
			}
			for _, f := range personalFields {
				delete(current, f)
			}
			current["name"] = pseudonym
			current["username"] = pseudonym
			current["email"] = ""
			current["phone"] = ""
			current["telegram_id"] = ""
			// No role means no sign-in, and off duty means no paging: an erased
			// person must not be reachable, and must not appear as somebody the
			// escalation could still try.
			current["role"] = ""
			current["on_duty"] = false
			current["notification_targets"] = []any{map[string]any{"type": "log", "target": ""}}
			delete(current, "notification_policies")
			current["erased_at"] = ts
			current["updated_at"] = ts
			state.AuditEvents = append(state.AuditEvents,
				e.buildAuditEvent(ctx, AuditErase, "user", userID, map[string]any{"pseudonym": pseudonym}))
			return copyMap(current), nil
		}, advisoryLock["update_user"])
	if err != nil {
		return nil, err
	}

	// 2. Credentials and sessions. Sessions are deleted, not revoked: the row
	//    itself carries the IP and user agent.
	if err := e.store.DeleteUserPassword(ctx, userID); err != nil {
		return nil, fmt.Errorf("delete credentials: %w", err)
	}
	n, err := e.store.DeleteUserWebSessions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("delete web sessions: %w", err)
	}
	counts["web_sessions_deleted"] = n

	// 3. Device- and channel-shaped records, which exist only to reach this
	//    person and have no meaning without them.
	for _, collection := range []string{"mobile_sessions", "mobile_devices", "chatops_channels"} {
		items, err := e.store.ListItemsIn(ctx, collection, "user_id", []any{userID})
		if err != nil {
			return nil, err
		}
		deleted := 0
		for _, item := range items {
			if _, err := e.store.DeleteItem(ctx, collection, utils.StrVal(item, "id")); err != nil {
				return nil, fmt.Errorf("delete %s: %w", collection, err)
			}
			deleted++
		}
		counts[collection+"_deleted"] = deleted
	}

	// 4. Delivery addresses on the paging history.
	scrubbed, err := e.store.ScrubUserNotificationTargets(ctx, userID, pseudonym)
	if err != nil {
		return nil, fmt.Errorf("scrub notification targets: %w", err)
	}
	counts["notification_targets_scrubbed"] = scrubbed

	// 5. The audit trail: the events stay, the person's name and IP go.
	audited, err := e.store.PseudonymiseAuditActor(ctx, userID, pseudonym)
	if err != nil {
		return nil, fmt.Errorf("pseudonymise audit actor: %w", err)
	}
	counts["audit_events_pseudonymised"] = audited

	// 6. Schedules. A rotation that still names an erased person pages nobody
	//    for that slot while the slot looks covered — the same reason the
	//    ordinary delete does this.
	if removed, err := e.purgeUserFromSchedules(ctx, userID); err != nil {
		return nil, fmt.Errorf("purge from schedules: %w", err)
	} else {
		counts["schedules_updated"] = removed
	}

	slog.Info("user_personal_data_erased", "user_id", userID, "pseudonym", pseudonym)

	report, err := e.verifyErasure(ctx, userID, originals)
	if err != nil {
		return nil, err
	}
	report["erased_at"] = ts
	report["pseudonym"] = pseudonym
	report["counts"] = counts
	return report, nil
}

// originalIdentifiers collects the values that must no longer be findable.
// Short values are excluded: a one- or two-character name would match half the
// database and turn the verification into noise.
func originalIdentifiers(user map[string]any, except string) []string {
	var out []string
	for _, f := range personalFields {
		v := strings.TrimSpace(utils.StrVal(user, f))
		if len(v) < 3 || (except != "" && v == except) {
			continue
		}
		out = append(out, v)
	}
	return dedupe(out)
}

// erasurePseudonym is the stable replacement for one user's identifiers.
// Derived from the id so it is the same on a repeat run (which makes the
// operation idempotent) and unique per person (so two erased users do not
// collide on the username, which is unique).
func erasurePseudonym(userID string) string {
	return "erased-" + analyticsDigest(userID)
}

// verifyErasure reads the data back and looks for the identifiers that were
// supposed to be gone.
//
// This exists because "we ran the erasure" and "the data is gone" are different
// claims, and only the second one answers the question that was asked. It is a
// search over the places the inventory says personal data lives, and it reports
// what it found rather than asserting success: a residue here is a finding, not
// an exception to be swallowed.
//
// What it cannot see is stated in the report rather than left implied: backups,
// the log pipeline, and the analytics store are outside this database, and each
// is bounded by its own retention rather than by this operation.
func (e *Engine) verifyErasure(ctx context.Context, userID string, originals []string) (map[string]any, error) {
	var residue []string
	found := func(where, value string) {
		residue = append(residue, fmt.Sprintf("%s still contains %q", where, value))
	}
	hit := func(where string, hay string) {
		for _, needle := range originals {
			if needle != "" && strings.Contains(hay, needle) {
				found(where, needle)
			}
		}
	}

	user, err := e.store.GetItem(ctx, "users", userID)
	if err != nil {
		return nil, err
	}
	if user != nil {
		for _, f := range personalFields {
			hit("users."+f, utils.StrVal(user, f))
		}
	}

	notifications, err := e.store.ListItemsIn(ctx, "notifications", "user_id", []any{userID})
	if err != nil {
		return nil, err
	}
	ids := make([]any, 0, len(notifications))
	for _, n := range notifications {
		hit("notifications.target", utils.StrVal(n, "target"))
		ids = append(ids, utils.StrVal(n, "id"))
	}
	attempts, err := e.store.ListItemsIn(ctx, "notification_delivery_attempts", "notification_id", ids)
	if err != nil {
		return nil, err
	}
	for _, a := range attempts {
		hit("notification_delivery_attempts.target", utils.StrVal(a, "target"))
	}

	sessions, err := e.store.ListWebSessions(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(sessions) > 0 {
		residue = append(residue, fmt.Sprintf("web_sessions still holds %d row(s) with a client IP", len(sessions)))
	}

	for _, collection := range []string{"mobile_devices", "mobile_sessions", "chatops_channels"} {
		items, err := e.store.ListItemsIn(ctx, collection, "user_id", []any{userID})
		if err != nil {
			return nil, err
		}
		if len(items) > 0 {
			residue = append(residue, fmt.Sprintf("%s still holds %d row(s)", collection, len(items)))
		}
	}

	events, _, err := e.store.ListAuditEvents(ctx, map[string]any{"actor_id": userID}, auditExportLimit, 0)
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		hit("audit_events.actor_name", utils.StrVal(ev, "actor_name"))
		if ip := utils.StrVal(ev, "request_ip"); ip != "" {
			residue = append(residue, "audit_events.request_ip still holds an address")
			break
		}
	}

	sort.Strings(residue)
	return map[string]any{
		"user_id":  userID,
		"verified": len(residue) == 0,
		"residue":  toAnySlice(dedupe(residue)),
		// Named, not hinted at. An erasure report that quietly omits the copies
		// it cannot reach would be the most misleading document in the product.
		"out_of_scope": toAnySlice([]string{
			"database backups and WAL archives — bounded by the backup retention, not by this operation",
			"the log pipeline — bounded by the log store's own retention (see LOG_FORMAT and docs/DATA_INVENTORY.md)",
			"the analytics store (Kafka/ClickHouse), when enabled — bounded by kafkaConsumer.analytics.rawRetentionDays",
			"distributed traces, when enabled — bounded by the collector's retention",
		}),
	}, nil
}

// scheduleMentionsUser reports whether a schedule document names this user
// anywhere: in a rotation's participants, in a legacy shift, or in an override.
func scheduleMentionsUser(schedule map[string]any, userID string) bool {
	data, _ := schedule["data"].(map[string]any)
	if data == nil {
		data = schedule
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch typed := v.(type) {
		case string:
			return typed == userID
		case []any:
			for _, item := range typed {
				if walk(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range typed {
				if walk(item) {
					return true
				}
			}
		}
		return false
	}
	return walk(data)
}

// toAnyMaps widens a slice of records for a JSON response.
func toAnyMaps(items []map[string]any) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}
