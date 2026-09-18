package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// heartbeatPingInterval bounds how often a completed cycle is reported. Cycles
// run every few seconds; dead-man's-switch services expect a period measured in
// minutes and rate-limit anything much faster.
const heartbeatPingInterval = time.Minute

const heartbeatPingTimeout = 10 * time.Second

// heartbeatPinger reports completed worker cycles to an external dead-man's
// switch (healthchecks.io, Cronitor, an Uptime Kuma push monitor, ...).
//
// It exists for the one failure nxs-anomaly cannot page about itself: when the
// worker, its database or the network between them is down, so is the path
// every notification takes. The pings then stop, and the external service
// raises the alarm through a channel that does not depend on this process.
//
// A nil pinger is valid and does nothing, so callers need no "configured?"
// check.
type heartbeatPinger struct {
	url    string
	client *http.Client
	// last is touched only by the worker loop goroutine.
	last     time.Time
	inFlight atomic.Bool
}

// newHeartbeatPinger returns nil when raw is empty. An unusable URL is logged
// and also yields nil: the external service then sees no pings and alarms,
// which is the loud failure we want rather than a refusal to start.
func newHeartbeatPinger(raw string) *heartbeatPinger {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// The URL itself is not logged: these services put the check's secret
		// token in it.
		slog.Error("worker_heartbeat_url_invalid",
			"reason", "NXS_ANOMALY_WORKER_HEARTBEAT_URL must be an absolute http(s) URL",
			"effect", "no heartbeat pings will be sent")
		return nil
	}
	slog.Info("worker_heartbeat_enabled", "host", u.Host, "interval", heartbeatPingInterval)
	return &heartbeatPinger{url: raw, client: &http.Client{Timeout: heartbeatPingTimeout}}
}

// cycleCompleted is called after every worker cycle with the cycle's error.
// Only a cycle that succeeded counts. The ping runs in the background: a slow or
// unreachable monitor must never delay escalation.
func (p *heartbeatPinger) cycleCompleted(cycleErr error) {
	if p == nil || cycleErr != nil || time.Since(p.last) < heartbeatPingInterval {
		return
	}
	if !p.inFlight.CompareAndSwap(false, true) {
		return
	}
	p.last = time.Now()
	go func() {
		defer p.inFlight.Store(false)
		p.ping()
	}()
}

func (p *heartbeatPinger) ping() {
	resp, err := p.client.Get(p.url)
	if err != nil {
		slog.Warn("worker_heartbeat_ping_failed", "error", redactURLError(err))
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		slog.Warn("worker_heartbeat_ping_rejected", "status", resp.StatusCode)
	}
}

// redactURLError drops the URL from a client error, which would otherwise
// print the check's token into the log.
func redactURLError(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}
