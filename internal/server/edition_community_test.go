package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestCommunitySignInOffersNoSSOButExplainsWhy is the community half of the
// sign-in contract. The screen must not simply draw one fewer button: a person
// looking for the SSO they use elsewhere should learn that this build has none,
// rather than concluding the deployment is broken.
func TestCommunitySignInOffersNoSSOButExplainsWhy(t *testing.T) {
	srv, _ := newSessionServer(t)

	w := srv.call(http.MethodGet, "/api/v1/auth/methods", "")
	var methods map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &methods); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if methods["oidc"] != false {
		t.Errorf("oidc = %v, want false: this build has no provider", methods["oidc"])
	}
	if methods["sso_available"] != false {
		t.Errorf("sso_available = %v, want false", methods["sso_available"])
	}
	if methods["edition"] != "community" {
		t.Errorf("edition = %v, want community", methods["edition"])
	}
	// Password sign-in is unaffected: the edition removes a door, not the house.
	if methods["password"] != true {
		t.Errorf("password = %v, want true", methods["password"])
	}
}

// TestCommunityOIDCEndpointAnswers501NotSilence pins the difference a caller
// can act on. 404 is the enterprise build's answer when nobody configured an
// issuer — fixable by the operator. 501 says the code is not here at all.
// Falling through to 401 "authentication required", which is what an unclaimed
// path would produce, tells the caller nothing.
func TestCommunityOIDCEndpointAnswers501NotSilence(t *testing.T) {
	srv, _ := newSessionServer(t)

	for _, path := range []string{"/api/v1/auth/oidc/login", "/api/v1/auth/oidc/callback"} {
		w := srv.call(http.MethodGet, path, "")
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s: got %d, want 501; body %s", path, w.Code, w.Body.String())
		}
	}
}

// TestCommunityCapabilitiesNameTheEdition: the capabilities report is what the
// UI reads to draw a feature as blocked rather than absent, so the state has to
// be the one that says "a different edition", not "not set up".
func TestCommunityCapabilitiesNameTheEdition(t *testing.T) {
	srv, _ := newSessionServer(t)

	caps := readCapabilities(t, srv)
	for _, name := range []string{"sso", "team_scoping", "analytics_stream"} {
		if got := caps[name].State; got != capNotInEdition {
			t.Errorf("%s state = %q, want %q", name, got, capNotInEdition)
		}
	}
}
