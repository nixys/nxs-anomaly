package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/model"
)

// BenchmarkProcessDeliveries measures one delivery stage over a backlog of
// "log" notifications (no real network), isolating the bounded-concurrency loop
// plus the load/diff/save round-trip through the in-memory store.
func BenchmarkProcessDeliveries(b *testing.B) {
	seed := func() *memStore {
		ms := newMemStore()
		for i := 0; i < 200; i++ {
			id := fmt.Sprintf("n%05d", i)
			ms.seed("notifications", makeNotification(id, model.NotificationDeliveryScheduled, "log", "", nil))
		}
		return ms
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		e := deliveryEngine(seed())
		b.StartTimer()
		if _, err := e.ProcessNotificationDeliveries(context.Background()); err != nil {
			b.Fatalf("deliveries: %v", err)
		}
	}
}

// BenchmarkRunWorkerCycleIdle measures the fixed per-cycle overhead with no due
// work — the cost every poll tick pays regardless of load.
func BenchmarkRunWorkerCycleIdle(b *testing.B) {
	e := deliveryEngine(newMemStore())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.RunWorkerCycle(context.Background()); err != nil {
			b.Fatalf("cycle: %v", err)
		}
	}
}
