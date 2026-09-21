package store

import (
	"testing"
	"time"
)

const testDSN = "host=localhost dbname=x user=x sslmode=disable"

// TestStatementTimeoutZeroSendsNoParameter is issue #24: behind PgBouncer the
// statement_timeout startup parameter makes every connection fail, and 0 — the
// documented opt-out — used to be replaced by the 30 s default.
func TestStatementTimeoutZeroSendsNoParameter(t *testing.T) {
	t.Setenv("NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS", "0")
	cfg, err := poolConfigFromEnv(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := cfg.ConnConfig.RuntimeParams["statement_timeout"]; ok {
		t.Errorf("statement_timeout sent as %q, want no parameter", v)
	}
}

func TestStatementTimeoutDefaultAndExplicit(t *testing.T) {
	cfg, err := poolConfigFromEnv(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "30000" {
		t.Errorf("default statement_timeout = %q, want 30000", got)
	}
	t.Setenv("NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS", "5")
	cfg, _ = poolConfigFromEnv(testDSN)
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "5000" {
		t.Errorf("statement_timeout = %q, want 5000", got)
	}
}

// TestPoolSettingsRejectZeroWherePgxBreaks: for these, 0 is not "off" to
// pgxpool — it closes every connection on release, or panics on the health
// check ticker — so it keeps the default.
func TestPoolSettingsRejectZeroWherePgxBreaks(t *testing.T) {
	for _, key := range []string{
		"NXS_ANOMALY_DB_POOL_MAX",
		"NXS_ANOMALY_DB_POOL_MAX_CONN_LIFETIME_SECONDS",
		"NXS_ANOMALY_DB_POOL_MAX_CONN_IDLE_SECONDS",
		"NXS_ANOMALY_DB_POOL_HEALTHCHECK_SECONDS",
	} {
		t.Setenv(key, "0")
	}
	t.Setenv("NXS_ANOMALY_DB_POOL_MIN", "0")
	cfg, err := poolConfigFromEnv(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 10 {
		t.Errorf("MaxConns = %d, want the default 10", cfg.MaxConns)
	}
	if cfg.MinConns != 0 {
		t.Errorf("MinConns = %d, want 0 (allowed: keep no idle connections)", cfg.MinConns)
	}
	if cfg.MaxConnLifetime != time.Hour || cfg.MaxConnIdleTime != 30*time.Minute || cfg.HealthCheckPeriod != time.Minute {
		t.Errorf("lifetime/idle/healthcheck = %v/%v/%v, want the defaults",
			cfg.MaxConnLifetime, cfg.MaxConnIdleTime, cfg.HealthCheckPeriod)
	}
}
