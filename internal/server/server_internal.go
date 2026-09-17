package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/tracing"
)

func readJSON(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	m, _, ok := readJSONWithRaw(w, r)
	return m, ok
}

// readJSONWithRaw also returns the body bytes exactly as received, for checks
// that are defined over them — a request signature is computed by the sender
// over what it sent, not over any re-encoding of it.
func readJSONWithRaw(w http.ResponseWriter, r *http.Request) (map[string]any, []byte, bool) {
	body := http.MaxBytesReader(w, r.Body, maxRequestBody)
	data, err := io.ReadAll(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "request body too large"})
		return nil, nil, false
	}
	if len(data) == 0 {
		return map[string]any{}, data, true
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
		return nil, nil, false
	}
	return m, data, true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	data, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if _, err := w.Write(data); err != nil {
		slog.Debug("write_json_failed", "err", err)
	}
}

func writeEngineError(w http.ResponseWriter, err error, keyVals ...any) {
	if isNotFound(err) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	if isValidation(err) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if errors.Is(err, engine.ErrForbidden) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
		return
	}
	if errors.Is(err, engine.ErrConflict) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	args := append([]any{"err", err, "request_id", w.Header().Get("X-Request-ID")}, keyVals...)
	slog.Error("engine error", args...)
	// The full error is logged above; return a generic message so internal
	// details (SQL text, internal state) never leak to API clients.
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
}

func writeIngestError(w http.ResponseWriter, err error, source, key string) {
	if isNotFound(err) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	if isValidation(err) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if errors.Is(err, engine.ErrForbidden) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
		return
	}
	slog.Error("ingest_failed", "source", source, "key", key, "error", err, "request_id", w.Header().Get("X-Request-ID"))
	// A database that cannot serve right now is not the sender's fault, and the
	// status code is what decides whether the alert is retried or dropped.
	// Alertmanager, Grafana and webhook senders generally back off and retry a
	// 503 while treating a 500 as "this request is broken" — so answering 500 to
	// a rolling restart turns a blip into lost alerts. Measured during a
	// PostgreSQL restart under live ingest: 11 of 60 alerts came back 500, each
	// one an alert the sender had no reason to send again.
	//
	// Retry-After is deliberately short. The store reconnects on its own and the
	// pod stays up — the outage observed lasted about half a minute — so the
	// sender should come back soon rather than park the alert for minutes.
	if store.IsUnavailable(err) {
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database unavailable, retry"})
		return
	}
	// Logged above; return a generic message to avoid leaking internal details.
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
}

func isNotFound(err error) bool {
	if errors.Is(err, engine.ErrNotFound) {
		return true
	}
	// Fallback: store-level "no rows" from pgx (not an engine error).
	return strings.Contains(err.Error(), "no rows")
}

func isValidation(err error) bool {
	return errors.Is(err, engine.ErrValidation)
}

// clientIP returns the address of the client as far as trusted proxies vouch
// for it.
//
// X-Forwarded-For is walked from the right. Each proxy appends the address it
// received the request from, so the entries a trusted proxy added are the
// right-hand ones, while everything to their left may have been written by the
// client itself. The nearest address that is not a trusted proxy is the client.
// Taking the leftmost entry let any client choose its own address — and put
// every user behind one proxy into one sign-in rate bucket when it was not
// trusted at all.
func (srv *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !srv.isTrustedProxy(host) {
		return host
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(header, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !srv.isTrustedProxy(hops[i]) {
			return hops[i]
		}
	}
	if len(hops) > 0 {
		// Every hop is a trusted proxy: the request started inside the trusted
		// network, and the first hop is as close to its origin as we can see.
		return hops[0]
	}
	return host
}

func (srv *Server) isTrustedProxy(ipStr string) bool {
	if len(srv.trustedProxies) == 0 {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, cidr := range srv.trustedProxies {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// withRequestID propagates or generates an X-Request-ID for every request (O-3).
func (srv *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = fmt.Sprintf("%016x%08x", time.Now().UnixNano(), srv.reqIDCounter.Add(1))
		}
		w.Header().Set("X-Request-ID", rid)
		// The API answers JSON and nothing else. nosniff stops a browser from
		// deciding otherwise about a response whose body an attacker influenced
		// — a label echoed back in an error, say. The frontend's nginx sets this
		// on what it proxies, but the API is also reachable directly (its own
		// Service, an ingress routed straight at it), and that path had no
		// security headers at all.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Carried in the context as well as the header, so audit events written
		// downstream can be correlated with the access log line for the same
		// request instead of being matched up by timestamp.
		next.ServeHTTP(w, r.WithContext(engine.NewRequestIDContext(r.Context(), rid)))
	})
}

// statusRecorder captures the response status code for metrics and access logging.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.wroteHeader {
		sr.status = code
		sr.wroteHeader = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wroteHeader {
		sr.status = http.StatusOK
		sr.wroteHeader = true
	}
	return sr.ResponseWriter.Write(b)
}

// withObservability records request duration + count (by handler category and
// status code) and emits a structured access log line.
func (srv *Server) withObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		dur := time.Since(t0)
		category := classifyHandler(r.URL.Path)
		srv.metrics.requestDuration.WithLabelValues(category).Observe(dur.Seconds())
		srv.metrics.requestsTotal.WithLabelValues(category, strconv.Itoa(rec.status)).Inc()
		// Access log at Debug to avoid per-request noise at Info; health/metrics skipped.
		if r.URL.Path != "/live" && r.URL.Path != "/health" && r.URL.Path != "/ready" && r.URL.Path != "/metrics" {
			slog.Debug("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", dur.Milliseconds(),
				"request_id", w.Header().Get("X-Request-ID"),
				// Present only when tracing is on. It is what turns this line from
				// the end of the story into a pointer to the rest of it.
				"trace_id", tracing.TraceID(r.Context()),
				"remote", srv.clientIP(r),
			)
		}
	})
}

// withTracing starts a server span for every request, continuing the caller's
// trace when it sent a traceparent.
//
// It sits outside withObservability so the span covers the whole request
// including the metrics and logging work, and so the access log line can read the
// trace id back out of the context.
//
// The health and metrics endpoints are skipped. They are polled every few seconds
// by kubelet and Prometheus forever, and a trace per liveness probe is pure noise
// that would crowd out the traces worth keeping.
func (srv *Server) withTracing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live", "/health", "/ready", "/metrics":
			next.ServeHTTP(w, r)
			return
		}

		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		// Named by route category rather than raw path: the path carries entity
		// ids, which would give every alert group its own span name and make the
		// trace backend's grouping useless. The full path is an attribute.
		ctx, span := tracing.Start(ctx, r.Method+" "+classifyHandler(r.URL.Path),
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.URLPath(r.URL.Path),
			semconv.ClientAddress(srv.clientIP(r)),
		)
		defer span.End()

		// Hand the trace id back to the caller. Someone reporting "my webhook was
		// slow" can then quote the id instead of the time they think it happened.
		if id := tracing.TraceID(ctx); id != "" {
			w.Header().Set("X-Trace-ID", id)
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		span.SetAttributes(semconv.HTTPResponseStatusCode(rec.status))
		if rec.status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(rec.status))
		}
	})
}

// withRecover converts a panic in any downstream handler into a 500 response and
// keeps the process alive — a single malformed request must not crash the server.
func (srv *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				srv.metrics.panicsRecovered.Inc()
				slog.Error("http_panic_recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", w.Header().Get("X-Request-ID"),
					"panic", fmt.Sprintf("%v", rec),
					"stack", string(debug.Stack()),
				)
				// Best-effort 500; if the handler already wrote a header this is a no-op.
				defer func() { _ = recover() }()
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func classifyHandler(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/v1/"):
		return "api"
	case strings.HasPrefix(path, "/api/internal/v1"):
		return "compat"
	case strings.HasPrefix(path, "/integrations/v1/"), strings.HasPrefix(path, "/v2/alert/"):
		return "webhook"
	default:
		return "other"
	}
}

// pathDepth returns the number of path segments after prefix.
// e.g. "/api/v1/users/abc" with prefix "/api/v1/users/" → 1
func pathDepth(path, prefix string) int {
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return 0
	}
	parts := strings.Split(rest, "/")
	count := 0
	for _, p := range parts {
		if p != "" {
			count++
		}
	}
	return count
}

// lastSegment returns the last non-empty path segment.
func lastSegment(path string) string {
	path = strings.TrimSuffix(path, "/")
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return path
	}
	return path[idx+1:]
}

// segment returns the n-th (0-indexed) segment of a slash-split path.
func segment(path string, n int) string {
	parts := strings.Split(path, "/")
	idx := 0
	for _, p := range parts {
		if p == "" {
			continue
		}
		if idx == n {
			return p
		}
		idx++
	}
	return ""
}

func pageParams(r *http.Request) map[string]any {
	q := r.URL.Query()
	params := map[string]any{
		"limit":  parseIntParam(q.Get("limit"), 100),
		"offset": parseIntParam(q.Get("offset"), 0),
	}
	for _, key := range []string{"status", "severity", "integration_id", "channel", "user_id", "alert_group_id", "route_id",
		// A comma-separated set of ids, for a page that needs one fact about
		// each of several rows and would otherwise ask once per row.
		"ids",
		// Ordering, not filtering: the engine validates the column against the
		// collection before it reaches SQL.
		"sort", "order"} {
		if v := q.Get(key); v != "" {
			params[key] = v
		}
	}
	return params
}

func parseIntParam(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// writeResult writes an engine map result or error.
// Accepts the exact (map[string]any, error) tuple from engine methods.
func writeResult(w http.ResponseWriter, status int, v map[string]any, err error) {
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, status, v)
}

// writeResultSlice writes a slice result or error (needed because []map[string]any is not []any).
func writeResultSlice(w http.ResponseWriter, status int, items []map[string]any, err error) {
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, status, items)
}

// extractGroupIDs reads the group_ids array from a bulk-action request body.
func extractGroupIDs(body map[string]any) []string {
	raw, _ := body["group_ids"].([]any)
	ids := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			ids = append(ids, s)
		}
	}
	return ids
}
