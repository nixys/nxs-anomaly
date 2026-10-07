package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIngestGateBoundsHoldersPerKey(t *testing.T) {
	g := newIngestGate()
	var releases []func()
	for range ingestGateHolders {
		r, err := g.enter(context.Background(), "k1", 1)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	// Another key is not held up.
	other, err := g.enter(context.Background(), "k2", 1)
	if err != nil {
		t.Fatal(err)
	}
	other()

	got := make(chan struct{})
	go func() {
		r, err := g.enter(context.Background(), "k1", 1)
		if err != nil {
			t.Error(err)
			return
		}
		close(got)
		r()
	}()
	select {
	case <-got:
		t.Fatal("an ingest entered past ingestGateHolders of the same key")
	case <-time.After(50 * time.Millisecond):
	}
	releases[0]()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("the waiting ingest did not enter after release")
	}
	for _, r := range releases[1:] {
		r()
	}
}

// A storm is told to come back once the queue is full, instead of piling up.
func TestIngestGateRefusesPastTheQueue(t *testing.T) {
	g := newIngestGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < ingestGateHolders+ingestGateMaxWaiting; i++ {
		go func() { _, _ = g.enter(ctx, "k1", 1) }()
	}
	deadline := time.Now().Add(time.Second)
	for {
		users := 0
		g.mu.Lock()
		if s := g.slots["k1"]; s != nil { // not there until a goroutine enters
			users = s.users
		}
		g.mu.Unlock()
		if users == ingestGateHolders+ingestGateMaxWaiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("users = %d, want %d", users, ingestGateHolders+ingestGateMaxWaiting)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := g.enter(context.Background(), "k1", 1); !errors.Is(err, ErrIngestBusy) {
		t.Fatalf("enter past a full queue = %v, want ErrIngestBusy", err)
	}
}

// A caller that gives up leaves the queue, and the slot is freed when nobody
// uses it.
func TestIngestGateCancelledWaiterLeaves(t *testing.T) {
	g := newIngestGate()
	var releases []func()
	for range ingestGateHolders {
		r, err := g.enter(context.Background(), "k1", 1)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.enter(ctx, "k1", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled enter = %v, want context.Canceled", err)
	}
	for _, r := range releases {
		r()
	}
	g.mu.Lock()
	n := len(g.slots)
	g.mu.Unlock()
	if n != 0 {
		t.Fatalf("slots left = %d, want 0", n)
	}
}

// A turn that does not come within maxWait is a refusal, not a longer wait: a
// waiter that outlives the HTTP write timeout loses its answer, while the
// ingest it was queued for still runs and the sender sends it again.
func TestIngestGateRefusesAfterMaxWait(t *testing.T) {
	g := newIngestGate()
	g.maxWait = 50 * time.Millisecond
	var releases []func()
	for range ingestGateHolders {
		r, err := g.enter(context.Background(), "k1", 1)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	t0 := time.Now()
	if _, err := g.enter(context.Background(), "k1", 1); !errors.Is(err, ErrIngestBusy) {
		t.Fatalf("enter with every holder busy past maxWait = %v, want ErrIngestBusy", err)
	}
	if waited := time.Since(t0); waited > time.Second {
		t.Fatalf("waited %v, want about maxWait", waited)
	}
	for _, r := range releases {
		r()
	}
	g.mu.Lock()
	n := len(g.slots)
	g.mu.Unlock()
	if n != 0 {
		t.Fatalf("slots left = %d, want 0: the refused waiter must leave", n)
	}
	if ingestGateMaxWait >= 30*time.Second {
		t.Fatalf("ingestGateMaxWait = %v must stay well under the 30 s HTTP write timeout", ingestGateMaxWait)
	}
}

// An ingest that says when its answer is due waits until then, less the
// reserve for its own work — not the fixed bound. Before, a single alert was
// refused after 10 s with twenty seconds of the write timeout still left.
func TestIngestGateWaitsUntilTheAnswerIsDue(t *testing.T) {
	g := newIngestGate()
	g.maxWait = 50 * time.Millisecond // the bound without a deadline
	var releases []func()
	for range ingestGateHolders {
		r, err := g.enter(context.Background(), "k1", 1)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	go func() {
		time.Sleep(300 * time.Millisecond) // past maxWait, before the answer is due
		releases[0]()
	}()
	due := time.Now().Add(ingestReserveBase + ingestReservePerAlert + 2*time.Second)
	r, err := g.enter(WithAnswerBy(context.Background(), due), "k1", 1)
	if err != nil {
		t.Fatalf("enter with the answer due in 2 s, turn after 0.3 s = %v, want a turn", err)
	}
	r()
	releases[1]()
}

// The reserve grows with the alerts a request carries: an envelope whose work
// would not fit before its answer is due is refused at once, while a free
// place is still taken whatever the limit.
func TestIngestGateReservesTimeForTheWork(t *testing.T) {
	g := newIngestGate()
	due := time.Now().Add(ingestReserveBase + 50*ingestReservePerAlert)
	ctx := WithAnswerBy(context.Background(), due)
	var releases []func()
	for range ingestGateHolders {
		r, err := g.enter(ctx, "k1", 100) // free places: taken though the wait limit is spent
		if err != nil {
			t.Fatalf("enter with a free place = %v, want a turn", err)
		}
		releases = append(releases, r)
	}
	t0 := time.Now()
	if _, err := g.enter(ctx, "k1", 100); !errors.Is(err, ErrIngestBusy) {
		t.Fatalf("100 alerts with time for 50 = %v, want ErrIngestBusy", err)
	}
	if waited := time.Since(t0); waited > 200*time.Millisecond {
		t.Fatalf("refused after %v, want at once", waited)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		releases[0]()
	}()
	r, err := g.enter(ctx, "k1", 1) // one alert fits in the same time
	if err != nil {
		t.Fatalf("1 alert with time for 50 = %v, want a turn", err)
	}
	r()
	releases[1]()
}
