package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestJSConcurrencyLimit verifies that the global semaphore caps how many
// runJS calls can hold a goja runtime simultaneously. The cap exists to
// prevent the thundering-herd OOM pattern from a burst of concurrent
// renders that each instantiate a fresh runtime.
func TestJSConcurrencyLimit(t *testing.T) {
	limit := cap(jsConcurrencySem)
	if limit <= 0 {
		t.Fatalf("jsConcurrencySem must be a bounded channel, got cap=%d", limit)
	}

	// Each goroutine acquires a slot, signals it's running, then blocks
	// until the test releases it. With the slot held, the test counts
	// how many goroutines are concurrently inside the critical section.
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	gate := make(chan struct{})

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < limit*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			jsConcurrencySem <- struct{}{}
			defer func() { <-jsConcurrencySem }()
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				cur := maxInFlight.Load()
				if n <= cur || maxInFlight.CompareAndSwap(cur, n) {
					break
				}
			}
			<-gate
		}()
	}
	close(start)

	// Wait until enough goroutines are queued up that we'd see the cap.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if int(maxInFlight.Load()) >= limit {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	wg.Wait()

	if got := maxInFlight.Load(); int(got) != limit {
		t.Errorf("maxInFlight = %d, want %d (the semaphore cap)", got, limit)
	}
}

// TestRunJSAcquireTimeout verifies that runJS gives up waiting for a slot
// after jsAcquireTimeout and returns errJSConcurrencyTimeout. We hold all
// slots from background goroutines, then call runJS with a deliberately
// short timeout via a manual sem swap.
func TestRunJSAcquireTimeout(t *testing.T) {
	// Save and restore the global sem so this test doesn't leak.
	orig := jsConcurrencySem
	jsConcurrencySem = make(chan struct{}, 1)
	t.Cleanup(func() { jsConcurrencySem = orig })

	// Fill the (size-1) sem so the next acquire would block.
	jsConcurrencySem <- struct{}{}
	defer func() { <-jsConcurrencySem }()

	// Run with a context-equivalent: we can't easily reduce
	// jsAcquireTimeout for this one call, so we just verify the error
	// type via a short timeout select. Wrap runJS in a goroutine and
	// fail fast if it doesn't return quickly enough — but we WANT it
	// to wait the full jsAcquireTimeout. Skip if that would slow the
	// suite unduly.
	if testing.Short() {
		t.Skip("would take jsAcquireTimeout to confirm error")
	}

	done := make(chan error, 1)
	go func() {
		_, err := runJS("print('x');", nil, nil, false, "")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errJSConcurrencyTimeout) {
			t.Errorf("err = %v, want errJSConcurrencyTimeout", err)
		}
	case <-time.After(jsAcquireTimeout + 5*time.Second):
		t.Fatal("runJS did not return within jsAcquireTimeout + slack")
	}
}
