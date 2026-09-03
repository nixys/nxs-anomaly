package engine

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// capturingHandler records the messages logged through it so a test can assert
// whether warnClaimTimeoutRisk fired.
type capturingHandler struct {
	msgs []string
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.msgs = append(h.msgs, r.Message)
	return nil
}
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingHandler) has(msg string) bool {
	for _, m := range h.msgs {
		if m == msg {
			return true
		}
	}
	return false
}

// TestWarnClaimTimeoutRiskCycleBound isolates the WorkerCycleTimeout check by
// making the worst-case stage negligible (webhook 1s / concurrency 1000 ≈ 1s).
func TestWarnClaimTimeoutRiskCycleBound(t *testing.T) {
	cases := []struct {
		name     string
		claim    time.Duration
		cycle    time.Duration
		wantWarn bool
	}{
		{"claim below cycle bound warns", 60 * time.Second, 120 * time.Second, true},
		{"claim equal cycle bound warns", 120 * time.Second, 120 * time.Second, true},
		{"claim above cycle bound is safe", 120 * time.Second, 60 * time.Second, false},
		{"no cycle bound: nothing to validate", 60 * time.Second, 0, false},
		{"reaper disabled: no warning", 0, 120 * time.Second, false},
	}

	prev := slog.Default()
	defer slog.SetDefault(prev)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &capturingHandler{}
			slog.SetDefault(slog.New(h))
			cfg := DeliveryConfig{
				ClaimTimeout:          tc.claim,
				WorkerCycleTimeout:    tc.cycle,
				WebhookTimeoutSeconds: 1,
				DeliveryConcurrency:   1000,
			}
			cfg.warnClaimTimeoutRisk()
			if got := h.has("claim_timeout_too_small"); got != tc.wantWarn {
				t.Errorf("cycle-bound warn = %v, want %v (claim=%v cycle=%v)", got, tc.wantWarn, tc.claim, tc.cycle)
			}
		})
	}
}

// TestWarnClaimTimeoutRiskWorstCaseStage covers the default-config hazard: worst
// case = WorkerBatchLimit(500) × webhook(5s) / concurrency(8) ≈ 313s, so a 300s
// ClaimTimeout warns while the derived default does not.
func TestWarnClaimTimeoutRiskWorstCaseStage(t *testing.T) {
	cases := []struct {
		name     string
		claim    time.Duration
		wantWarn bool
	}{
		{"old 300s default is below worst case", 300 * time.Second, true},
		{"derived default is safe", defaultClaimTimeout(5, 8), false},
		{"reaper disabled: no warning", 0, false},
	}

	prev := slog.Default()
	defer slog.SetDefault(prev)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &capturingHandler{}
			slog.SetDefault(slog.New(h))
			cfg := DeliveryConfig{
				ClaimTimeout:          tc.claim,
				WebhookTimeoutSeconds: 5,
				DeliveryConcurrency:   8,
			}
			cfg.warnClaimTimeoutRisk()
			if got := h.has("claim_timeout_below_worst_case_stage"); got != tc.wantWarn {
				t.Errorf("worst-case warn = %v, want %v (claim=%v)", got, tc.wantWarn, tc.claim)
			}
		})
	}
}
