package utils

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// captureWarnings routes slog to a buffer for the duration of a test.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestEnvInt(t *testing.T) {
	const key = "NXS_ANOMALY_TEST_ENV_INT"
	cases := []struct {
		name     string
		value    string
		def, min int
		want     int
		warns    bool
	}{
		{"unset is the default", "", 30, 0, 30, false},
		// Issue #24: 0 has to reach the caller where it is allowed — it is
		// how a setting is turned off.
		{"zero where allowed", "0", 30, 0, 0, false},
		{"zero below the minimum", "0", 10, 1, 10, true},
		{"negative", "-5", 30, 0, 30, true},
		{"not a number", "30s", 30, 0, 30, true},
		{"surrounding space", " 45 ", 30, 0, 45, false},
		{"a valid value", "120", 30, 1, 120, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureWarnings(t)
			t.Setenv(key, tc.value)
			if got := EnvInt(key, tc.def, tc.min); got != tc.want {
				t.Errorf("EnvInt(%q) = %d, want %d", tc.value, got, tc.want)
			}
			warned := strings.Contains(logs.String(), "invalid_setting")
			if warned != tc.warns {
				t.Errorf("warning logged = %v, want %v (log: %q)", warned, tc.warns, logs.String())
			}
			if tc.warns && !strings.Contains(logs.String(), key) {
				t.Errorf("warning does not name the variable: %q", logs.String())
			}
		})
	}
}

func TestEnvSeconds(t *testing.T) {
	const key = "NXS_ANOMALY_TEST_ENV_SECONDS"
	captureWarnings(t)
	t.Setenv(key, "0")
	if got := EnvSeconds(key, time.Minute, 0); got != 0 {
		t.Errorf("0 with min 0 = %v, want 0", got)
	}
	if got := EnvSeconds(key, time.Minute, 1); got != time.Minute {
		t.Errorf("0 with min 1 = %v, want the default", got)
	}
	t.Setenv(key, "90")
	if got := EnvSeconds(key, time.Minute, 1); got != 90*time.Second {
		t.Errorf("90 = %v, want 90s", got)
	}
}
