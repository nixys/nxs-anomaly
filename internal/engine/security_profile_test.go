package engine

import (
	"context"
	"testing"
)

// TestCreateIntegrationRejectsInlineSecretInProduction exercises the ban through
// the real CreateIntegration path: an inline webhook_secret is refused under the
// production profile, an env: reference is accepted.
func TestCreateIntegrationRejectsInlineSecretInProduction(t *testing.T) {
	t.Setenv("NXS_ANOMALY_PROFILE", "production")
	e, _ := cachedEngine(newMemStore())
	if _, err := e.CreateIntegration(context.Background(), map[string]any{
		"name": "acme", "webhook_secret": "plaintext",
	}); err == nil {
		t.Fatal("inline webhook_secret must be rejected in the production profile")
	}
	if _, err := e.CreateIntegration(context.Background(), map[string]any{
		"name": "acme2", "webhook_secret": "env:NXS_ANOMALY_ACME_HMAC",
	}); err != nil {
		t.Fatalf("env: reference must be accepted: %v", err)
	}
}

// TestProductionProfileDeliveryDefaults: the production profile turns the SSRF
// guard and the delivery circuit breaker on by default; an explicit env value
// still overrides. These are security regressions — a silent revert to the
// permissive defaults would reopen an SSRF and a hammer-the-dead-provider hole.
func TestProductionProfileDeliveryDefaults(t *testing.T) {
	t.Run("production on by default", func(t *testing.T) {
		t.Setenv("NXS_ANOMALY_PROFILE", "production")
		cfg := DeliveryConfigFromEnv()
		if !cfg.BlockPrivateWebhooks {
			t.Error("production must block private webhooks (SSRF guard) by default")
		}
		if cfg.CircuitBreakerThreshold == 0 {
			t.Error("production must enable the delivery circuit breaker by default")
		}
	})

	t.Run("non-production off by default", func(t *testing.T) {
		t.Setenv("NXS_ANOMALY_PROFILE", "")
		cfg := DeliveryConfigFromEnv()
		if cfg.BlockPrivateWebhooks {
			t.Error("non-production must not block private webhooks unless asked")
		}
	})

	t.Run("explicit env overrides the profile", func(t *testing.T) {
		t.Setenv("NXS_ANOMALY_PROFILE", "production")
		t.Setenv("NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS", "false")
		if DeliveryConfigFromEnv().BlockPrivateWebhooks {
			t.Error("explicit BLOCK_PRIVATE_WEBHOOKS=false must win over the profile")
		}
	})
}

// TestRejectInlineSecret: in the production profile a new inline secret is
// refused; an env: reference and an empty value are fine. Outside production the
// legacy inline path stays open so an upgrade keeps working.
func TestRejectInlineSecret(t *testing.T) {
	t.Run("production rejects inline, accepts reference/empty", func(t *testing.T) {
		t.Setenv("NXS_ANOMALY_PROFILE", "production")
		if err := rejectInlineSecret("webhook_secret", "plaintext"); err == nil {
			t.Error("inline secret must be rejected in production")
		}
		if err := rejectInlineSecret("webhook_secret", "env:NXS_ANOMALY_HMAC"); err != nil {
			t.Errorf("env reference must be accepted: %v", err)
		}
		if err := rejectInlineSecret("webhook_secret", ""); err != nil {
			t.Errorf("empty must be accepted: %v", err)
		}
	})

	t.Run("non-production accepts inline (upgrade compatibility)", func(t *testing.T) {
		t.Setenv("NXS_ANOMALY_PROFILE", "")
		if err := rejectInlineSecret("webhook_secret", "plaintext"); err != nil {
			t.Errorf("inline secret must be allowed outside production: %v", err)
		}
	})
}
