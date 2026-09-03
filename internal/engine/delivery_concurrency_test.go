package engine

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestRunBoundedCollectsKeptResults verifies that runBounded returns exactly the
// results fn chose to keep (skips dropped, nothing lost or duplicated). Run under
// -race it also exercises the concurrent result collection.
func TestRunBoundedCollectsKeptResults(t *testing.T) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}
	got := runBounded(items, 8, func(n int) (int, bool) {
		if n%2 == 0 {
			return 0, false // skip evens
		}
		return n * 10, true
	})
	if len(got) != 50 {
		t.Fatalf("kept %d results, want 50", len(got))
	}
	seen := map[int]bool{}
	for _, v := range got {
		if v%20 != 10 {
			t.Errorf("unexpected result %d (not an odd*10)", v)
		}
		if seen[v] {
			t.Errorf("duplicate result %d", v)
		}
		seen[v] = true
	}
}

// TestRunBoundedRespectsLimit verifies that exactly `limit` invocations of fn run
// at once and never more: each invocation blocks on a barrier, so they pile up to
// the bound; the semaphore in runBounded guarantees the bound is never exceeded.
func TestRunBoundedRespectsLimit(t *testing.T) {
	const limit, items = 4, 32
	var inFlight, maxSeen int32
	release := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once
	done := make(chan struct{})
	go func() {
		runBounded(make([]int, items), limit, func(int) (int, bool) {
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				m := atomic.LoadInt32(&maxSeen)
				if cur <= m || atomic.CompareAndSwapInt32(&maxSeen, m, cur) {
					break
				}
			}
			if cur == limit {
				once.Do(func() { close(reached) })
			}
			<-release // hold the slot until `limit` are simultaneously in flight
			atomic.AddInt32(&inFlight, -1)
			return 0, true
		})
		close(done)
	}()
	<-reached // limit invocations are now held simultaneously
	close(release)
	<-done
	if got := atomic.LoadInt32(&maxSeen); got != limit {
		t.Fatalf("max concurrent invocations = %d, want exactly %d", got, limit)
	}
}

// TestRunBoundedSequentialFallback verifies limit<1 runs (sequentially) rather
// than hanging or dropping items — the zero-value config default.
func TestRunBoundedSequentialFallback(t *testing.T) {
	items := []int{1, 2, 3}
	got := runBounded(items, 0, func(n int) (int, bool) { return n, true })
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
}

// TestRunBoundedRecoversPanic verifies a panicking task never crashes the worker
// process: the panic is recovered, that item is dropped from the results, and the
// non-panicking items still complete.
func TestRunBoundedRecoversPanic(t *testing.T) {
	got := runBounded([]int{0, 1, 2, 3, 4, 5}, 3, func(n int) (int, bool) {
		if n%2 == 1 {
			panic("boom")
		}
		return n, true
	})
	if len(got) != 3 { // 0, 2, 4 succeed; 1, 3, 5 panic and are dropped
		t.Fatalf("got %d results, want 3 (panicking items dropped): %v", len(got), got)
	}
	for _, v := range got {
		if v%2 != 0 {
			t.Errorf("unexpected odd (panicking) result %d in %v", v, got)
		}
	}
}
