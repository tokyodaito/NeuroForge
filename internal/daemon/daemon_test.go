package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/storage"
	"neuroforge/internal/transport"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// runInBackground starts a daemon Run in a goroutine and registers a cleanup
// that cancels and waits for it. The returned stop function is idempotent.
func runInBackground(t *testing.T) (Dirs, func()) {
	t.Helper()
	dirs := WithRoot(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error

	go func() {
		runErr = Run(ctx, RunConfig{Dirs: dirs, Logger: quietLogger()})
		close(done)
	}()

	if err := waitForHealthy(dirs, 5*time.Second); err != nil {
		cancel()
		<-done
		t.Fatalf("daemon not healthy: %v", err)
	}

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Fatalf("daemon Run did not stop within timeout (runErr=%v)", runErr)
			}
		})
	}
	t.Cleanup(stop)
	return dirs, func() { <-done }
}

func waitForHealthy(dirs Dirs, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if isReachableAndHealthy(context.Background(), dirs) {
			return nil
		}
		if time.Now().After(deadline) {
			return errTimeout{}
		}
		time.Sleep(40 * time.Millisecond)
	}
}

type errTimeout struct{}

func (errTimeout) Error() string { return "timed out" }

func TestRun_BindsLoopbackAndServesHealth(t *testing.T) {
	dirs, _ := runInBackground(t)

	st := GetStatus(context.Background(), dirs)
	if st.State != StatusRunning {
		t.Fatalf("state = %s, want running (note: %s)", st.State, st.Note)
	}
	if st.Health == nil || st.Health.Status != "ok" {
		t.Fatalf("health = %+v", st.Health)
	}
	if st.PID <= 0 {
		t.Fatalf("pid = %d", st.PID)
	}
	if st.Addr == "" || !contains(st.Addr, "127.0.0.1") {
		t.Fatalf("addr not loopback: %q", st.Addr)
	}
}

func TestRun_RuntimeFilesArePrivate(t *testing.T) {
	dirs, _ := runInBackground(t)

	for _, p := range []string{dirs.PIDFile, dirs.TokenFile, dirs.AddrFile} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if runtime.GOOS == "windows" {
			// Windows has no Unix permission model: os.WriteFile/Chmod with
			// 0o600 are a no-op and files are created with the default DACL,
			// so Mode().Perm() reports 0o666 regardless. We deliberately do
			// NOT claim a 0o600 guarantee on Windows (no false security claim,
			// rule §36.25). Privacy on Windows is enforced by the owning
			// directory being owner-only (~/.neuroforge/run, created 0o700),
			// which the user profile ACL keeps private. The 0o600 assertion is
			// retained on POSIX, where it is meaningful and enforced.
			continue
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 600", p, mode)
		}
	}
	tok, err := readToken(dirs)
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	if len(tok) < 32 {
		t.Fatalf("token too short: %d", len(tok))
	}
}

func TestRun_GracefulShutdownOnCancel(t *testing.T) {
	dirs, _ := runInBackground(t)
	if st := GetStatus(context.Background(), dirs); st.State != StatusRunning {
		t.Fatalf("not running: %s", st.State)
	}
	// t.Cleanup cancels the context and waits for Run to exit; if Run hangs,
	// the cleanup fails the test with a timeout.
}

func TestRun_AuditStartedPersistedWhileRunning(t *testing.T) {
	dirs, _ := runInBackground(t)
	time.Sleep(80 * time.Millisecond) // allow daemon.started to flush

	db, err := storage.Open(context.Background(), filepath.Join(dirs.Root, "state.db"), &storage.Options{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	rows, err := db.ListAuditEvents(context.Background(), storage.AuditFilter{ScopeID: "global"})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	types := map[string]bool{}
	for _, r := range rows {
		types[r.Type] = true
	}
	for _, want := range []string{"daemon.starting", "daemon.started"} {
		if !types[want] {
			t.Errorf("missing audit event %q while running; have %v", want, keysOf(types))
		}
	}
}

// TestRun_APIShutdownRecordsStopped drives shutdown through the loopback API
// (POST /shutdown), which is the graceful path used by `forge daemon stop`
// against a separate process. We do NOT call daemon.Stop() here because in this
// in-process harness the daemon shares the test process PID.
func TestRun_APIShutdownRecordsStopped(t *testing.T) {
	dirs, wait := runInBackground(t)

	addr, _ := readAddr(dirs)
	token, _ := readToken(dirs)
	cli := transport.NewClient(addr, token)
	if err := cli.Shutdown(context.Background()); err != nil {
		t.Fatalf("api shutdown: %v", err)
	}
	// Wait for the daemon Run goroutine to fully exit, so the daemon.stopped
	// audit event has been written and the DB flushed.
	wait()

	db, err := storage.Open(context.Background(), filepath.Join(dirs.Root, "state.db"), &storage.Options{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	rows, err := db.ListAuditEvents(context.Background(), storage.AuditFilter{ScopeID: "global"})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	types := map[string]bool{}
	for _, r := range rows {
		types[r.Type] = true
	}
	if !types["daemon.stopped"] {
		t.Errorf("missing daemon.stopped after shutdown; have %v", keysOf(types))
	}
}

func TestRun_TokenIsRandomPerRun(t *testing.T) {
	d1, _ := runInBackground(t)
	tok1, _ := readToken(d1)

	d2, _ := runInBackground(t)
	tok2, _ := readToken(d2)

	if tok1 == "" || tok2 == "" {
		t.Fatal("empty token")
	}
	if tok1 == tok2 {
		t.Fatal("tokens must differ across daemon runs")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
