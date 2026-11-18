package traveltime

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every i in [0, n) must be called exactly once, and each call writes to
// its own slot — this is the actual invariant runOnce's route loop relies
// on to be race-free without a mutex around the shared results slice.
func TestRunWithConcurrencyCallsEveryIndexExactlyOnce(t *testing.T) {
	const n = 37
	var calls [n]int32
	runWithConcurrency(n, 5, func(i int) {
		atomic.AddInt32(&calls[i], 1)
	})
	for i, c := range calls {
		if c != 1 {
			t.Errorf("index %d called %d times, want 1", i, c)
		}
	}
}

// The concurrency bound must actually be enforced — never more than
// `concurrency` calls to fn running at the same instant.
func TestRunWithConcurrencyNeverExceedsBound(t *testing.T) {
	const n, concurrency = 50, 4
	var current, max int32
	var mu sync.Mutex
	runWithConcurrency(n, concurrency, func(i int) {
		cur := atomic.AddInt32(&current, 1)
		mu.Lock()
		if cur > max {
			max = cur
		}
		mu.Unlock()
		time.Sleep(2 * time.Millisecond) // hold the slot briefly so overlaps are observable
		atomic.AddInt32(&current, -1)
	})
	if max > concurrency {
		t.Errorf("observed %d concurrent calls, want <= %d", max, concurrency)
	}
	if max < 2 {
		t.Errorf("observed max concurrency %d — test didn't actually exercise overlap, strengthen it", max)
	}
}

// A concurrency bound >= n (or <= 0, meaning "unbounded") must still run
// every task — just with no artificial serialization.
func TestRunWithConcurrencyZeroOrUnboundedStillRunsEverything(t *testing.T) {
	for _, c := range []int{0, -1, 1000} {
		var calls int32
		runWithConcurrency(10, c, func(i int) { atomic.AddInt32(&calls, 1) })
		if calls != 10 {
			t.Errorf("concurrency=%d: got %d calls, want 10", c, calls)
		}
	}
}

func TestRunWithConcurrencyZeroTasksIsANoop(t *testing.T) {
	called := false
	runWithConcurrency(0, 5, func(i int) { called = true })
	if called {
		t.Error("expected fn to never be called for n=0")
	}
}

// runOnce's route loop depends on returning only after every goroutine has
// actually finished writing its slot — not merely after they were all
// launched.
func TestRunWithConcurrencyWaitsForAllToFinish(t *testing.T) {
	const n = 20
	var done [n]bool
	runWithConcurrency(n, 3, func(i int) {
		time.Sleep(time.Millisecond)
		done[i] = true
	})
	for i, d := range done {
		if !d {
			t.Errorf("index %d: runWithConcurrency returned before its call finished", i)
		}
	}
}
