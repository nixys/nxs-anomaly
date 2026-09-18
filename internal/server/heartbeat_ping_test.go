package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHeartbeatPinger(t *testing.T) {
	hits := make(chan struct{}, 10)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
	}))
	defer ts.Close()

	expectHits := func(t *testing.T, want int) {
		t.Helper()
		got := 0
		deadline := time.After(500 * time.Millisecond)
		for {
			select {
			case <-hits:
				got++
			case <-deadline:
				if got != want {
					t.Fatalf("pings = %d, want %d", got, want)
				}
				return
			}
		}
	}

	t.Run("successful cycle pings, next one within the interval does not", func(t *testing.T) {
		p := newHeartbeatPinger(ts.URL)
		p.cycleCompleted(nil)
		expectHits(t, 1)
		p.cycleCompleted(nil)
		expectHits(t, 0)
	})

	t.Run("failed cycle does not ping", func(t *testing.T) {
		p := newHeartbeatPinger(ts.URL)
		p.cycleCompleted(errors.New("database unreachable"))
		expectHits(t, 0)
		// A failure does not consume the interval: the next success still pings.
		p.cycleCompleted(nil)
		expectHits(t, 1)
	})

	t.Run("pings again once the interval has passed", func(t *testing.T) {
		p := newHeartbeatPinger(ts.URL)
		p.cycleCompleted(nil)
		expectHits(t, 1)
		p.last = time.Now().Add(-heartbeatPingInterval)
		p.cycleCompleted(nil)
		expectHits(t, 1)
	})
}

func TestNewHeartbeatPingerDisabled(t *testing.T) {
	for _, raw := range []string{"", "hc-ping.com/abc", "ftp://example.com/x", "http://"} {
		if p := newHeartbeatPinger(raw); p != nil {
			t.Errorf("newHeartbeatPinger(%q) = %v, want nil", raw, p)
		}
	}
	// A nil pinger is a no-op, so the worker loops need no nil check.
	var p *heartbeatPinger
	p.cycleCompleted(nil)
}
