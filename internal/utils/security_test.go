package utils

import "testing"

func TestProductionProfile(t *testing.T) {
	t.Setenv("NXS_ANOMALY_PROFILE", "")
	if ProductionProfile() {
		t.Error("empty profile must not be production")
	}
	t.Setenv("NXS_ANOMALY_PROFILE", "Production")
	if !ProductionProfile() {
		t.Error("profile=Production must be production (case-insensitive)")
	}
	t.Setenv("NXS_ANOMALY_PROFILE", "dev")
	if ProductionProfile() {
		t.Error("profile=dev must not be production")
	}
}

func TestResolveSecretRef(t *testing.T) {
	t.Setenv("MY_HMAC", "s3cr3t")
	cases := []struct{ in, want string }{
		{"env:MY_HMAC", "s3cr3t"},            // reference resolves from env
		{"env: MY_HMAC ", "s3cr3t"},          // whitespace tolerated
		{"env:MISSING", ""},                  // missing var → empty
		{"literal-secret", "literal-secret"}, // legacy inline passes through
		{"", ""},
	}
	for _, c := range cases {
		if got := ResolveSecretRef(c.in); got != c.want {
			t.Errorf("ResolveSecretRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if !IsSecretRef("env:X") || IsSecretRef("plain") {
		t.Error("IsSecretRef misclassified")
	}
}
