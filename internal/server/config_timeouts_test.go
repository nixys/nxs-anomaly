package server

import (
	"testing"
	"time"
)

func TestConfigHTTPTimeoutDefaults(t *testing.T) {
	cfg := ConfigFromEnv()
	checks := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ReadHeaderTimeout", cfg.ReadHeaderTimeout, 5 * time.Second},
		{"ReadTimeout", cfg.ReadTimeout, 15 * time.Second},
		{"WriteTimeout", cfg.WriteTimeout, 30 * time.Second},
		{"IdleTimeout", cfg.IdleTimeout, 60 * time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s default = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestConfigHTTPTimeoutOverride(t *testing.T) {
	t.Setenv("NXS_ANOMALY_HTTP_READ_HEADER_TIMEOUT_SECONDS", "7")
	t.Setenv("NXS_ANOMALY_HTTP_IDLE_TIMEOUT_SECONDS", "120")
	cfg := ConfigFromEnv()
	if cfg.ReadHeaderTimeout != 7*time.Second {
		t.Errorf("ReadHeaderTimeout override = %v, want 7s", cfg.ReadHeaderTimeout)
	}
	if cfg.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout override = %v, want 120s", cfg.IdleTimeout)
	}
	// Unset values keep their defaults.
	if cfg.WriteTimeout != 30*time.Second {
		t.Errorf("WriteTimeout = %v, want default 30s", cfg.WriteTimeout)
	}
}
