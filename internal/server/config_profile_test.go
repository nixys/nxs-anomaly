package server

import "testing"

// TestProductionProfileRateLimits: the production profile enables request rate
// limits by default; an explicit env value (including 0 to disable) wins, and
// non-production leaves them off.
func TestProductionProfileRateLimits(t *testing.T) {
	t.Run("production defaults on", func(t *testing.T) {
		t.Setenv("NXS_ANOMALY_PROFILE", "production")
		t.Setenv("NXS_ANOMALY_WEBHOOK_RATE", "")
		t.Setenv("NXS_ANOMALY_API_RATE", "")
		cfg := ConfigFromEnv()
		if cfg.WebhookRate <= 0 || cfg.APIRate <= 0 {
			t.Errorf("production must set default rate limits, got webhook=%v api=%v", cfg.WebhookRate, cfg.APIRate)
		}
	})

	t.Run("explicit 0 disables even in production", func(t *testing.T) {
		t.Setenv("NXS_ANOMALY_PROFILE", "production")
		t.Setenv("NXS_ANOMALY_API_RATE", "0")
		if ConfigFromEnv().APIRate != 0 {
			t.Error("explicit API_RATE=0 must win over the profile")
		}
	})

	t.Run("non-production off", func(t *testing.T) {
		t.Setenv("NXS_ANOMALY_PROFILE", "")
		t.Setenv("NXS_ANOMALY_WEBHOOK_RATE", "")
		t.Setenv("NXS_ANOMALY_API_RATE", "")
		cfg := ConfigFromEnv()
		if cfg.WebhookRate != 0 || cfg.APIRate != 0 {
			t.Error("non-production must default to no rate limit")
		}
	})
}
