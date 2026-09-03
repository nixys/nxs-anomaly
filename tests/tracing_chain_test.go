package tests

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
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// The point of the trace work is a question nobody could answer from metrics:
// why did *this* notification take four minutes? Answering it needs the ingest
// and the delivery to be findable from one another, and they happen in different
// processes, minutes apart, joined only by a row in PostgreSQL.
//
// This test walks that join end to end against a real database: ingest under a
// span, run the worker, and require the delivery span to carry a link back to
// the trace the alert arrived in.

func withChainRecorder(t *testing.T) *tracetest.SpanRecorder {
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

func TestTraceLinksIngestToDelivery(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	rec := withChainRecorder(t)

	delivered := make(chan struct{}, 1)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case delivered <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	chain, err := eng.CreateEscalationChain(ctx, map[string]any{
		"name":  "trace-chain",
		"steps": []any{map[string]any{"kind": "TRIGGER_WEBHOOK", "webhook_url": stub.URL}},
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	integ, err := eng.CreateIntegration(ctx, map[string]any{
		"name": "trace-integ", "type": "webhook",
		"routes": []any{map[string]any{
			"is_default": true, "escalation_chain_id": utils.StrVal(chain, "id"),
		}},
	})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}

	// The ingest half: a span standing in for the HTTP request that received the
	// webhook. It ends here, exactly as the real one does.
	ingestCtx, ingestSpan := tracing.Start(ctx, "http.request")
	ingestTraceID := tracing.TraceID(ingestCtx)
	if ingestTraceID == "" {
		t.Fatal("no trace id on the ingesting span")
	}
	if _, err := eng.IngestAlert(ingestCtx, utils.StrVal(integ, "key"),
		map[string]any{"title": "traced alert", "severity": "critical"}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	ingestSpan.End()

	// The traceparent has to be on the stored group, or there is nothing for the
	// worker to pick up — this is the join itself.
	groups, err := st.ListCollection(ctx, "alert_groups")
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 alert group, got %d", len(groups))
	}
	storedTP := utils.StrVal(groups[0], "trace_parent")
	if storedTP == "" {
		t.Fatal("the alert group carries no trace_parent; ingest and delivery cannot be joined")
	}
	if link, ok := tracing.LinkFrom(storedTP); !ok || link.SpanContext.TraceID().String() != ingestTraceID {
		t.Fatalf("stored trace_parent %q does not point at the ingest trace %q", storedTP, ingestTraceID)
	}

	// The worker half, a separate trace of its own.
	for range 5 {
		if _, err := eng.RunWorkerCycle(ctx); err != nil {
			t.Fatalf("worker cycle: %v", err)
		}
		select {
		case <-delivered:
		default:
			continue
		}
		break
	}

	var deliverySpans []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if len(s.Name()) > 9 && s.Name()[:9] == "delivery." {
			deliverySpans = append(deliverySpans, s)
		}
	}
	if len(deliverySpans) == 0 {
		t.Fatal("no delivery span was recorded; the webhook step did not run")
	}

	d := deliverySpans[0]
	if d.SpanContext().TraceID().String() == ingestTraceID {
		t.Error("the delivery span was parented into the ingest trace; it must belong to the worker's own trace and only link back")
	}
	links := d.Links()
	if len(links) != 1 {
		t.Fatalf("delivery span has %d links, want exactly 1 back to the ingest", len(links))
	}
	if got := links[0].SpanContext.TraceID().String(); got != ingestTraceID {
		t.Errorf("delivery links to trace %q, want the ingest trace %q", got, ingestTraceID)
	}
}

// With tracing off nothing is stored and nothing is linked, and the alert flow
// has to be completely unaffected — that is the default configuration.
func TestNoTraceParentStoredWhenTracingIsOff(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	// No recorder installed: the global provider is the no-op one.
	integ, err := eng.CreateIntegration(ctx, map[string]any{"name": "untraced", "type": "webhook"})
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	if _, err := eng.IngestAlert(ctx, utils.StrVal(integ, "key"),
		map[string]any{"title": "untraced alert"}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	groups, err := st.ListCollection(ctx, "alert_groups")
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 alert group, got %d", len(groups))
	}
	if _, present := groups[0]["trace_parent"]; present {
		t.Errorf("trace_parent was written with tracing disabled: %#v", groups[0]["trace_parent"])
	}
}
