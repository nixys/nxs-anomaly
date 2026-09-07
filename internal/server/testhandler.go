package server

import (
	"net/http"
	"time"

	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/store"
)

// NewTestHandler builds the same routed HTTP handler New serves, over the given
// store and engine, with none of the process-level machinery: no listener, no
// TLS, no admin bootstrap, no worker loop.
//
// It exists so that the integration suite can exercise ingest the way a source
// does — an actual POST through the actual router and middleware — instead of
// calling engine methods and hoping the HTTP layer agrees. Two of the bugs it
// covers (a repeat firing executing the escalation step a WAIT was counting
// down to, and parallel ingests overwriting each other's group state) are only
// reachable request-by-request against a real database, which is exactly what
// no in-package handler test can set up.
//
// No rate limiter is installed: a test that fires twenty alerts at one
// integration key would otherwise be measuring the limiter.
func NewTestHandler(s store.PostgreSQLStore, eng *engine.Engine) http.Handler {
	srv := &Server{
		eng:       eng,
		store:     s,
		metrics:   newMetrics(),
		startTime: time.Now(),
		cfg:       Config{AllowAnonymous: true},
	}
	return srv.handler()
}
