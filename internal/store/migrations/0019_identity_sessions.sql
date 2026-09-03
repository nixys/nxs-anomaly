-- Local user credentials and browser sessions.
--
-- Credentials live in their own table rather than in nxs_anomaly_users.data
-- on purpose: the management API serves that JSONB document verbatim through
-- the generic collection endpoints, so a password hash stored there would be
-- one GET /api/v1/users away from disclosure. A separate table cannot leak by
-- accident — it is only reachable through the methods declared for it.
--
-- Sessions store a SHA-256 of the token, never the token itself, so a database
-- dump (or a leaked backup) does not hand over live sessions.

CREATE TABLE IF NOT EXISTS nxs_anomaly_user_credentials (
    user_id       TEXT        PRIMARY KEY,
    password_hash TEXT        NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS nxs_anomaly_web_sessions (
    id         TEXT        PRIMARY KEY,
    token_hash TEXT        NOT NULL UNIQUE,
    user_id    TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    request_ip TEXT        NOT NULL DEFAULT '',
    user_agent TEXT        NOT NULL DEFAULT ''
);

-- Every authenticated request looks a session up by token hash; the partial
-- predicate keeps the index to live sessions, which is the only case that is
-- ever queried.
CREATE INDEX IF NOT EXISTS nxs_anomaly_web_sessions_live_idx
    ON nxs_anomaly_web_sessions (token_hash)
    WHERE revoked_at IS NULL;

-- "Sign this user out everywhere" and the expiry sweep are the only other
-- accesses.
CREATE INDEX IF NOT EXISTS nxs_anomaly_web_sessions_user_idx
    ON nxs_anomaly_web_sessions (user_id);
CREATE INDEX IF NOT EXISTS nxs_anomaly_web_sessions_expires_idx
    ON nxs_anomaly_web_sessions (expires_at);

-- Deleting a user must not leave a usable credential or session behind. The FK
-- is added NOT VALID, consistent with the other constraints in this schema:
-- validating it would take a full scan on upgrade, and new rows are checked
-- either way.
ALTER TABLE nxs_anomaly_user_credentials
    DROP CONSTRAINT IF EXISTS nxs_anomaly_user_credentials_user_fk;
ALTER TABLE nxs_anomaly_user_credentials
    ADD CONSTRAINT nxs_anomaly_user_credentials_user_fk
    FOREIGN KEY (user_id) REFERENCES nxs_anomaly_users(id) ON DELETE CASCADE NOT VALID;

ALTER TABLE nxs_anomaly_web_sessions
    DROP CONSTRAINT IF EXISTS nxs_anomaly_web_sessions_user_fk;
ALTER TABLE nxs_anomaly_web_sessions
    ADD CONSTRAINT nxs_anomaly_web_sessions_user_fk
    FOREIGN KEY (user_id) REFERENCES nxs_anomaly_users(id) ON DELETE CASCADE NOT VALID;
