package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/nixys/nxs-anomaly/internal/engine"
)

func TestReadJSON(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":1}`))
		w := httptest.NewRecorder()
		m, ok := readJSON(w, r)
		if !ok || m["a"] != float64(1) {
			t.Fatalf("ok=%v m=%v", ok, m)
		}
	})
	t.Run("empty body yields empty map", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
		w := httptest.NewRecorder()
		m, ok := readJSON(w, r)
		if !ok || len(m) != 0 {
			t.Fatalf("ok=%v m=%v", ok, m)
		}
	})
	t.Run("invalid JSON → 400", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{bad`))
		w := httptest.NewRecorder()
		_, ok := readJSON(w, r)
		if ok || w.Code != http.StatusBadRequest {
			t.Fatalf("ok=%v code=%d", ok, w.Code)
		}
	})
	t.Run("oversized body → 400", func(t *testing.T) {
		big := strings.Repeat("a", maxRequestBody+10)
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`"`+big+`"`))
		w := httptest.NewRecorder()
		_, ok := readJSON(w, r)
		if ok || w.Code != http.StatusBadRequest {
			t.Fatalf("ok=%v code=%d", ok, w.Code)
		}
	})
}

func TestWriteEngineErrorMapsStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", fmt.Errorf("user x: %w", engine.ErrNotFound), http.StatusNotFound},
		{"validation", fmt.Errorf("bad: %w", engine.ErrValidation), http.StatusBadRequest},
		{"no rows fallback", fmt.Errorf("sql: no rows in result set"), http.StatusNotFound},
		{"internal", fmt.Errorf("boom"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeEngineError(w, c.err)
			if w.Code != c.want {
				t.Fatalf("code = %d, want %d", w.Code, c.want)
			}
			// Internal errors must not leak the underlying message.
			if c.want == http.StatusInternalServerError && strings.Contains(w.Body.String(), "boom") {
				t.Errorf("internal error leaked details: %s", w.Body.String())
			}
		})
	}
}

// A client that hung up is not an engine failure: no ERROR line, no 500. A real
// timeout still is.
func TestWriteEngineErrorClientGone(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	w := httptest.NewRecorder()
	writeEngineError(w, fmt.Errorf("list groups: %w", context.Canceled))
	if w.Code != statusClientClosedRequest {
		t.Fatalf("code = %d, want %d", w.Code, statusClientClosedRequest)
	}
	if strings.Contains(buf.String(), "engine error") {
		t.Fatalf("a canceled request was logged as an engine error: %s", buf.String())
	}

	w = httptest.NewRecorder()
	writeEngineError(w, fmt.Errorf("list groups: %w", context.DeadlineExceeded))
	if w.Code != http.StatusInternalServerError || !strings.Contains(buf.String(), "engine error") {
		t.Fatalf("a timeout must stay an engine error: code=%d log=%s", w.Code, buf.String())
	}
}

func TestWriteIngestErrorMapsStatus(t *testing.T) {
	w := httptest.NewRecorder()
	writeIngestError(w, fmt.Errorf("bad: %w", engine.ErrValidation), "webhook", "k1")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
	w = httptest.NewRecorder()
	writeIngestError(w, fmt.Errorf("internal boom"), "webhook", "k1")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "boom") {
		t.Fatalf("internal error not generic: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestWithRequestIDGeneratesAndPropagates(t *testing.T) {
	srv := &Server{}
	var seen string
	h := srv.withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = w.Header().Get("X-Request-ID")
	}))

	// Generated when absent.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if seen == "" || w.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected generated request id")
	}

	// Propagated when present.
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "fixed-123")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if seen != "fixed-123" || w.Header().Get("X-Request-ID") != "fixed-123" {
		t.Fatalf("request id not propagated: %q", seen)
	}
}

func TestClassifyHandler(t *testing.T) {
	cases := map[string]string{
		"/api/v1/users":           "api",
		"/api/internal/v1/plugin": "compat",
		"/integrations/v1/abc":    "webhook",
		"/v2/alert/xyz":           "webhook",
		"/health":                 "other",
	}
	for path, want := range cases {
		if got := classifyHandler(path); got != want {
			t.Errorf("classifyHandler(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestPathHelpers(t *testing.T) {
	if d := pathDepth("/api/v1/users/abc", "/api/v1/users/"); d != 1 {
		t.Errorf("pathDepth = %d, want 1", d)
	}
	if d := pathDepth("/api/v1/users/", "/api/v1/users/"); d != 0 {
		t.Errorf("pathDepth empty = %d, want 0", d)
	}
	if s := lastSegment("/a/b/c/"); s != "c" {
		t.Errorf("lastSegment = %q, want c", s)
	}
	if s := segment("/a/b/c", 1); s != "b" {
		t.Errorf("segment 1 = %q, want b", s)
	}
	if s := segment("/a/b/c", 9); s != "" {
		t.Errorf("segment out of range = %q, want empty", s)
	}
}

func TestPageParams(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?limit=50&offset=10&status=open&severity=high", nil)
	p := pageParams(r)
	if p["limit"] != 50 || p["offset"] != 10 {
		t.Errorf("limit/offset = %v/%v", p["limit"], p["offset"])
	}
	if p["status"] != "open" || p["severity"] != "high" {
		t.Errorf("filters not parsed: %v", p)
	}
	// Defaults + bad values.
	r = httptest.NewRequest(http.MethodGet, "/?limit=notanint", nil)
	p = pageParams(r)
	if p["limit"] != 100 || p["offset"] != 0 {
		t.Errorf("defaults wrong: %v", p)
	}
	if _, ok := p["status"]; ok {
		t.Errorf("absent filter should not be set")
	}
}

func TestExtractGroupIDs(t *testing.T) {
	ids := extractGroupIDs(map[string]any{"group_ids": []any{"a", "b", 3, "c"}})
	if len(ids) != 3 || ids[0] != "a" || ids[2] != "c" {
		t.Errorf("extractGroupIDs = %v, want [a b c]", ids)
	}
	if got := extractGroupIDs(map[string]any{}); len(got) != 0 {
		t.Errorf("missing group_ids should yield empty, got %v", got)
	}
}

func TestStatusRecorderCapturesCode(t *testing.T) {
	w := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	rec.WriteHeader(http.StatusTeapot)
	rec.WriteHeader(http.StatusOK) // second call ignored
	if rec.status != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.status)
	}

	// Write without explicit WriteHeader defaults to 200.
	w2 := httptest.NewRecorder()
	rec2 := &statusRecorder{ResponseWriter: w2}
	_, _ = rec2.Write([]byte("x"))
	if rec2.status != http.StatusOK {
		t.Errorf("implicit status = %d, want 200", rec2.status)
	}
}

// TestSecurityHeadersOnEveryResponse: the API is reachable without the
// frontend's nginx in front of it — its own Service, or an ingress routed
// straight at it — and on that path it used to send no security header at all.
// Asserted on an error response too, since those are the ones whose body most
// often echoes attacker-controlled input back.
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	srv := &Server{}
	for _, tc := range []struct {
		name   string
		handle http.HandlerFunc
	}{
		{"success", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }},
		{"error", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.withRequestID(tc.handle).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

// Responder actions are their own latency category, the "p95 acknowledge"
// SLI; list reads under the same prefix stay in "api".
func TestObservabilityRespondCategory(t *testing.T) {
	srv, _ := newTestServer()
	handler := srv.withObservability(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/alert-groups/g-1/acknowledge", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/mobile/alert-groups/g-1/resolve", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/mobile/alert-groups/", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/alert-groups/g-1", nil),
	} {
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	count := func(handler string) float64 {
		var m dto.Metric
		if err := srv.metrics.requestsTotal.WithLabelValues(handler, "200").Write(&m); err != nil {
			t.Fatal(err)
		}
		return m.GetCounter().GetValue()
	}
	if got := count("respond"); got != 2 {
		t.Errorf("respond requests = %v, want 2", got)
	}
	if got := count("api"); got != 2 {
		t.Errorf("api requests = %v, want 2 (reads stay in api)", got)
	}
}

// While draining after SIGTERM the process still serves, but /health fails so
// the readiness probe takes the pod out of the Service before the listener shuts.
func TestHealthReportsDraining(t *testing.T) {
	srv, _ := newTestServer()
	w := httptest.NewRecorder()
	srv.handleHealth(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthy: status %d, want 200 (%s)", w.Code, w.Body.String())
	}
	srv.draining.Store(true)
	w = httptest.NewRecorder()
	srv.handleHealth(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"draining"`) {
		t.Fatalf("draining: status %d body %s, want 503 draining", w.Code, w.Body.String())
	}
}
