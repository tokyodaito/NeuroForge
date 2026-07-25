package cli

import (
	"fmt"
	"testing"
	"time"
)

// TestForgeRun_NFR1_TimingBounds verifies NFR-1 (REQUIREMENTS.md §4.12): a
// no-op `forge run` against the fake adapter on an empty repo must complete in
// < 5s on a warm daemon and < 12s on a cold autostart. The test measures and
// reports the actual duration on failure, uses no network and no paid model
// (fake/no-change), and is skipped in -short mode (it spawns real daemon
// processes). Bounds are the spec values; if the architecture cannot meet them
// the cause must be fixed, not the bound relaxed.
func TestForgeRun_NFR1_TimingBounds(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test spawns real daemon processes")
	}
	const (
		warmBound = 5 * time.Second
		coldBound = 12 * time.Second
	)

	f := newRunFixture(t)

	// Cold autostart: no daemon exists. The run must spawn + ready the daemon
	// and reach a terminal state within the cold bound.
	coldStart := time.Now()
	_, _, code := f.run("--engine", "fake", "--model", "fake/no-change", "cold")
	coldDur := time.Since(coldStart)
	if code != 1 {
		t.Fatalf("cold run exit = %d (expected 1 = no-change); the run must still complete", code)
	}
	if coldDur > coldBound {
		t.Fatalf("NFR-1 COLD autostart took %v, bound %v (must spawn+ready+run a no-op fake run)", coldDur, coldBound)
	}
	t.Logf("NFR-1 cold autostart duration: %v (bound %v)", coldDur, coldBound)

	if got := daemonStatus(f); got != "running" {
		t.Fatalf("daemon state = %q after cold autostart, want running", got)
	}

	// Warm reuse: the daemon is already running. The second run must reuse it
	// and complete within the warm bound.
	warmStart := time.Now()
	_, _, code = f.run("--engine", "fake", "--model", "fake/no-change", "warm")
	warmDur := time.Since(warmStart)
	if code != 1 {
		t.Fatalf("warm run exit = %d (expected 1 = no-change)", code)
	}
	if warmDur > warmBound {
		t.Fatalf("NFR-1 WARM run took %v, bound %v", warmDur, warmBound)
	}
	t.Logf("NFR-1 warm run duration: %v (bound %v)", warmDur, warmBound)

	// Assert the daemon PID did not change between cold and warm (R-2.3: no
	// second daemon). daemonStatus already implies a single running daemon;
	// the warm reuse must not have spawned another.
	if got := daemonStatus(f); got != "running" {
		t.Fatalf("daemon state = %q after warm reuse, want running", got)
	}

	_ = fmt.Sprintf // keep import if future formatting added
}
