package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/store"
)

func TestRateLimiterEvictsStaleBuckets(t *testing.T) {
	rl := newRateLimiter(1000, 1000)
	staleAt := time.Now().Add(-bucketTTL - time.Minute)
	freshAt := time.Now()

	rl.buckets["stale"] = &bucket{tokens: 1, last: staleAt, lastAccess: staleAt}
	rl.buckets["fresh"] = &bucket{tokens: 1, last: freshAt, lastAccess: freshAt}
	rl.callCount = evictEvery - 1

	if !rl.allow("current") {
		t.Fatalf("current request should be allowed")
	}
	if _, ok := rl.buckets["stale"]; ok {
		t.Fatalf("stale bucket was not evicted")
	}
	if _, ok := rl.buckets["fresh"]; !ok {
		t.Fatalf("fresh bucket was evicted")
	}
	if _, ok := rl.buckets["current"]; !ok {
		t.Fatalf("current bucket was not retained")
	}
}

// stubStore implements store.PostgreSQLStore with only FindIntegrationByKey functional.
// Calling any other method panics, which is acceptable for unit tests that only exercise
// the webhook signature verification path.
type stubStore struct {
	store.PostgreSQLStore
	findIntegration func(ctx context.Context, key string) (map[string]any, error)
}

func (s *stubStore) FindIntegrationByKey(ctx context.Context, key string) (map[string]any, error) {
	return s.findIntegration(ctx, key)
}

// TestVerifyWebhookSig covers the five meaningful code paths in verifyWebhookSig:
// no integration, no secret, valid signature, invalid signature, and store error.
func TestVerifyWebhookSig(t *testing.T) {
	const secret = "test-secret"
	body := map[string]any{"key": "value"}
	raw, _ := json.Marshal(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	validSig := "sha256=" + fmt.Sprintf("%x", mac.Sum(nil))

	tests := []struct {
		name        string
		integration map[string]any
		storeErr    error
		sigHeader   string
		wantSigErr  bool // true → expect errWebhookSigInvalid
		wantAnyErr  bool // true → expect any non-nil error
	}{
		{
			name:        "no integration",
			integration: nil,
		},
		{
			name:        "no secret",
			integration: map[string]any{"id": "int-1"},
		},
		{
			name:        "valid signature",
			integration: map[string]any{"id": "int-1", "webhook_secret": secret},
			sigHeader:   validSig,
		},
		{
			name:        "invalid signature",
			integration: map[string]any{"id": "int-1", "webhook_secret": secret},
			sigHeader:   "sha256=badhash",
			wantSigErr:  true,
		},
		{
			name:       "store error propagated",
			storeErr:   fmt.Errorf("db unavailable"),
			wantAnyErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{
				store: &stubStore{
					findIntegration: func(_ context.Context, _ string) (map[string]any, error) {
						return tc.integration, tc.storeErr
					},
				},
			}
			r := httptest.NewRequest(http.MethodPost, "/hook", nil)
			if tc.sigHeader != "" {
				r.Header.Set("X-Hub-Signature-256", tc.sigHeader)
			}
			err := srv.verifyWebhookSig(r, "some-key", body)
			switch {
			case tc.wantSigErr:
				if err != errWebhookSigInvalid {
					t.Fatalf("expected errWebhookSigInvalid, got %v", err)
				}
			case tc.wantAnyErr:
				if err == nil {
					t.Fatalf("expected non-nil error, got nil")
				}
			default:
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
			}
		})
	}
}

// TestClientIPTrustedProxy verifies that X-Forwarded-For is only honoured when
// the connecting IP falls within the configured trusted-proxy CIDR list.
func TestClientIPTrustedProxy(t *testing.T) {
	_, loopback, _ := net.ParseCIDR("127.0.0.1/8")
	srv := &Server{trustedProxies: []*net.IPNet{loopback}}

	// Trusted proxy: first value from X-Forwarded-For should be returned.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	if got := srv.clientIP(r); got != "10.0.0.1" {
		t.Fatalf("trusted proxy: clientIP = %q, want 10.0.0.1", got)
	}

	// Untrusted proxy: X-Forwarded-For must be ignored.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "10.99.0.1:5000"
	r2.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := srv.clientIP(r2); got != "10.99.0.1" {
		t.Fatalf("untrusted proxy: clientIP = %q, want 10.99.0.1", got)
	}

	// No trusted proxies configured: RemoteAddr always wins.
	srvNone := &Server{}
	r3 := httptest.NewRequest(http.MethodGet, "/", nil)
	r3.RemoteAddr = "192.168.1.1:8080"
	r3.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := srvNone.clientIP(r3); got != "192.168.1.1" {
		t.Fatalf("no proxies: clientIP = %q, want 192.168.1.1", got)
	}
}

// TestDeliveryProviderResponseTypeAssertionRegression is a regression guard for
// B-1: a bare .(string) assertion on a value read back out of the attempt map
// would panic on a nil or non-string database value.
//
// The original guard looked for utils.StrVal on provider_response. That call
// is gone because the hazard is: the provider's answer now travels as a typed
// deliveryOutcome field and is written into the attempt map, never read back
// out of it. The guard therefore checks the property directly — no bare string
// assertion on an attempt field anywhere in the delivery pipeline.
func TestDeliveryProviderResponseTypeAssertionRegression(t *testing.T) {
	data, err := os.ReadFile("../engine/delivery.go")
	if err != nil {
		t.Fatalf("could not read delivery.go: %v", err)
	}
	if bytes.Contains(data, []byte(`r.attempt["provider_response"].(string)`)) ||
		bytes.Contains(data, []byte(`attempt["provider_response"].(string)`)) {
		t.Fatal("delivery.go asserts provider_response to string directly — B-1 regression detected")
	}
	// And the typed carrier is what feeds the notification's terminal state.
	if !bytes.Contains(data, []byte("e.applyOutcome(n, r.outcome, r.finishedAt)")) {
		t.Fatal("delivery.go no longer finalizes notifications through the typed outcome")
	}
}

// TestParseAPIKeys covers role parsing and defaults.
//
// The bare-key case is the one that changed: an automation credential must
// state what it may do, so omitting the role no longer inherits admin.
func TestParseAPIKeys(t *testing.T) {
	got, bare := parseAPIKeys("k1:admin, k2:readonly , k3 , k4:bogus, k5:responder, k6:editor")
	want := map[string]string{
		"k1": string(authz.RoleAdmin),
		"k2": string(authz.RoleViewer), // legacy scope name still accepted
		"k3": string(authz.RoleViewer), // bare key no longer inherits admin
		"k4": string(authz.RoleViewer), // unknown role degrades, never widens
		"k5": string(authz.RoleResponder),
		"k6": string(authz.RoleEditor),
	}
	if len(got) != len(want) {
		t.Fatalf("parseAPIKeys len = %d, want %d (%#v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q role = %q, want %q", k, got[k], v)
		}
	}
	// Counted so startup can warn: the change is otherwise invisible until a
	// write starts failing.
	if bare != 1 {
		t.Errorf("bare key count = %d, want 1", bare)
	}
	if m, n := parseAPIKeys(""); m != nil || n != 0 {
		t.Errorf("empty input should yield a nil map and no bare keys, got %v/%d", m, n)
	}
}

// TestLegacySingleKeyStaysAdmin: NXS_ANOMALY_API_KEY means "the single admin
// key" by definition, so narrowing bare entries must not touch it.
func TestLegacySingleKeyStaysAdmin(t *testing.T) {
	t.Setenv("NXS_ANOMALY_API_KEY", "legacy-admin-key")
	t.Setenv("NXS_ANOMALY_API_KEYS", "")
	cfg := ConfigFromEnv()
	if cfg.APIKeys["legacy-admin-key"] != string(authz.RoleAdmin) {
		t.Fatalf("legacy key role = %q, want admin", cfg.APIKeys["legacy-admin-key"])
	}
	if cfg.BareAPIKeys != 0 {
		t.Errorf("the legacy key must not count as a bare key, got %d", cfg.BareAPIKeys)
	}
}

// TestAuthenticateClosedByDefault is the regression guard for the backlog item:
// a deployment with no keys configured used to serve the management API to
// anyone with admin rights.
func TestAuthenticateClosedByDefault(t *testing.T) {
	srv := &Server{cfg: Config{}}
	if _, ok := srv.authenticate(httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)); ok {
		t.Fatal("no keys configured must reject, not grant admin")
	}
}

// TestAuthenticateAllowAnonymous covers the explicit development escape hatch.
func TestAuthenticateAllowAnonymous(t *testing.T) {
	srv := &Server{cfg: Config{AllowAnonymous: true}}
	actor, ok := srv.authenticate(httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))
	if !ok || actor.Role != authz.RoleAdmin {
		t.Fatalf("allow-anonymous: actor=%+v ok=%v, want admin/true", actor, ok)
	}
	if actor.Kind != authz.KindService || actor.ID != "anonymous" {
		t.Errorf("anonymous actor must be identifiable in audit records, got %+v", actor)
	}
}

// TestAuthenticate covers key matching via both headers and rejection paths.
func TestAuthenticate(t *testing.T) {
	srv := &Server{cfg: Config{APIKeys: map[string]string{
		"adm": string(authz.RoleAdmin),
		"ro":  string(authz.RoleViewer),
	}}}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.Header.Set("X-API-Key", "adm")
	if actor, ok := srv.authenticate(r); !ok || actor.Role != authz.RoleAdmin {
		t.Errorf("admin key: actor=%+v ok=%v", actor, ok)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.Header.Set("Authorization", "Bearer ro")
	if actor, ok := srv.authenticate(r); !ok || actor.Role != authz.RoleViewer {
		t.Errorf("viewer key via Bearer: actor=%+v ok=%v", actor, ok)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.Header.Set("X-API-Key", "nope")
	if _, ok := srv.authenticate(r); ok {
		t.Errorf("unknown key should be rejected")
	}

	if _, ok := srv.authenticate(httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)); ok {
		t.Errorf("missing credentials should be rejected")
	}
}

// TestAPIKeyIDIsStableAndHidesTheSecret checks the audit label never leaks the key.
func TestAPIKeyIDIsStableAndHidesTheSecret(t *testing.T) {
	secret := "super-secret-key"
	id := apiKeyID(secret)
	if id != apiKeyID(secret) {
		t.Fatal("apiKeyID must be stable for the same key")
	}
	if strings.Contains(id, secret) || len(id) != 8 {
		t.Fatalf("apiKeyID(%q) = %q; must be a short opaque label", secret, id)
	}
	if apiKeyID("other-key") == id {
		t.Error("different keys should not share a label")
	}
}

// TestRequiredAction pins the permission matrix: the same verb means different
// things on different paths, which is the whole reason the mapping is
// path-based.
func TestRequiredAction(t *testing.T) {
	cases := []struct {
		method, path string
		want         authz.Action
	}{
		{http.MethodGet, "/api/v1/alert-groups", authz.ActionRead},
		{http.MethodGet, "/api/v1/users", authz.ActionRead},
		{http.MethodPost, "/api/v1/alert-groups/grp-1/acknowledge", authz.ActionRespond},
		{http.MethodPost, "/api/v1/alert-groups/grp-1/resolve", authz.ActionRespond},
		{http.MethodPost, "/api/v1/alert-groups/grp-1/silence", authz.ActionRespond},
		{http.MethodPost, "/api/v1/alert-groups/bulk-acknowledge", authz.ActionRespond},
		{http.MethodPost, "/api/v1/mobile/alert-groups/g1/resolve", authz.ActionRespond},
		{http.MethodPost, "/api/v1/integrations", authz.ActionEdit},
		{http.MethodPut, "/api/v1/escalation-chains/c1", authz.ActionEdit},
		{http.MethodPost, "/api/v1/users", authz.ActionAdmin},
		{http.MethodDelete, "/api/v1/users/u1", authz.ActionAdmin},
		{http.MethodGet, "/api/v1/audit", authz.ActionAdmin},
		{http.MethodPost, "/api/v1/escalations/run", authz.ActionAdmin},
		{http.MethodPost, "/api/v1/routes/debug/key", authz.ActionAdmin},
		// Reading the readiness report is a read; accepting its blockers, or
		// asserting that a backup exists, are claims the readiness gate then
		// trusts, so both are administrative.
		{http.MethodGet, "/api/v1/readiness", authz.ActionRead},
		{http.MethodPost, "/api/v1/readiness/acknowledge", authz.ActionAdmin},
		{http.MethodPost, "/api/v1/backups/report", authz.ActionAdmin},
		// An unknown write must not fall through to something a viewer can do.
		{http.MethodPost, "/api/v1/brand-new-thing", authz.ActionEdit},
	}
	for _, c := range cases {
		if got := requiredAction(c.method, c.path); got != c.want {
			t.Errorf("requiredAction(%s %s) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

// TestViewerCannotWrite verifies a viewer role is blocked from non-GET.
func TestViewerCannotWrite(t *testing.T) {
	srv := &Server{cfg: Config{APIKeys: map[string]string{"ro": string(authz.RoleViewer)}}, metrics: newMetrics()}

	rPost := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader([]byte("{}")))
	rPost.Header.Set("X-API-Key", "ro")
	wPost := httptest.NewRecorder()
	srv.handleAPI(wPost, rPost)
	if wPost.Code != http.StatusForbidden {
		t.Fatalf("viewer POST: status = %d, want 403", wPost.Code)
	}
}

// TestResponderCannotEditConfiguration is the role that most needs pinning: it
// may act on alerts but must not reconfigure the system.
func TestResponderCannotEditConfiguration(t *testing.T) {
	srv := &Server{cfg: Config{APIKeys: map[string]string{"rp": string(authz.RoleResponder)}}, metrics: newMetrics()}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/integrations", bytes.NewReader([]byte("{}")))
	r.Header.Set("X-API-Key", "rp")
	w := httptest.NewRecorder()
	srv.handleAPI(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("responder creating an integration: status = %d, want 403", w.Code)
	}
}

// TestEditorCannotTouchIdentity separates configuration rights from identity.
func TestEditorCannotTouchIdentity(t *testing.T) {
	srv := &Server{cfg: Config{APIKeys: map[string]string{"ed": string(authz.RoleEditor)}}, metrics: newMetrics()}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader([]byte("{}")))
	r.Header.Set("X-API-Key", "ed")
	w := httptest.NewRecorder()
	srv.handleAPI(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("editor creating a user: status = %d, want 403", w.Code)
	}

	rAudit := httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)
	rAudit.Header.Set("X-API-Key", "ed")
	wAudit := httptest.NewRecorder()
	srv.handleAPI(wAudit, rAudit)
	if wAudit.Code != http.StatusForbidden {
		t.Fatalf("editor reading the audit trail: status = %d, want 403", wAudit.Code)
	}
}

// TestUnauthenticatedManagementCallRejected is the end-to-end form of the
// backlog item: no credentials, no keys configured, no access.
func TestUnauthenticatedManagementCallRejected(t *testing.T) {
	srv := &Server{cfg: Config{}, metrics: newMetrics()}
	w := httptest.NewRecorder()
	srv.handleAPI(w, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET: status = %d, want 401", w.Code)
	}
}

// TestWithRecoverConvertsPanicTo500 verifies the recovery middleware keeps the
// server alive and returns 500 instead of crashing.
func TestWithRecoverConvertsPanicTo500(t *testing.T) {
	srv := &Server{metrics: newMetrics()}
	panicky := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	h := srv.withRecover(panicky)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	// Must not panic out of ServeHTTP.
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("recovered handler: status = %d, want 500", w.Code)
	}
}

// TestHandleLiveNeverTouchesDB confirms /live returns 200 with a nil store.
func TestHandleLiveNeverTouchesDB(t *testing.T) {
	srv := &Server{startTime: time.Now()} // no store, no metrics
	w := httptest.NewRecorder()
	srv.handleLive(w, httptest.NewRequest(http.MethodGet, "/live", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/live status = %d, want 200", w.Code)
	}
}
