package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/nixys/nxs-anomaly/internal/tracing"
)

func withSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevProvider := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
		_ = provider.Shutdown(context.Background())
	})
	return rec
}

// The trace id has to reach the handler's context, because that is how it gets
// onto audit rows and log lines written deep inside the request.
func TestWithTracingPutsTraceIDInContext(t *testing.T) {
	withSpanRecorder(t)
	srv, _ := newTestServer()

	var seen string
	handler := srv.withTracing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = tracing.TraceID(r.Context())
	}))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/users", nil))

	if seen == "" {
		t.Fatal("handler saw no trace id")
	}
	if got := w.Header().Get("X-Trace-ID"); got != seen {
		t.Errorf("X-Trace-ID header = %q, want the handler's trace id %q", got, seen)
	}
}

// A caller that already has a trace — Alertmanager behind a traced proxy, or a
// retry from another service — must have its trace continued, not replaced.
func TestWithTracingContinuesAnIncomingTrace(t *testing.T) {
	withSpanRecorder(t)
	srv, _ := newTestServer()

	const upstream = "4bf92f3577b34da6a3ce929d0e0e4736"

	var seen string
	handler := srv.withTracing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = tracing.TraceID(r.Context())
	}))

	r := httptest.NewRequest(http.MethodPost, "/integrations/v1/abc", nil)
	r.Header.Set("traceparent", "00-"+upstream+"-00f067aa0ba902b7-01")
	handler.ServeHTTP(httptest.NewRecorder(), r)

	if seen != upstream {
		t.Errorf("trace id = %q, want the caller's %q — a new trace was started instead of continuing theirs", seen, upstream)
	}
}

// Health and metrics endpoints are polled forever by kubelet and Prometheus.
// A span each would drown the traces that matter.
func TestWithTracingSkipsProbes(t *testing.T) {
	rec := withSpanRecorder(t)
	srv, _ := newTestServer()

	handler := srv.withTracing(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for _, path := range []string{"/live", "/health", "/ready", "/metrics"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if n := len(rec.Ended()); n != 0 {
		t.Errorf("probe requests produced %d spans, want 0", n)
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))
	if n := len(rec.Ended()); n != 1 {
		t.Errorf("a real request produced %d spans, want 1", n)
	}
}

// Span names must not carry entity ids, or every alert group gets its own span
// name and the trace backend cannot group anything.
func TestWithTracingSpanNameIsRouteCategory(t *testing.T) {
	rec := withSpanRecorder(t)
	srv, _ := newTestServer()

	handler := srv.withTracing(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/api/v1/alert_groups/ag_0123456789ab", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if got := spans[0].Name(); got != "GET api" {
		t.Errorf("span name = %q, want %q", got, "GET api")
	}
	// The full path is still available, as an attribute.
	var sawPath bool
	for _, attr := range spans[0].Attributes() {
		if attr.Key == "url.path" && attr.Value.AsString() == "/api/v1/alert_groups/ag_0123456789ab" {
			sawPath = true
		}
	}
	if !sawPath {
		t.Error("the request path was not recorded as an attribute")
	}
}

// A 5xx must mark the span failed, or a trace search for errors misses exactly
// the requests worth looking at.
func TestWithTracingMarksServerErrors(t *testing.T) {
	rec := withSpanRecorder(t)
	srv, _ := newTestServer()

	handler := srv.withTracing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Status().Code.String() != "Error" {
		t.Errorf("span status = %q on a 500, want Error", spans[0].Status().Code)
	}

	// A 4xx is the caller's problem, not this service's, and must not be counted
	// as a server error.
	handler4xx := srv.withTracing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	handler4xx.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))

	spans = rec.Ended()
	if spans[1].Status().Code.String() == "Error" {
		t.Error("a 400 was recorded as a server-side error")
	}
}
