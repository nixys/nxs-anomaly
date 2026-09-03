package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// The carrier is what joins the two halves of an alert's life: an API pod
// ingests and returns, a worker pod delivers minutes later, and nothing calls
// anything in between. These tests cover the round trip through the string that
// gets stored on the row.

func TestTraceparentRoundTrip(t *testing.T) {
	withRecorder(t)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	ctx, span := Start(t.Context(), "ingest.alert")
	defer span.End()

	tp := Traceparent(ctx)
	if tp == "" {
		t.Fatal("Traceparent returned empty for a recording span")
	}

	link, ok := LinkFrom(tp)
	if !ok {
		t.Fatalf("LinkFrom(%q) reported no link", tp)
	}
	if got := link.SpanContext.TraceID().String(); got != TraceID(ctx) {
		t.Errorf("link trace id = %q, want %q", got, TraceID(ctx))
	}
	if got := link.SpanContext.SpanID().String(); got != SpanID(ctx) {
		t.Errorf("link span id = %q, want %q", got, SpanID(ctx))
	}
}

// With tracing off there is nothing to carry, and every downstream call has to
// treat that as ordinary rather than as an error — it is the default state.
func TestTraceparentEmptyWithoutSpan(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if got := Traceparent(context.Background()); got != "" {
		t.Errorf("Traceparent on a bare context = %q, want empty", got)
	}
}

func TestLinkFromRejectsGarbage(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	for _, tp := range []string{
		"",
		"nonsense",
		"00-tooshort-00f067aa0ba902b7-01",
		// ff is the one version W3C declares invalid; a merely unknown future
		// version (say 99) is accepted on purpose, so it is not listed here.
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01", // zero trace id
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", // zero span id
	} {
		if _, ok := LinkFrom(tp); ok {
			t.Errorf("LinkFrom(%q) accepted an unusable traceparent", tp)
		}
	}
}

// StartLinked must produce a span in the *caller's* trace, not the linked one.
// Getting this backwards would pull every delivery out of its worker cycle and
// scatter the worker's trace across one trace per alert.
func TestStartLinkedStaysInTheCallersTrace(t *testing.T) {
	rec := withRecorder(t)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// The "ingest" trace, which has ended by the time delivery happens.
	ingestCtx, ingestSpan := Start(t.Context(), "ingest.alert")
	stored := Traceparent(ingestCtx)
	ingestTraceID := TraceID(ingestCtx)
	ingestSpan.End()

	// A separate worker-cycle trace, minutes later.
	cycleCtx, cycleSpan := Start(t.Context(), "worker.cycle")
	deliveryCtx, deliverySpan := StartLinked(cycleCtx, "delivery.telegram", stored)
	deliverySpan.End()
	cycleSpan.End()

	if TraceID(deliveryCtx) != TraceID(cycleCtx) {
		t.Errorf("delivery is in trace %q, want the worker cycle's %q",
			TraceID(deliveryCtx), TraceID(cycleCtx))
	}
	if TraceID(deliveryCtx) == ingestTraceID {
		t.Error("delivery was parented into the ingest trace; it must only link to it")
	}

	var delivery trace.SpanContext
	var links []sdktrace.Link
	for _, s := range rec.Ended() {
		if s.Name() == "delivery.telegram" {
			delivery = s.SpanContext()
			links = s.Links()
		}
	}
	if !delivery.IsValid() {
		t.Fatal("delivery span was not recorded")
	}
	if len(links) != 1 {
		t.Fatalf("delivery has %d links, want 1", len(links))
	}
	if got := links[0].SpanContext.TraceID().String(); got != ingestTraceID {
		t.Errorf("link points at trace %q, want the ingest trace %q", got, ingestTraceID)
	}
}

// A group ingested before tracing was switched on carries no traceparent. That
// must produce an ordinary span, not a missing one and not an error.
func TestStartLinkedWithoutATraceparent(t *testing.T) {
	rec := withRecorder(t)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	ctx, span := StartLinked(t.Context(), "delivery.email", "")
	span.End()

	if TraceID(ctx) == "" {
		t.Error("StartLinked with no traceparent produced no span")
	}
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if n := len(spans[0].Links()); n != 0 {
		t.Errorf("span has %d links, want 0", n)
	}
}
