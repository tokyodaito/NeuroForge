//go:build windows

package daemon

import (
	"os"
	"testing"
)

// TestProcessAlive_Windows_OpenProcess locks in the Windows liveness probe.
//
// Regression guard: the previous non-Unix fallback returned `pid > 0`
// unconditionally (os.FindProcess always succeeds on Windows), which made the
// single-instance guard unable to distinguish a crashed daemon's leftover PID
// file from a live owner. That broke stale-PID reclaim and caused
// TestReconcile_StalePID_Reclaimed to fail. The OpenProcess-based probe must
// report the current process as alive and a never-recycled PID as not alive.
func TestProcessAlive_Windows_OpenProcess(t *testing.T) {
	// The current process must be reported alive.
	if !processAlive(os.Getpid()) {
		t.Fatalf("processAlive(self pid=%d) = false, want true", os.Getpid())
	}
	// Non-positive PIDs are never alive.
	if processAlive(0) {
		t.Error("processAlive(0) = true, want false")
	}
	if processAlive(-1) {
		t.Error("processAlive(-1) = true, want false")
	}
	// 1<<30 is far outside any real Windows PID range and is never recycled
	// during a test run, so it must read as not-alive (stale reclaim relies on it).
	if processAlive(1 << 30) {
		t.Error("processAlive(1<<30) = true, want false (stale PID must be reclaimable)")
	}
}
