package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// capabilities_test.go runs in both editions on purpose. The states it asserts
// are derived from the edition constants rather than written out, so the same
// test says "sso is unavailable in this edition" in one build and "sso is
// present but unconfigured" in the other — and fails if a build ever reports a
// state that contradicts what it actually ships.

func readCapabilities(t *testing.T, srv *Server) map[string]capability {
	t.Helper()
	// The endpoint is authenticated: it describes the installation, and that is
	// not something to hand out before sign-in. The sign-in screen gets the one
	// bit it needs from /api/v1/auth/methods instead.
	cookie := sessionCookieFrom(t, login(t, srv, "ada", testPassword))
	w := srv.call(http.MethodGet, "/api/v1/capabilities", "", withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("capabilities: got %d, body %s", w.Code, w.Body.String())
	}
	var body struct {
		Edition      string       `json:"edition"`
		Capabilities []capability `json:"capabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if body.Edition != Edition {
		t.Errorf("edition = %q, want %q", body.Edition, Edition)
	}
	out := map[string]capability{}
	for _, c := range body.Capabilities {
		out[c.Name] = c
	}
	return out
}

// TestCapabilitiesSeparateUnavailableFromUnconfigured is the reason the
// endpoint exists. An operator who has not set an issuer can fix that; an
// operator on an edition without the provider cannot, and telling them the same
// thing wastes their time.
func TestCapabilitiesSeparateUnavailableFromUnconfigured(t *testing.T) {
	srv, _ := newSessionServer(t)

	caps := readCapabilities(t, srv)
	for _, name := range []string{"sso", "team_scoping", "analytics_stream"} {
		if _, ok := caps[name]; !ok {
			t.Fatalf("capability %q missing from the report", name)
		}
		if caps[name].Detail == "" {
			t.Errorf("capability %q reports no detail; a state with no sentence is not actionable", name)
		}
	}

	// Nothing is configured in the test server, so each feature is either
	// missing from the build or merely unset — never "available".
	wantSSO := capNotConfigured
	if !editionHasSSO {
		wantSSO = capNotInEdition
	}
	if got := caps["sso"].State; got != wantSSO {
		t.Errorf("sso state = %q, want %q", got, wantSSO)
	}

	wantScoping := capNotConfigured
	if !editionHasTeamScoping {
		wantScoping = capNotInEdition
	}
	if got := caps["team_scoping"].State; got != wantScoping {
		t.Errorf("team_scoping state = %q, want %q", got, wantScoping)
	}
}

// TestCapabilitiesReportConfiguredFeaturesAsAvailable pins the third state:
// once the deployment does set the feature up, the report must move off
// "not_configured" — otherwise the endpoint would be a static description of
// the build and nobody would trust it.
func TestCapabilitiesReportConfiguredFeaturesAsAvailable(t *testing.T) {
	if !editionHasTeamScoping {
		t.Skip("this edition ships no team boundary; the community state is asserted above")
	}
	srv, _ := newSessionServer(t)
	srv.cfg.TeamScoping = true

	if got := readCapabilities(t, srv)["team_scoping"].State; got != capAvailable {
		t.Errorf("team_scoping state = %q, want %q once the flag is on", got, capAvailable)
	}
}
