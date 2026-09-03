-- Cluster-wide token buckets for the sign-in rate limit.
--
-- The limiter used to live in a map inside each API process. With one replica
-- that is a global limit; with N replicas it is N independent limits, so the
-- effective allowance is N times the configured one and a brute-force attempt
-- spread across pods gets N times the attempts before anything says no. The
-- limit that most needs to be global was the one that scaled with the deployment.
--
-- Only the auth limiters moved here. The ingest limiter stays in memory on
-- purpose: it sits on the hot path, and a database round-trip per accepted alert
-- would cost more than the protection is worth (see docs/SECURITY_PROFILE.md).
--
-- Rows are ephemeral. A bucket that has had time to refill completely is
-- indistinguishable from one that never existed, so the worker's retention sweep
-- deletes them; nothing here is history and nothing reads it but the limiter.

CREATE TABLE IF NOT EXISTS nxs_anomaly_rate_buckets (
    bucket_key text primary key,
    tokens     double precision not null,
    updated_at timestamptz not null default now()
);

-- The retention sweep deletes by age; without this it is a full scan every cycle.
CREATE INDEX IF NOT EXISTS nxs_anomaly_rate_buckets_updated_at_idx
    ON nxs_anomaly_rate_buckets (updated_at);
