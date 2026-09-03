package store

import (
	"context"
	"fmt"
)

// PseudonymiseAuditActor removes the personal data an audit row holds about one
// actor without touching the trail.
//
// The audit table is append-only and the trigger enforces it, so this uses the
// same narrow, transaction-scoped hole as PruneAuditEvents. It is worth being
// exact about what is given up and what is not, because "we edited the audit
// log" is a sentence that deserves suspicion:
//
//   - Nothing about *what happened* changes. The event id, the actor id, the
//     action, the entity, the timestamps, the request and trace ids and the
//     payload are all left alone. Every question the trail answers — who did
//     this, in which order, as part of which request — it still answers
//     afterwards, and the actor id still joins the events of one person together.
//   - What goes is the two columns that carry personal data rather than
//     evidence: actor_name (a human's name) and request_ip (an address that
//     identifies a person's device and location).
//
// The alternative designs are both worse. Deleting the rows destroys the record
// of actions an erased user took — including actions taken *on* other people's
// data, which is exactly what an audit trail exists to preserve. Leaving the
// name in place means an erasure request cannot be honoured at all, since the
// name is the personal datum being asked about.
//
// No request path reaches this method; it is called from the erasure flow, which
// records its own audit event about having run.
func (s *pgStore) PseudonymiseAuditActor(ctx context.Context, actorID, pseudonym string) (int, error) {
	if actorID == "" {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rolled back only if Commit did not run

	// Scoped to this transaction: a crash mid-update leaves the trigger enabled,
	// because the ALTER rolls back with everything else.
	if _, err := tx.Exec(ctx,
		"ALTER TABLE nxs_anomaly_audit_events DISABLE TRIGGER nxs_anomaly_audit_events_append_only"); err != nil {
		return 0, fmt.Errorf("disable append-only trigger: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE nxs_anomaly_audit_events
		    SET actor_name = $2, request_ip = ''
		  WHERE actor_id = $1
		    AND (actor_name <> $2 OR request_ip <> '')`,
		actorID, pseudonym)
	if err != nil {
		return 0, fmt.Errorf("pseudonymise audit actor: %w", err)
	}
	if _, err := tx.Exec(ctx,
		"ALTER TABLE nxs_anomaly_audit_events ENABLE TRIGGER nxs_anomaly_audit_events_append_only"); err != nil {
		return 0, fmt.Errorf("re-enable append-only trigger: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// DeleteUserWebSessions removes a user's browser sessions outright, rather than
// revoking them.
//
// Revocation is the right operation for signing somebody out: the row stays, and
// "this session existed and was ended" is useful during an incident. Erasure is
// the other case — the row itself carries the client IP and user agent, which
// are the personal data being erased, so here it goes.
func (s *pgStore) DeleteUserWebSessions(ctx context.Context, userID string) (int, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM nxs_anomaly_web_sessions WHERE user_id = $1", userID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ScrubUserNotificationTargets replaces the delivery address on everything this
// user was ever paged through, in the notifications and in the per-attempt
// records that reference them.
//
// The rows stay. "This person was paged at 03:12 on the second escalation step
// and did not answer" is the record an incident review runs on, and deleting it
// to erase an address would destroy the account of what happened to somebody
// else's outage. What goes is the address itself — the phone number, the chat
// id, the mail address — which is the personal datum, and which the record does
// not need in order to say what it says.
func (s *pgStore) ScrubUserNotificationTargets(ctx context.Context, userID, replacement string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rolled back only if Commit did not run

	nt, err := tx.Exec(ctx,
		`UPDATE nxs_anomaly_notifications
		    SET data = jsonb_set(data, '{target}', to_jsonb($2::text), true)
		  WHERE user_id = $1
		    AND COALESCE(data->>'target', '') <> $2`,
		userID, replacement)
	if err != nil {
		return 0, fmt.Errorf("scrub notification targets: %w", err)
	}
	// The attempts are reached through their notification rather than by a
	// user column, because they have none: an attempt belongs to a
	// notification, and the notification is what belongs to a person.
	at, err := tx.Exec(ctx,
		`UPDATE nxs_anomaly_notification_delivery_attempts a
		    SET target = $2,
		        data = jsonb_set(a.data, '{target}', to_jsonb($2::text), true)
		   FROM nxs_anomaly_notifications n
		  WHERE a.notification_id = n.id
		    AND n.user_id = $1
		    AND (a.target IS DISTINCT FROM $2 OR COALESCE(a.data->>'target', '') <> $2)`,
		userID, replacement)
	if err != nil {
		return 0, fmt.Errorf("scrub delivery attempt targets: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(nt.RowsAffected() + at.RowsAffected()), nil
}
