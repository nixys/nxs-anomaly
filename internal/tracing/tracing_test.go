package tracing

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// withRecorder installs a real SDK provider writing to an in-memory exporter, so
// a test can assert on spans that were actually produced rather than on the
// no-op provider's silence.
func withRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	return rec
}

// Tracing is off by default, and off must mean "costs nothing and breaks
// nothing" — every call site starts spans unconditionally.
func TestDisabledByDefault(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	if Enabled() {
		t.Error("tracing reports enabled with no endpoint configured")
	}

	shutdown, err := Init(t.Context(), "test")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned a nil shutdown function")
	}
	if err := shutdown(t.Context()); err != nil {
		t.Errorf("no-op shutdown returned %v", err)
	}

	// Spans still start; they simply do not record.
	ctx, span := Start(t.Context(), "probe")
	span.End()
	if TraceID(ctx) != "" {
		t.Error("a disabled tracer produced a trace id")
	}
}

func TestEnabledReadsEitherEndpointVariable(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	if Enabled() {
		t.Fatal("enabled with both unset")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318")
	if !Enabled() {
		t.Error("the general endpoint variable was ignored")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318/v1/traces")
	if !Enabled() {
		t.Error("the traces-specific endpoint variable was ignored")
	}

	// Whitespace is not a configuration.
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "   ")
	if Enabled() {
		t.Error("blank endpoint counted as configured")
	}
}

func TestTraceIDAndSpanID(t *testing.T) {
	withRecorder(t)

	ctx, span := Start(t.Context(), "probe")
	defer span.End()

	traceID := TraceID(ctx)
	if len(traceID) != 32 {
		t.Errorf("trace id = %q, want 32 hex characters", traceID)
	}
	if spanID := SpanID(ctx); len(spanID) != 16 {
		t.Errorf("span id = %q, want 16 hex characters", spanID)
	}

	// The join key must be stable for the whole span, since it is written to
	// audit rows at arbitrary points inside it.
	if again := TraceID(ctx); again != traceID {
		t.Errorf("trace id changed within one span: %q then %q", traceID, again)
	}
}

func TestTraceIDEmptyWithoutSpan(t *testing.T) {
	if got := TraceID(context.Background()); got != "" {
		t.Errorf("TraceID on a bare context = %q, want empty", got)
	}
	if got := SpanID(context.Background()); got != "" {
		t.Errorf("SpanID on a bare context = %q, want empty", got)
	}
}

// A child span must inherit its parent's trace id — that inheritance is the
// entire mechanism by which ingest, escalation and delivery end up in one trace.
func TestChildSpanSharesTraceID(t *testing.T) {
	rec := withRecorder(t)

	ctx, parent := Start(t.Context(), "ingest.alert")
	childCtx, child := Start(ctx, "delivery.telegram")
	child.End()
	parent.End()

	if TraceID(ctx) != TraceID(childCtx) {
		t.Errorf("child trace id %q differs from parent %q", TraceID(childCtx), TraceID(ctx))
	}
	if SpanID(ctx) == SpanID(childCtx) {
		t.Error("child reused the parent's span id")
	}

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("recorded %d spans, want 2", len(spans))
	}
	// Ended() is in completion order: the child finished first.
	if spans[0].Parent().SpanID() != spans[1].SpanContext().SpanID() {
		t.Error("the child span is not parented to the outer span")
	}
}

// Propagators are installed even when tracing is off, so an incoming traceparent
// is not dropped on the floor by a service that happens not to export spans.
func TestInitInstallsPropagatorsWhenDisabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	if _, err := Init(t.Context(), "test"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	const (
		wantTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
		wantSpan  = "00f067aa0ba902b7"
	)
	header := http.Header{}
	header.Set("traceparent", "00-"+wantTrace+"-"+wantSpan+"-01")

	ctx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.HeaderCarrier(header))

	sc := trace.SpanContextFromContext(ctx)
	if sc.TraceID().String() != wantTrace {
		t.Errorf("extracted trace id = %q, want %q", sc.TraceID(), wantTrace)
	}
	if sc.SpanID().String() != wantSpan {
		t.Errorf("extracted span id = %q, want %q", sc.SpanID(), wantSpan)
	}
}

func TestSamplerFromEnv(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{name: "unset samples everything", arg: "", want: "AlwaysOnSampler"},
		{name: "ratio", arg: "0.25", want: "TraceIDRatioBased{0.25}"},
		{name: "unparseable falls back to always-on", arg: "banana", want: "AlwaysOnSampler"},
		{name: "out of range falls back to always-on", arg: "7", want: "AlwaysOnSampler"},
		{name: "negative falls back to always-on", arg: "-1", want: "AlwaysOnSampler"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", tc.arg)
			// ParentBased wraps the root sampler and names it in its description.
			if got := sampler().Description(); !strings.Contains(got, tc.want) {
				t.Errorf("sampler = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

func TestServiceNameOverride(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	if got := serviceName(); got != ServiceName {
		t.Errorf("default service name = %q, want %q", got, ServiceName)
	}
	t.Setenv("OTEL_SERVICE_NAME", "nxs-anomaly-staging")
	if got := serviceName(); got != "nxs-anomaly-staging" {
		t.Errorf("service name = %q, want the override", got)
	}
}

func TestRecordError(t *testing.T) {
	rec := withRecorder(t)

	_, ok := Start(t.Context(), "ok")
	RecordError(ok, nil) // must be a no-op
	ok.End()

	_, bad := Start(t.Context(), "bad")
	RecordError(bad, context.DeadlineExceeded)
	bad.End()

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("recorded %d spans, want 2", len(spans))
	}
	if len(spans[0].Events()) != 0 {
		t.Errorf("a nil error recorded %d events, want 0", len(spans[0].Events()))
	}
	if len(spans[1].Events()) == 0 {
		t.Error("the error was not recorded on the span")
	}
	if spans[1].Status().Description != context.DeadlineExceeded.Error() {
		t.Errorf("span status = %q, want the error text", spans[1].Status().Description)
	}
}

func TestShutdownToleratesNil(t *testing.T) {
	Shutdown(nil) // must not panic
}

// Every span carries the service it came from. The SDK's default resource
// declares the semantic-conventions schema of its own release, and this
// package's attributes declared another; Merge refused the pair, the fallback
// kept the default, and every trace arrived as "unknown_service" with no
// version.
func TestServiceResourceNamesTheService(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	res := serviceResource("v9.9.9")
	got := map[string]string{}
	for _, kv := range res.Attributes() {
		got[string(kv.Key)] = kv.Value.String()
	}
	if got["service.name"] != serviceName() || got["service.version"] != "v9.9.9" {
		t.Fatalf("service.name=%q service.version=%q, want %q and v9.9.9", got["service.name"], got["service.version"], serviceName())
	}
	if got["telemetry.sdk.language"] != "go" {
		t.Errorf("the SDK's own attributes are missing: %v", got)
	}
}
