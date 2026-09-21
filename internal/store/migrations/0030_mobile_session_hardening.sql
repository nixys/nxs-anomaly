-- Mobile sessions become a real credential: hashed, expiring, and issued by
-- pairing a phone rather than by an administrator.
--
-- Until now the session token was stored as issued (a 48-bit id, in plaintext)
-- and never expired. Those tokens are not hashed in place: they were weak to
-- begin with, and none of them could authenticate a request without an API key
-- as well, so nobody depends on one. They are revoked and erased instead, which
-- also takes the plaintext out of the database.
ALTER TABLE nxs_anomaly_mobile_sessions ADD COLUMN IF NOT EXISTS expires_at timestamptz;

UPDATE nxs_anomaly_mobile_sessions
SET token = NULL,
    revoked_at = COALESCE(revoked_at, now()),
    data = (data - 'token')
        || jsonb_build_object(
               'is_active', false,
               'revoked_at', COALESCE(data->>'revoked_at',
                   to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"+00:00"')))
WHERE expires_at IS NULL;

-- Pairing codes live in the table 0014 created for the Grafana plugin's
-- verification tokens, which lost its last reader when that bridge was removed.
-- The shape is the same (a secret, whose user it is, when it stops working);
-- the token column now holds a hash of the code. Anything left from the old
-- use is meaningless now.
DELETE FROM nxs_anomaly_mobile_verification_tokens;
