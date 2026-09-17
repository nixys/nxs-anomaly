package server

import (
	"context"
	"net/http"
	"testing"
)

// A process waiting for PostgreSQL or a migration is alive and not ready. The
// liveness probe must see the first, the readiness probe the second, and the
// port must stay open across the handover to the started service.
func TestFrontdoorIsLiveButNotReadyUntilHandedOver(t *testing.T) {
	fd, err := OpenFrontdoor("127.0.0.1:0", Config{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Shutdown(context.Background()) }()
	base := "http://" + fd.Addr().String()

	status := func(path string) int {
		t.Helper()
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := status("/live"); got != http.StatusOK {
		t.Errorf("/live while starting = %d, want 200", got)
	}
	for _, path := range []string{"/health", "/ready", "/api/v1/users"} {
		if got := status(path); got != http.StatusServiceUnavailable {
			t.Errorf("%s while starting = %d, want 503", path, got)
		}
	}

	fd.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	if got := status("/health"); got != http.StatusTeapot {
		t.Errorf("/health after handover = %d, want the service's answer", got)
	}
}
