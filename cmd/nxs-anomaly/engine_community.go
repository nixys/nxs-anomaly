package main

import (
	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/store"
)

// newEngine creates an Engine. This build ships no Kafka producer, so the
// analytics outbox is never drained and KAFKA_BROKERS is not consulted.
func newEngine(s store.PostgreSQLStore) *engine.Engine {
	return engine.New(s)
}
