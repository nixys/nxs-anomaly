package store

import (
	"context"
	"encoding/json"
	"time"
)

// store_identity.go covers the two tables added by migration 0019: local user
// credentials and browser sessions. Neither is a State collection — they are
// never loaded into a snapshot, never mutated through the generic CRUD paths,
// and must never be serialised to an API response, so they get narrow typed
// methods instead.

// WebSession is a live browser session. The token itself is never stored or
// returned: callers hold it, the database holds its SHA-256.
type WebSession struct {
	ID        string
	UserID    string
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time
	RequestIP string
	UserAgent string
}

// SetUserPassword stores (or replaces) a user's password hash. The hash format
// is the caller's concern; this layer only persists the string.
func (s *pgStore) SetUserPassword(ctx context.Context, userID, passwordHash string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO nxs_anomaly_user_credentials(user_id, password_hash, updated_at)
		 VALUES($1, $2, now())
		 ON CONFLICT(user_id) DO UPDATE SET password_hash=EXCLUDED.password_hash, updated_at=now()`,
		userID, passwordHash)
	return err
}

// GetUserPasswordHash returns the stored hash, or ok=false when the user has no
// password set — a roster entry that can be paged but cannot sign in.
func (s *pgStore) GetUserPasswordHash(ctx context.Context, userID string) (string, bool, error) {
	var hash string
	err := s.pool.QueryRow(ctx,
		"SELECT password_hash FROM nxs_anomaly_user_credentials WHERE user_id=$1", userID).Scan(&hash)
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return hash, true, nil
}

// DeleteUserPassword removes a user's ability to sign in with a password. It is
// not an error to call it for a user who never had one.
func (s *pgStore) DeleteUserPassword(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		"DELETE FROM nxs_anomaly_user_credentials WHERE user_id=$1", userID)
	return err
}

// CountUsersWithPassword reports how many users can sign in with a password. It
// exists so startup can tell "no way in at all" from "no API keys, but people
// can log in", which are very different deployments.
func (s *pgStore) CountUsersWithPassword(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM nxs_anomaly_user_credentials").Scan(&n)
	return n, err
}

// FindUserByLogin looks a user up by username or e-mail, case-insensitively.
// Both are accepted because people type whichever they remember.
func (s *pgStore) FindUserByLogin(ctx context.Context, login string) (map[string]any, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT data FROM nxs_anomaly_users
		  WHERE lower(username)=lower($1) OR lower(email)=lower($1)
		  ORDER BY id LIMIT 1`, login)
	if err != nil {
		return nil, err
	}
	items, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items[0], nil
}

// ListTeamIDsForUser returns the teams a user belongs to.
//
// Membership lives in teams.member_ids rather than on the user, so this is a
// containment query rather than a column read. It runs once per request for a
// team-scoped actor, which is why migration 0020 adds a GIN index for it.
func (s *pgStore) ListTeamIDsForUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM nxs_anomaly_teams
		  WHERE data->'member_ids' @> to_jsonb($1::text)
		  ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CreateWebSession persists a new session.
func (s *pgStore) CreateWebSession(ctx context.Context, sess WebSession) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO nxs_anomaly_web_sessions
		   (id, token_hash, user_id, expires_at, request_ip, user_agent)
		 VALUES($1, $2, $3, $4, $5, $6)`,
		sess.ID, sess.TokenHash, sess.UserID, sess.ExpiresAt, sess.RequestIP, sess.UserAgent)
	return err
}

// FindSessionUser resolves a session token hash straight to the user record it
// belongs to, in one round trip.
//
// Joining rather than doing two lookups matters twice over: it is one query per
// authenticated request instead of two, and it reads the *current* user row, so
// a role change or a deleted user takes effect on the next request rather than
// when the session eventually expires.
//
// Returns sessionID == "" when the token matches no live session.
func (s *pgStore) FindSessionUser(ctx context.Context, tokenHash string) (user map[string]any, sessionID string, err error) {
	rows, err := s.pool.Query(ctx,
		`SELECT u.data, s.id FROM nxs_anomaly_web_sessions s
		   JOIN nxs_anomaly_users u ON u.id = s.user_id
		  WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at > now()
		  LIMIT 1`, tokenHash)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, "", rows.Err()
	}
	var raw []byte
	var id string
	if err := rows.Scan(&raw, &id); err != nil {
		return nil, "", err
	}
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, "", err
	}
	return item, id, rows.Err()
}

// ListWebSessions returns a user's live sessions, newest first.
//
// The token hash is returned so the caller can mark which row is the session
// making the request. It is a hash, not a credential, and it never leaves the
// server — see the HTTP layer, which compares and then drops it.
func (s *pgStore) ListWebSessions(ctx context.Context, userID string) ([]WebSession, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, token_hash, user_id, created_at, expires_at, request_ip, user_agent
		   FROM nxs_anomaly_web_sessions
		  WHERE user_id=$1 AND revoked_at IS NULL AND expires_at > now()
		  ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebSession
	for rows.Next() {
		var sess WebSession
		if err := rows.Scan(&sess.ID, &sess.TokenHash, &sess.UserID,
			&sess.CreatedAt, &sess.ExpiresAt, &sess.RequestIP, &sess.UserAgent); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// RevokeWebSessionByID revokes one session, but only if it belongs to userID.
//
// Scoping the update by owner rather than checking ownership first is what
// makes this safe against a guessed id: there is no window between the check
// and the write, and a session belonging to someone else simply matches no row.
func (s *pgStore) RevokeWebSessionByID(ctx context.Context, sessionID, userID string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE nxs_anomaly_web_sessions SET revoked_at=now()
		  WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, sessionID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeWebSession marks one session as revoked. Revoking rather than deleting
// keeps the row for the audit window; the expiry sweep removes it later.
func (s *pgStore) RevokeWebSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE nxs_anomaly_web_sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL",
		tokenHash)
	return err
}

// RevokeUserSessions signs a user out everywhere. It is called whenever the
// user's ability to sign in changes — password reset, password removal,
// deletion — so a stolen or stale session cannot outlive the credential it came
// from.
func (s *pgStore) RevokeUserSessions(ctx context.Context, userID string) (int, error) {
	tag, err := s.pool.Exec(ctx,
		"UPDATE nxs_anomaly_web_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL",
		userID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// DeleteExpiredWebSessions removes sessions that expired or were revoked more
// than a day ago. Called from the worker cycle.
func (s *pgStore) DeleteExpiredWebSessions(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM nxs_anomaly_web_sessions
		  WHERE expires_at < now() - interval '1 day'
		     OR (revoked_at IS NOT NULL AND revoked_at < now() - interval '1 day')`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
