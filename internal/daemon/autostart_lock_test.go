package daemon

import (
	"context"
	"testing"
	"time"
)

// TestLockFile_ActuallySerializes is the direct regression test for the BF-F-01
// root cause. The previous lockFile used `if err := flockTry(...); err == nil`
// followed by `if !errors.Is(err, ...)` — but that second `err` was the
// os.OpenFile error (nil after a successful open), not flockTry's error. So the
// first EAGAIN fell into the no-op fallback branch and the loop NEVER retried:
// every concurrent caller received a no-op "lock" and proceeded in parallel.
//
// This test proves the lock now genuinely serializes: a second concurrent
// lockFile call on the same path must BLOCK until the first releases, rather
// than returning a no-op lock immediately.
func TestLockFile_ActuallySerializes(t *testing.T) {
	dir := t.TempDir()
	lockPath := dir + "/test.lock"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	unlock1, err := lockFile(ctx, lockPath)
	if err != nil {
		t.Fatalf("first lockFile: %v", err)
	}
	defer unlock1()

	// Second concurrent acquirer: must NOT get a no-op lock. It should block
	// until unlock1 runs. Give it a short window to prove it is still waiting.
	gotLock := make(chan error, 1)
	start := time.Now()
	go func() {
		unlock2, err := lockFile(ctx, lockPath)
		if err != nil {
			gotLock <- err
			return
		}
		gotLock <- nil
		unlock2()
	}()

	select {
	case err := <-gotLock:
		t.Fatalf("second lockFile returned after %v without blocking (err=%v) — lock is a no-op, BF-F-01 regressed",
			time.Since(start), err)
	case <-time.After(250 * time.Millisecond):
		// good: still blocked waiting for the first holder
	}

	// Release the first; the second must now acquire within the timeout.
	unlock1()
	select {
	case err := <-gotLock:
		if err != nil {
			t.Fatalf("second lockFile failed after first released: %v", err)
		}
	case <-time.After(9 * time.Second):
		t.Fatalf("second lockFile never acquired after first released")
	}
}

// TestLockFile_ConcurrentAcquirersSerialize proves that N concurrent acquirers
// are granted the lock strictly one at a time (never two simultaneously).
func TestLockFile_ConcurrentAcquirersSerialize(t *testing.T) {
	dir := t.TempDir()
	lockPath := dir + "/multi.lock"

	const n = 8
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	inFlight := make(chan struct{}, n)
	maxInFlight := make(chan int, 1)
	maxInFlight <- 0
	done := make(chan error, n)

	for i := 0; i < n; i++ {
		go func() {
			unlock, err := lockFile(ctx, lockPath)
			if err != nil {
				done <- err
				return
			}
			inFlight <- struct{}{}
			cur := len(inFlight)
			prev := <-maxInFlight
			if cur > prev {
				prev = cur
			}
			maxInFlight <- prev
			// hold briefly to widen the overlap window a misbehaving lock would hit
			time.Sleep(15 * time.Millisecond)
			<-inFlight
			unlock()
			done <- nil
		}()
	}

	for i := 0; i < n; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("acquirer %d: %v", i, err)
			}
		case <-time.After(19 * time.Second):
			t.Fatalf("acquirer %d did not finish", i)
		}
	}

	if m := <-maxInFlight; m > 1 {
		t.Fatalf("lock admitted %d concurrent holders — BF-F-01 regressed (want <= 1)", m)
	}
}
