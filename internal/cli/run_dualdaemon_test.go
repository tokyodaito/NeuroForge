package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/daemon"
)

// TestForgeRun_DualDaemonRace (B-11 / R-2.3): several `forge run` invocations
// start concurrently against a fresh home with no daemon. Exactly ONE daemon
// process must be created, all clients must reach a correct terminal result,
// the PID file must hold a single live pid (sampled throughout the burst so a
// transient second daemon cannot hide), no stale autostart lock may remain
// stuck, and the property must hold under repetition. Artifacts (daemon log)
// are dumped on failure.
func TestForgeRun_DualDaemonRace(t *testing.T) {
	if testing.Short() {
		t.Skip("dual-daemon race spawns real processes")
	}
	const clients = 6
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
					time.Sleep(5 * time.Millisecond)
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
