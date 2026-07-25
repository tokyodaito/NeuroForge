package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeStubRuntime writes plausible pid/token/addr files so a reclaim decision
// can be observed against them.
func writeStubRuntime(t *testing.T, dirs Dirs, pid int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dirs.PIDFile), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dirs.PIDFile, dirs.TokenFile, dirs.AddrFile} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(dirs.PIDFile, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runtimeFilesPresent(dirs Dirs) bool {
	for _, p := range []string{dirs.PIDFile, dirs.TokenFile, dirs.AddrFile} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// TestReclaimStaleRuntime_NoPIDFileIsNoop: a clean home (no pid file) must not
// be touched. (I2 — never destructive on clean/absent state.)
func TestReclaimStaleRuntime_NoPIDFileIsNoop(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	reclaimStaleRuntime(dirs)
	// Nothing was ever written; nothing should appear.
	if runtimeFilesPresent(dirs) {
		t.Fatalf("reclaim created files on a clean home")
	}
}

// TestReclaimStaleRuntime_CorruptedPIDReclaims: an unparseable PID file is
// treated as corrupted (no identifiable live owner) and reclaimed. (R-2.5 / T6.)
func TestReclaimStaleRuntime_CorruptedPIDReclaims(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(dirs.PIDFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dirs.PIDFile, []byte("not-a-number\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dirs.TokenFile, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	reclaimStaleRuntime(dirs)
	if runtimeFilesPresent(dirs) {
		t.Fatalf("corrupted pid state was not reclaimed")
	}
}

// TestReclaimStaleRuntime_DeadPIDReclaims: a PID file pointing at a provably
// dead process is stale and reclaimed. (R-2.4 / T6.)
func TestReclaimStaleRuntime_DeadPIDReclaims(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	writeStubRuntime(t, dirs, 1<<30) // implausibly large PID — guaranteed dead
	if !processAlive(1 << 30) {
		// sanity: the PID really is dead
	}
	reclaimStaleRuntime(dirs)
	if runtimeFilesPresent(dirs) {
		t.Fatalf("stale (dead-pid) state was not reclaimed")
	}
}

// TestReclaimStaleRuntime_LivePIDIsPreserved: a PID file pointing at a LIVE
// process must NEVER be reclaimed — even though that process may be a daemon
// that is still binding and momentarily unhealthy. This is the core I2 / I3
// guarantee that eliminates the dual-daemon clobber race. (T6 / T7.)
func TestReclaimStaleRuntime_LivePIDIsPreserved(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	// Use this test process's own PID as the "live owner".
	writeStubRuntime(t, dirs, os.Getpid())
	if !processAlive(os.Getpid()) {
		t.Fatalf("sanity: test process must be alive")
	}
	reclaimStaleRuntime(dirs)
	if !runtimeFilesPresent(dirs) {
		t.Fatalf("reclaim removed files of a LIVE pid %d — would clobber a starting daemon", os.Getpid())
	}
	// Specifically the pid/token/addr of the live owner must be intact.
	for _, p := range []string{dirs.PIDFile, dirs.TokenFile, dirs.AddrFile} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("live-owner file removed: %s: %v", p, err)
		}
	}
}

// TestReclaimStaleRuntime_ReusedPIDIsPreserved: PID-reuse safety (I3). A PID
// that is alive (here: the test process) must not be cleaned just because the
// metadata looks stale to a higher-level probe. reclaimStaleRuntime relies only
// on process liveness, so a reused PID is conservatively treated as a live owner.
func TestReclaimStaleRuntime_ReusedPIDIsPreserved(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	// Simulate leftover metadata from a long-dead daemon whose PID was reused
	// by this test process. The addr/socket are stale strings, but the PID is
	// alive — reclaim must refuse to touch them.
	writeStubRuntime(t, dirs, os.Getpid())
	if err := os.WriteFile(dirs.AddrFile, []byte("http://127.0.0.1:1"), 0o600); err != nil {
		t.Fatal(err)
	}
	reclaimStaleRuntime(dirs)
	if !runtimeFilesPresent(dirs) {
		t.Fatalf("reused-but-alive pid state was cleaned — violates I3 (pid-reuse safety)")
	}
}
