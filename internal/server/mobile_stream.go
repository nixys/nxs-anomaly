package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// The event stream a phone listens on (GET /api/v1/mobile/events).
//
// The app has no push: it polled every fifteen minutes, which is Android's
// floor for periodic work and is not a way to wake somebody at night. There is
// no Firebase here and there is not going to be one — this is a self-hosted
// product, and a push service would put every installation's alerts through a
// third party. So the phone holds this stream open instead and learns about a
// group seconds after it appears.
//
// The stream is deliberately dumb: on every tick it asks the engine which
// groups concern this person right now and reports what changed since the last
// tick. It carries no state across connections, so a reconnect after a dead
// network or a killed process re-synchronises by itself — the phone gets the
// current set and decides what is new to it. That matters more than saving the
// queries a smarter design would: a missed wake-up is the failure this feature
// exists to prevent.

// mobileStreamInterval is how often the stream re-reads. Seconds, not minutes:
// this is the latency between an alert and a phone ringing.
func mobileStreamInterval() time.Duration {
	return utils.EnvSeconds("NXS_ANOMALY_MOBILE_STREAM_INTERVAL_SECONDS", 10*time.Second, 1)
}

// mobileStreamHeartbeat bounds how long the stream can be silent. A proxy that
// sees nothing on a connection closes it, and the phone would keep believing it
// is being watched over.
const mobileStreamHeartbeat = 20 * time.Second

// mobileStreamLimit caps concurrent streams for the whole process. Each one
// costs a connection and a periodic read; the cap is what stops a bug in a
// client — or a phone reconnecting in a loop — from becoming a load problem.
func mobileStreamLimit() int {
	return utils.EnvInt("NXS_ANOMALY_MOBILE_STREAM_MAX", 200, 1)
}

// openMobileStreams counts live streams, for the cap and for /metrics.
var openMobileStreams atomic.Int64

// MobileStreamCount reports how many event streams are open right now.
func MobileStreamCount() int64 { return openMobileStreams.Load() }

// handleMobileEvents streams the groups that concern the caller.
func (srv *Server) handleMobileEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := authz.FromContext(ctx)
	// An API key is not a person: there is no "groups that concern me" for it.
	if actor.Kind != authz.KindUser || actor.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "the event stream is for a signed-in person or a paired phone, not an API key"})
		return
	}
	// Through the controller, not a direct type assertion: the writer arrives
	// wrapped by the access-log middleware, and the assertion failed on that
	// wrapper rather than on anything about the connection.
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		slog.Error("mobile_stream_cannot_flush", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming is not supported here"})
		return
	}
	if n := openMobileStreams.Add(1); n > int64(mobileStreamLimit()) {
		openMobileStreams.Add(-1)
		w.Header().Set("Retry-After", "30")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "too many open event streams on this server"})
		return
	}
	defer openMobileStreams.Add(-1)

	// The write deadline of an ordinary request would cut this connection
	// after NXS_ANOMALY_HTTP_WRITE_TIMEOUT_SECONDS (30 by default). A stream
	// has no such bound; the read side keeps its own.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		slog.Debug("mobile_stream_no_write_deadline_control", "error", err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Nginx buffers proxied responses by default, which would hold events
	// until the buffer fills — that is, until the wake-up no longer matters.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	interval := mobileStreamInterval()
	send := func(event string, payload any) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			slog.Error("mobile_stream_marshal_failed", "event", event, "error", err)
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
			return false
		}
		if err := rc.Flush(); err != nil {
			return false
		}
		return true
	}
	slog.Debug("mobile_stream_open", "user_id", actor.ID, "open_streams", openMobileStreams.Load())
	if !send("hello", map[string]any{
		"interval_seconds": int(interval / time.Second),
		"server_time":      utils.ToISO(utils.UTCNow()),
	}) {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	heartbeat := time.NewTicker(mobileStreamHeartbeat)
	defer heartbeat.Stop()

	// nil means "nothing read yet": the first tick reports the current set as
	// the baseline, and the phone decides what of it is news to it.
	var previous map[string]mobileGroupBrief
	for {
		groups, err := srv.eng.MobileRelevantGroups(ctx, actor.ID)
		if err != nil {
			// A failed read is not the end of the stream: the database may be
			// restarting, and dropping the connection would send every phone
			// into a reconnect loop at exactly the wrong moment.
			slog.Warn("mobile_stream_read_failed", "user_id", actor.ID, "error", err)
			if !send("error", map[string]any{"detail": "could not read alert groups"}) {
				return
			}
		} else {
			current := briefsByID(groups)
			if !send("groups", mobileStreamPayload(previous, current)) {
				return
			}
			previous = current
		}
		select {
		case <-ctx.Done():
			slog.Debug("mobile_stream_closed", "user_id", actor.ID)
			return
		case <-ticker.C:
			// The credentials were checked once, when the stream opened. A
			// phone signed out from the web, an expired session or a deleted
			// person must stop receiving groups now, not when the connection
			// happens to drop. The phone reconnects, is refused, and stops.
			if again, ok := srv.authenticate(r); !ok || again.ID != actor.ID {
				slog.Info("mobile_stream_signed_out", "user_id", actor.ID)
				return
			}
		case <-heartbeat.C:
			// An SSE comment: it keeps proxies and NAT from forgetting the
			// connection without looking like an event to the client.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// mobileGroupBrief is what the phone needs to decide whether to ring: which
// group, how bad, and where it stands now.
type mobileGroupBrief struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Severity   string `json:"severity"`
	Status     string `json:"status"`
	AlertCount int    `json:"alert_count"`
	ReceivedAt string `json:"last_received_at"`
}

func briefsByID(groups []map[string]any) map[string]mobileGroupBrief {
	out := make(map[string]mobileGroupBrief, len(groups))
	for _, raw := range groups {
		g := model.WrapAlertGroup(raw)
		out[g.ID()] = mobileGroupBrief{
			ID:         g.ID(),
			Title:      g.Title(),
			Severity:   g.Severity(),
			Status:     g.Status(),
			AlertCount: g.AlertCount(),
			ReceivedAt: g.LastReceivedAt(),
		}
	}
	return out
}

// mobileStreamPayload describes one tick: everything that concerns the person,
// plus which of it is new or has changed since the previous tick.
//
// The full set travels every time, not only the difference. It is small (the
// groups one person is on the hook for), and it means a phone that missed a
// tick — asleep, off the network, restarted — is correct again after the next
// one instead of waiting for something to change.
func mobileStreamPayload(previous, current map[string]mobileGroupBrief) map[string]any {
	groups := make([]mobileGroupBrief, 0, len(current))
	added := make([]string, 0)
	changed := make([]string, 0)
	for id, g := range current {
		groups = append(groups, g)
		before, seen := previous[id]
		switch {
		case previous == nil:
			// Baseline tick: nothing is "new" yet, or a phone reconnecting
			// would ring for every group it already knows about.
		case !seen:
			added = append(added, id)
		case before != g:
			changed = append(changed, id)
		}
	}
	// Ordered so that two ticks with the same content produce the same bytes:
	// a test can compare them, and a log line is readable.
	sort.Strings(added)
	sort.Strings(changed)
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return map[string]any{
		"groups":   groups,
		"added":    added,
		"changed":  changed,
		"baseline": previous == nil,
		"at":       utils.ToISO(utils.UTCNow()),
	}
}
