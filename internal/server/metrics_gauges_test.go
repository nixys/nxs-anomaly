package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// metricValue pulls one unlabelled sample out of an exposition body.
func metricValue(t *testing.T, body, name string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+" ") {
			return strings.TrimSpace(strings.TrimPrefix(line, name+" "))
		}
	}
	t.Fatalf("%s not found in /metrics", name)
	return ""
}

// TestNoSchedulerReplicaStillReportsDBUp is the regression that motivated
// splitting updateProcessGauges out.
//
// An API replica runs with --no-scheduler, so no worker cycle ever executes in
// it. The gauge refresh used to live only inside that cycle, which left
// nxs_anomaly_db_up at its 0 zero-value for the life of the process while the
// database was perfectly reachable — and the DatabaseUnavailable alert reads
// that gauge from every process, API replicas included. It fired on a healthy
// deployment and pointed at the database.
func TestNoSchedulerReplicaStillReportsDBUp(t *testing.T) {
	srv, st := newTestServer()
	srv.cfg.PollInterval = 10 * time.Millisecond

	if got := metricValue(t, scrapeMetrics(t, srv.metrics), "nxs_anomaly_db_up"); got != "0" {
		t.Fatalf("db_up before any refresh = %s, want the 0 zero-value", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.runGaugeRefreshLoop(ctx)

	waitForMetric(t, srv, "nxs_anomaly_db_up", "1")

	// And it must follow the database back down, or the alert can never fire.
	st.SetPingErr(errors.New("connection refused"))
	waitForMetric(t, srv, "nxs_anomaly_db_up", "0")
}

// TestGaugeRefreshLoopStopsWithItsContext: the loop is started per process and
// must not outlive shutdown.
func TestGaugeRefreshLoopStopsWithItsContext(t *testing.T) {
	srv, _ := newTestServer()
	srv.cfg.PollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		srv.runGaugeRefreshLoop(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gauge refresh loop ignored its cancelled context")
	}
}

func waitForMetric(t *testing.T, srv *Server, name, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = metricValue(t, scrapeMetrics(t, srv.metrics), name)
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s = %s, want %s", name, got, want)
}
