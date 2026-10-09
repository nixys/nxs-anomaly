package engine

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrIngestBusy is returned when an integration already has as many ingests
// waiting in this process as it may queue. The HTTP layer answers 503 with
// Retry-After: the sender keeps the alert and tries again, which is what an
// overloaded receiver should ask of it.
var ErrIngestBusy = errors.New("integration is busy, retry")

// ingestGateMaxWaiting is how many ingests of one integration may wait in one
// process behind those at the database. One integration takes 25–100 ingests
// a second (measured locally and on a sandbox), so a full queue is one to five
// seconds of work — a burst waits, a storm that would never catch up is told
// to come back.
const ingestGateMaxWaiting = 128

// ingestGateHolders is how many ingests of one integration go to the database at
// once. One integration gets somewhat faster the more of its ingests are in
// flight, though its advisory lock serializes the writes — measured locally at
// 21 alerts/s with one in flight, 25 with two to five, 33 with twenty (and all
// ten pooled connections spent on it). Two keep it within a fifth of that, and
// leave the pool to everything else.
const ingestGateHolders = 2

// ingestGateMaxWait is how long an ingest may wait for its turn when nobody
// said by when it has to be answered (WithAnswerBy): heartbeats, the CLI.
const ingestGateMaxWait = 10 * time.Second

// An ingest that came over HTTP waits until its answer is due (WithAnswerBy:
// the request's start plus the HTTP write timeout) less what the work after
// its turn may take: ingestReserveBase plus ingestReservePerAlert for every
// alert it carries. Past that it is told to come back (ErrIngestBusy, 503 +
// Retry-After).
//
// A fixed bound cannot serve both kinds of request. An Alertmanager envelope
// carries up to a hundred alerts: with no bound its POSTs waited 18–30 s under
// load, and past the 30 s write timeout the server dropped the answer to an
// envelope it went on to store in full — the sender saw a failure and sent it
// again. A single alert is done in well under a second once it has its turn:
// bounded at 10 s like an envelope, 6–7 % of the alerts of one integration at
// 50/s beside forty readers were refused with twenty seconds of the write
// timeout left (sandbox, 1.9.20). The reserve is over twice the slowest
// envelope measured under load (4.6 s for 100 alerts after its turn, sandbox),
// so the answer still goes out before the write timeout cuts it.
const (
	ingestReserveBase     = 2 * time.Second
	ingestReservePerAlert = 100 * time.Millisecond
)

type answerByKey struct{}

// WithAnswerBy records when the caller must have its answer, so an ingest
// waiting its turn gives up while there is still time to say so.
func WithAnswerBy(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, answerByKey{}, t)
}

// AnswerBy is the time recorded by WithAnswerBy, if any.
func AnswerBy(ctx context.Context) (time.Time, bool) {
	by, ok := ctx.Value(answerByKey{}).(time.Time)
	return by, ok
}

// waitLimit is how long an ingest of alerts alerts may wait for its turn.
func (g *ingestGate) waitLimit(ctx context.Context, alerts int) time.Duration {
	by, ok := AnswerBy(ctx)
	if !ok {
		return g.maxWait
	}
	return time.Until(by) - ingestReserveBase - time.Duration(alerts)*ingestReservePerAlert
}

// ingestGate lets ingestGateHolders ingests per integration go to the database
// at a time in this process; the others wait here, holding no connection.
//
// The ingest lock is a PostgreSQL advisory lock taken inside the transaction,
// so an ingest waiting for it holds a pooled connection while it waits. The
// pool has ten. An alert storm on one integration used to park all ten on that
// one lock, and everything else the process does — other integrations, the
// web UI, the phone — queued for a connection behind it: measured locally at
// 200 alerts/s on one integration, a users list went from 3 ms to 3.4 s and an
// alert on another integration from 38 ms to 8.3 s. Waiting here costs a
// goroutine, and the queue is bounded, so a storm neither starves the pool nor
// grows without limit.
//
// The gate is per process; replicas still meet on the advisory lock, which
// stays the guarantee. It only decides how many connections each replica
// spends on waiting for it: at most ingestGateHolders per integration.
type ingestGate struct {
	mu      sync.Mutex
	slots   map[string]*ingestSlot
	maxWait time.Duration
}

type ingestSlot struct {
	tokens chan struct{} // one token per free place at the database
	users  int           // the holders and everyone waiting
}

func newIngestGate() *ingestGate {
	return &ingestGate{slots: map[string]*ingestSlot{}, maxWait: ingestGateMaxWait}
}

// enter waits for the slot of key for an ingest of alerts alerts and returns
// its release, or ErrIngestBusy at once when the queue is full or when its
// wait limit passes without a turn, or the context's error if the caller gives
// up.
func (g *ingestGate) enter(ctx context.Context, key string, alerts int) (func(), error) {
	g.mu.Lock()
	s := g.slots[key]
	if s == nil {
		s = &ingestSlot{tokens: make(chan struct{}, ingestGateHolders)}
		for range ingestGateHolders {
			s.tokens <- struct{}{}
		}
		g.slots[key] = s
	}
	if s.users >= ingestGateHolders+ingestGateMaxWaiting {
		g.mu.Unlock()
		return nil, ErrIngestBusy
	}
	s.users++
	g.mu.Unlock()

	release := func() {
		s.tokens <- struct{}{}
		g.leave(key, s)
	}
	// A free place is taken even when the wait limit is already spent.
	select {
	case <-s.tokens:
		return release, nil
	default:
	}
	timer := time.NewTimer(g.waitLimit(ctx, alerts))
	defer timer.Stop()
	select {
	case <-s.tokens:
		return release, nil
	case <-timer.C:
		g.leave(key, s)
		return nil, ErrIngestBusy
	case <-ctx.Done():
		g.leave(key, s)
		return nil, ctx.Err()
	}
}

// enterIngest passes the ingest of integrationKey through the gate. It comes
// before the integration is even looked up, so an ingest waiting its turn uses
// no connection at all.
func (e *Engine) enterIngest(ctx context.Context, integrationKey string, alerts int) (func(), error) {
	if e.ingestGate == nil {
		return func() {}, nil
	}
	return e.ingestGate.enter(ctx, integrationKey, alerts)
}

func (g *ingestGate) leave(key string, s *ingestSlot) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s.users--
	if s.users == 0 {
		delete(g.slots, key)
	}
}
