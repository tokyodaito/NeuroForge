package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/daemon"
)

// TestForgeRun_DualDaemonRace (B-11 / R-2.3 / BF-F-01): several `forge run`
// invocations start concurrently against a fresh home with no daemon. Exactly
// ONE daemon process must be created, all clients must reach a correct terminal
// result, the PID file must hold a single live pid (sampled throughout the
// burst so a transient second daemon cannot hide), AND the OS process table is
// sampled throughout the burst so a fast-dying second daemon cannot hide by
// exiting before the next PID-file poll. The property must hold under
// repetition and leave no orphan daemon processes. Artifacts (daemon log) are
// dumped on failure.
func TestForgeRun_DualDaemonRace(t *testing.T) {
	if testing.Short() {
		t.Skip("dual-daemon race spawns real processes")
	}
	const clients = 8
	const repeats = 3

	for r := 0; r < repeats; r++ {
		t.Run("repeat", func(t *testing.T) {
			f := newRunFixture(t)
			dirs := daemon.WithRoot(f.home)

			// Ensure no daemon for THIS home yet (fresh cold start).
			if got := daemonStatus(f); got == "running" {
				t.Fatalf("precondition: daemon already running for fresh home")
			}

			// Sample the PID file throughout the burst to collect every pid that
			// ever served this home. A correct autostart yields exactly one.
			pidSeen := make(map[int]struct{})
			// Sample the OS process table throughout the burst. The PID file is
			// only written AFTER a child binds, so a fast-dying second daemon
			// could otherwise hide between bind-of-A and bind-of-B. Counting
			// live `daemon run` processes for this binary catches any overlap
			// regardless of who owns the PID file (BF-F-01 / T1).
			var procMu sync.Mutex
			maxConcurrentDaemons := 0
			var samplerWG sync.WaitGroup
			stopSampler := make(chan struct{})
			samplerWG.Add(1)
			go func() {
				defer samplerWG.Done()
				for {
					select {
					case <-stopSampler:
						return
					default:
					}
					if pid, err := readPIDFile(dirs.PIDFile); err == nil && pid > 0 {
						pidSeen[pid] = struct{}{}
					}
					if n := daemonProcCount(f.bin); n > 0 {
						procMu.Lock()
						if n > maxConcurrentDaemons {
							maxConcurrentDaemons = n
						}
						procMu.Unlock()
					}
					time.Sleep(3 * time.Millisecond)
				}
			}()

			var wg sync.WaitGroup
			errs := make([]error, clients)
			codes := make([]int, clients)
			wg.Add(clients)
			startGate := make(chan struct{})
			for i := 0; i < clients; i++ {
				go func(idx int) {
					defer wg.Done()
					<-startGate // release all clients simultaneously
					_, stderr, code := f.run("--engine", "fake", "--model", "fake/no-change",
						"concurrent "+strconv.Itoa(idx))
					codes[idx] = code
					if code != 1 {
						errs[idx] = errDualDaemon("client " + strconv.Itoa(idx) +
							" exit=" + strconv.Itoa(code) + " stderr=" + stderr)
					}
				}(i)
			}
			close(startGate)

			doneCh := make(chan struct{})
			go func() { wg.Wait(); close(doneCh) }()
			select {
			case <-doneCh:
			case <-time.After(90 * time.Second):
				close(stopSampler)
				t.Fatalf("concurrent clients did not finish within 90s (deadlock?)")
			}
			close(stopSampler)
			samplerWG.Wait()

			for i, err := range errs {
				if err != nil {
					t.Errorf("client %d: %v", i, err)
				}
			}

			// Exactly one daemon pid must ever have served this home.
			if len(pidSeen) != 1 {
				dumpDaemonArtifacts(t, dirs)
				pids := make([]int, 0, len(pidSeen))
				for p := range pidSeen {
					pids = append(pids, p)
				}
				t.Fatalf("repeat %d: saw %d distinct daemon pids %v (want exactly 1) — dual daemon spawned",
					r, len(pidSeen), pids)
			}

			// T1 / BF-F-01: the OS process table must never have shown more
			// than ONE live daemon for this binary at any instant during the
			// burst. A fast-dying second daemon cannot hide here even if it
			// never won the PID file. (Forbids "only check the final PID file".)
			procMu.Lock()
			mcd := maxConcurrentDaemons
			procMu.Unlock()
			if mcd > 1 {
				dumpDaemonArtifacts(t, dirs)
				t.Fatalf("repeat %d: process table saw %d concurrent daemon processes (want <= 1) — transient dual daemon",
					r, mcd)
			}

			// That single pid must be alive and recorded.
			var pid int
			for p := range pidSeen {
				pid = p
			}
			if !pidAlive(pid) {
				t.Fatalf("recorded daemon pid %d is not alive", pid)
			}
			curPID, _ := readPIDFile(dirs.PIDFile)
			if curPID != pid {
				t.Fatalf("pid file flipped: sampled %d, now %d", pid, curPID)
			}

			if got := daemonStatus(f); got != "running" {
				t.Fatalf("daemon state = %q, want running", got)
			}

			// System-wide check (secondary, tolerant): the number of daemon
			// processes for this binary must not grow without bound. After all
			// clients exited, at least the one daemon remains; we only fail on
			// a clear dual-spawn (>= 2 daemons added that are still alive and
			// not attributable to other tests). The per-home pid sampling above
			// is the authoritative uniqueness proof.
			aliveDaemons := 0
			for p := range pidSeen {
				if pidAlive(p) {
					aliveDaemons++
				}
			}
			if aliveDaemons > 1 {
				t.Fatalf("repeat %d: %d daemons for one home are still alive", r, aliveDaemons)
			}

			// A fresh single run on the now-warm daemon must still succeed and
			// reuse the SAME pid (no second daemon, no stale lock).
			_, _, code := f.run("--engine", "fake", "--model", "fake/no-change", "warm-check")
			if code != 1 {
				t.Fatalf("warm-check exit = %d, want 1", code)
			}
			pidAfterWarm, _ := readPIDFile(dirs.PIDFile)
			if pidAfterWarm != pid {
				t.Fatalf("daemon pid changed after warm reuse: %d -> %d (second daemon spawned?)",
					pid, pidAfterWarm)
			}

			// T10: cleanup leaves no orphan daemon processes for this binary.
			// The fixture's withDaemonCleanup stops the home's daemon; here we
			// sanity-check there is exactly one live daemon proc for this binary
			// (the warm daemon), not a herd of orphaned starters.
			if n := daemonProcCount(f.bin); n > 1 {
				dumpDaemonArtifacts(t, dirs)
				t.Fatalf("repeat %d: %d daemon processes alive after burst (orphan leak)", r, n)
			}
		})
	}
}

// TestForgeRun_StaleAutostartLockNotStuck verifies that a leftover (unheld)
// autostart.lock file — e.g. after a holder crashed, which the OS auto-releases
// — does not block a subsequent cold start. flock is on the fd, so a stale
// lock FILE is harmless; this pins that behaviour.
func TestForgeRun_StaleAutostartLockNotStuck(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real daemon")
	}
	f := newRunFixture(t)
	dirs := daemon.WithRoot(f.home)
	lockPath := filepath.Join(dirs.Root, "autostart.lock")
	if err := os.WriteFile(lockPath, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, code := f.run("--engine", "fake", "--model", "fake/no-change", "x")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (no-change); stale lock file must not block autostart", code)
	}
	if got := daemonStatus(f); got != "running" {
		t.Fatalf("daemon state = %q, want running", got)
	}
}

// TestForgeRun_DirectDaemonDoubleStart_OnlyOneBinds (T8 / BF-F-01): two daemon
// processes started DIRECTLY (bypassing the CLI autostart lock) against the same
// fresh home must not both bind. The authoritative daemon-side guard (bind.lock
// + post-acquire health re-check) must make exactly one the owner; the loser
// must exit cleanly WITHOUT deleting or overwriting the winner's runtime files,
// and no orphan may remain.
func TestForgeRun_DirectDaemonDoubleStart_OnlyOneBinds(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real daemon processes")
	}
	if runtime.GOOS == "windows" {
		t.Skip("process-table signalling is unix-only here")
	}
	f := newRunFixture(t)
	dirs := daemon.WithRoot(f.home)

	// Start daemon A directly in the foreground and wait until it is healthy.
	procA := startRawDaemon(t, f)
	t.Cleanup(func() {
		// Best-effort graceful shutdown of the raw daemon (cross-platform).
		_ = procA.Process.Kill()
		_, _ = procA.Process.Wait()
	})
	if err := waitForHealthFiles(dirs, 15*time.Second); err != nil {
		t.Fatalf("daemon A did not become healthy: %v", err)
	}
	addrA := mustReadFile(t, dirs.AddrFile)
	pidA := mustReadFile(t, dirs.PIDFile)

	// Start daemon B directly. It must detect A as healthy and exit on its own
	// without binding, within a short window.
	procB := startRawDaemon(t, f)
	doneB := make(chan error, 1)
	go func() { doneB <- procB.Wait() }()
	select {
	case err := <-doneB:
		// B must have exited (it saw A healthy and bowed out).
		if err != nil {
			t.Fatalf("daemon B exited with error: %v", err)
		}
	case <-time.After(15 * time.Second):
		// B is still running — that is only acceptable if it never became the
		// owner (it should have exited). Treat as a failure.
		_ = procB.Process.Kill()
		_, _ = procB.Process.Wait()
		t.Fatalf("daemon B did not bow out within 15s — daemon-side single-instance guard broken")
	}

	// The winner's runtime files must be intact: B did NOT overwrite them and
	// did NOT delete them.
	if got := mustReadFile(t, dirs.AddrFile); got != addrA {
		t.Fatalf("addr file changed after loser start: %q -> %q (loser clobbered winner)", addrA, got)
	}
	if got := mustReadFile(t, dirs.PIDFile); got != pidA {
		t.Fatalf("pid file changed after loser start: %q -> %q (loser clobbered winner)", pidA, got)
	}

	// Exactly one daemon process for this binary must be alive (the winner A).
	if n := daemonProcCount(f.bin); n != 1 {
		t.Fatalf("after loser bow-out, %d daemon processes alive (want exactly 1)", n)
	}

	// A must still be healthy and serving.
	if got := daemonStatus(f); got != "running" {
		t.Fatalf("daemon A state = %q after loser start, want running", got)
	}
}

// startRawDaemon launches `<bin> daemon run` as a detached foreground daemon for
// the fixture's home and returns the started command (caller arranges shutdown).
func startRawDaemon(t *testing.T, f *runFixture) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(f.bin, "daemon", "run")
	cmd.Env = append(os.Environ(), "NEUROFORGE_HOME="+f.home)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start raw daemon: %v", err)
	}
	return cmd
}

// waitForHealthFiles polls until the runtime files exist AND the daemon answers
// /health successfully (so a winner is genuinely the owner, not mid-bind).
func waitForHealthFiles(dirs daemon.Dirs, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := daemon.GetStatus(context.Background(), dirs)
		if st.State == "running" {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errTimeoutStr("daemon never became running")
}

type errTimeoutStr string

func (e errTimeoutStr) Error() string { return string(e) }

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}

// ---- helpers ----

type errDualDaemon string

func (e errDualDaemon) Error() string { return string(e) }

func readPIDFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func pidAlive(pid int) bool {
	// Sending signal 0 probes process existence without affecting it.
	if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err != nil {
		return false
	}
	return true
}

// daemonProcCount returns the number of live detached daemon child processes
// (`<bin> daemon run`) for the test binary. It is used to sample the OS process
// table during a concurrent cold-start burst so a transient second daemon that
// dies before the next PID-file poll cannot hide (BF-F-01 / T1).
//
// On non-unix platforms there is no portable pgrep, so the function returns 0
// (process-table sampling is disabled; the per-home PID-file sampler remains
// the authoritative check).
func daemonProcCount(bin string) int {
	if runtime.GOOS == "windows" {
		return 0
	}
	// pgrep -f matches the full command line. The daemon child is launched as
	// `<bin> daemon run`; quoting bin makes the pattern unambiguous for temp
	// paths containing spaces.
	out, err := exec.Command("pgrep", "-f", bin+" daemon run").Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			n++
		}
	}
	return n
}

func dumpDaemonArtifacts(t *testing.T, dirs daemon.Dirs) {
	t.Helper()
	log, err := os.ReadFile(dirs.LogFile)
	if err != nil {
		t.Logf("daemon log: %v", err)
		return
	}
	// Count authoritative spawn events ("--- daemon starting ---") and the set
	// of daemon pids that appear in the log. Two distinct pids here proves a
	// real dual-spawn (not a measurement artifact).
	starts := strings.Count(string(log), "daemon starting")
	t.Logf("daemon log: %d 'daemon starting' events", starts)
	t.Logf("daemon log tail:\n%s", tailN(string(log), 2000))
	if pid, err := os.ReadFile(dirs.PIDFile); err == nil {
		t.Logf("pid file: %s", string(pid))
	}
	if addr, err := os.ReadFile(dirs.AddrFile); err == nil {
		t.Logf("addr file: %s", string(addr))
	}
}

func tailN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
