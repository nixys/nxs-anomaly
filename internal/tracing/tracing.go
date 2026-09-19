// Package tracing wires OpenTelemetry tracing into the alert pipeline.
//
// The service already exposes forty-odd Prometheus metrics and structured logs.
// Between them they answer "how many deliveries are slow" and "what happened to
// this process". Neither answers the question an operator actually asks after an
// incident: *why did this particular notification take four minutes?* That answer
// is a causal chain — ingest → grouping → escalation step → provider call — and a
// chain is what a trace is. Metrics aggregate it away; logs record the links but
// not the connections between them.
//
// Tracing is off unless an endpoint is configured. With no endpoint, Init
// installs nothing, every span becomes a no-op from the global provider, and the
// cost is an interface call on paths that were already doing work. Nobody has to
// run a collector to run nxs-anomaly.
package tracing

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// ServiceName is the value reported as service.name. Overridable via
// OTEL_SERVICE_NAME for a deployment that runs API and worker as one service
// name or wants an environment suffix.
const ServiceName = "nxs-anomaly"

// tracerName identifies the instrumentation scope, not the service.
const tracerName = "github.com/nixys/nxs-anomaly"

// Tracer returns the tracer every instrumented call site uses. Before Init runs
// — or when tracing is disabled — this is the global no-op provider's tracer,
// so call sites never need to check whether tracing is on.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// Enabled reports whether an exporter endpoint is configured. Call sites do not
// need this (spans are cheap no-ops when disabled); it exists for startup
// logging and for tests.
func Enabled() bool {
	return endpoint() != ""
}

func endpoint() string {
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
}

// Init installs the global tracer provider and W3C propagators, returning a
// shutdown function that flushes pending spans.
//
// When no endpoint is configured it returns a no-op shutdown and installs
// nothing but the propagators — those are installed regardless so that an
// incoming traceparent is still carried through to outgoing requests and logs,
// which costs nothing and keeps this service from breaking someone else's trace.
//
// An exporter that cannot be built is logged and swallowed rather than returned
// as a fatal error. Telemetry is not worth refusing to start over: a service that
// will not boot because its trace collector is missing has made an observability
// problem into an outage.
func Init(ctx context.Context, version string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	// The SDK reports its own troubles here. Without this they are silent, and a
	// collector that rejects every batch looks exactly like a service producing
	// no spans.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("otel_error", "err", err)
	}))

	ep := endpoint()
	if ep == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		slog.Error("tracing_exporter_failed",
			"err", err,
			"endpoint", ep,
			"effect", "continuing without tracing")
		return func(context.Context) error { return nil }, nil
	}

	res := serviceResource(version)

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler()),
	)
	otel.SetTracerProvider(provider)

	slog.Info("tracing_enabled",
		"endpoint", ep,
		"service_name", serviceName(),
		"sampler", os.Getenv("OTEL_TRACES_SAMPLER"))

	return provider.Shutdown, nil
}

func serviceName() string {
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		return v
	}
	return ServiceName
}

// sampler reads OTEL_TRACES_SAMPLER_ARG as a ratio, defaulting to sampling
// everything.
//
// Always-on is the right default here and not the usual reckless one: this is an
// alerting system, so its request volume is the rate at which somebody's
// infrastructure is breaking, not user traffic. Spans per second is measured in
// tens, and the traces worth having are the rare slow ones a ratio sampler is
// most likely to drop. ParentBased keeps a decision made upstream.
func sampler() sdktrace.Sampler {
	arg := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG"))
	if arg == "" {
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
	ratio, err := strconv.ParseFloat(arg, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		slog.Warn("tracing_bad_sampler_arg",
			"value", arg,
			"effect", "sampling every trace")
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// Start begins a span. It is a thin wrapper so call sites do not each import
// otel, and so the instrumentation scope is stated in exactly one place.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// TraceID returns the current trace ID as a hex string, or "" when there is no
// recording span. This is the join key between a trace and everything else: it
// goes into audit events and log lines, so an audit row found six weeks later
// still points at the trace that produced it.
func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

// SpanID returns the current span ID as a hex string, or "".
func SpanID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasSpanID() {
		return ""
	}
	return sc.SpanID().String()
}

// RecordError marks the span failed and attaches the error. A nil error is
// ignored, so call sites can pass a result straight through.
func RecordError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// Attributes used across more than one call site. Names follow OTel convention
// (namespaced, snake_case) so they do not collide with semantic conventions.
func AlertGroupID(id string) attribute.KeyValue { return attribute.String("nxs.alert_group_id", id) }
func NotificationID(id string) attribute.KeyValue {
	return attribute.String("nxs.notification_id", id)
}
func IntegrationID(id string) attribute.KeyValue { return attribute.String("nxs.integration_id", id) }
func Channel(c string) attribute.KeyValue        { return attribute.String("nxs.channel", c) }
func Source(s string) attribute.KeyValue         { return attribute.String("nxs.source", s) }
func Count(n int) attribute.KeyValue             { return attribute.Int("nxs.count", n) }
func Outcome(s string) attribute.KeyValue        { return attribute.String("nxs.outcome", s) }

// ShutdownTimeout bounds the flush at exit. Long enough for a batch to reach a
// healthy collector, short enough not to hold up a rolling restart when the
// collector is the thing that is down.
const ShutdownTimeout = 5 * time.Second

// Shutdown flushes with the standard timeout and logs rather than propagating —
// a failed flush at exit is not something a caller can act on.
func Shutdown(fn func(context.Context) error) {
	if fn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := fn(ctx); err != nil {
		slog.Warn("tracing_shutdown_failed", "err", err)
	}
}

// ── Carrying a trace across a database row ──────────────────────────────────
//
// An alert's life is not one call chain. A webhook lands on an API pod and
// returns; minutes later a worker pod picks the group off a table and delivers
// it. Nothing calls anything — the two halves are joined by a row, so no amount
// of context.Context plumbing connects them.
//
// The fix is the same one used for message queues: serialise the span context
// into the row on the way in (Traceparent), and on the way out attach it to the
// consumer's span as a *link* rather than a parent. A link says "this was caused
// by that" without pretending the delivery happened inside the HTTP request,
// which would put a span lasting four minutes inside a request that returned in
// twenty milliseconds and make the worker's own trace unreadable.

// Traceparent renders ctx's span context as a W3C traceparent header value, or
// "" when there is no recording span. Store the result next to the work it
// describes; pass it back to LinkFrom later.
func Traceparent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// LinkFrom turns a stored traceparent back into a span link. The second result
// is false when the value is absent or unparseable, in which case there is
// simply nothing to link to — a group ingested before tracing was switched on is
// the ordinary case, not an error.
func LinkFrom(traceparent string) (trace.Link, bool) {
	if traceparent == "" {
		return trace.Link{}, false
	}
	ctx := otel.GetTextMapPropagator().Extract(
		context.Background(),
		propagation.MapCarrier{"traceparent": traceparent},
	)
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return trace.Link{}, false
	}
	return trace.Link{SpanContext: sc}, true
}

// StartLinked starts a span that is a child of ctx and additionally links to the
// trace identified by traceparent. An empty or invalid traceparent just yields
// an ordinary span, so call sites need no branch.
func StartLinked(ctx context.Context, name, traceparent string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithAttributes(attrs...)}
	if link, ok := LinkFrom(traceparent); ok {
		opts = append(opts, trace.WithLinks(link))
	}
	return Tracer().Start(ctx, name, opts...)
}

// serviceResource is the SDK's default resource plus this service's name and
// version.
//
// The attributes carry no schema URL of their own. The default resource
// declares the semantic-conventions schema of the SDK's release (v1.43.0 with
// SDK 1.46), this package's semconv import is v1.26.0, and Merge refuses two
// different schema URLs — so the fallback below used to run on every start and
// every span went out as "unknown_service" without a version. A schemaless
// resource merges into whichever schema the SDK declares.
func serviceResource(version string) *resource.Resource {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(serviceName()),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		// A schema-URL conflict, not a reason to lose tracing entirely.
		slog.Warn("tracing_resource_merge_failed", "err", err)
		res = resource.Default()
	}
	return res
}
