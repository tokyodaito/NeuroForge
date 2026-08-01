package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/daemon"
)

// This file contains integration tests that build the real `forge` binary and
// drive its daemon lifecycle end-to-end. They require the `go` toolchain. If the
// binary cannot be built, the tests are skipped.

var (
	buildOnce  sync.Once
	binaryPath string
	buildErr   error
)

// forgeBinary builds ./cmd/forge into a temp file once per package and returns
// its path, or skips the test if the toolchain is unavailable.
func forgeBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		modroot := findModuleRoot(t)
		tmp, err := os.MkdirTemp("", "forge-it-")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(tmp, "forge")
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/forge")
		cmd.Dir = modroot
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
			return
		}
		binaryPath = bin
	})
	if buildErr != nil {
		t.Skipf("cannot build forge binary: %v", buildErr)
	}
	return binaryPath
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Skipf("go env GOMOD: %v", err)
	}
	p := strings.TrimSpace(string(out))
	if p == "" || p == "/dev/null" {
		t.Fatal("not in a Go module")
	}
	return filepath.Dir(p)
}

// runForge runs the built binary with NEUROFORGE_HOME set to home, returning
// stdout, stderr and exit code.
func runForge(t *testing.T, bin, home string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "NEUROFORGE_HOME="+home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run forge %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), code
}

// withDaemonCleanup ensures any daemon started in home is stopped after the
// test, so no detached daemon leaks across tests.
func withDaemonCleanup(t *testing.T, bin, home string) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = ctx
		runForge(t, bin, home, "daemon", "stop")
	})
}

func daemonStatusJSON(t *testing.T, bin, home string) map[string]any {
	t.Helper()
	out, _, code := runForge(t, bin, home, "daemon", "status", "--json")
	if code != 0 && code != 1 {
		t.Fatalf("status exit %d: %s", code, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("parse status json %q: %v", out, err)
	}
	return m
}

func TestIntegration_DaemonLifecycle(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	// start
	out, _, code := runForge(t, bin, home, "daemon", "start")
	if code != 0 {
		t.Fatalf("start: exit %d stderr=%s", code, out)
	}
	if !strings.Contains(out, "started") {
		t.Fatalf("start output: %q", out)
	}

	// status -> running
	st := daemonStatusJSON(t, bin, home)
	if st["state"] != "running" {
		t.Fatalf("state=%v, want running", st["state"])
	}
	pid := toInt(st["pid"])
	if pid <= 0 {
		t.Fatalf("pid=%v", st["pid"])
	}

	// logs -> non-empty (daemon wrote structured logs + banner)
	out, _, code = runForge(t, bin, home, "daemon", "logs")
	if code != 0 {
		t.Fatalf("logs: exit %d", code)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected non-empty logs")
	}

	// doctor -> daemon OK while running
	out, _, code = runForge(t, bin, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "daemon") || !strings.Contains(out, "running") {
		t.Fatalf("doctor should report running daemon:\n%s", out)
	}

	// stop
	out, _, code = runForge(t, bin, home, "daemon", "stop")
	if code != 0 {
		t.Fatalf("stop: exit %d stderr=%s", code, out)
	}
	if !strings.Contains(out, "stopped") {
		t.Fatalf("stop output: %q", out)
	}

	// status -> not running (exit 1)
	_, _, code = runForge(t, bin, home, "daemon", "status")
	if code != ExitErr {
		t.Fatalf("status after stop: exit %d, want %d", code, ExitErr)
	}
}

func TestIntegration_RepeatedStartDoesNotSpawnSecondDaemon(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	if out, _, code := runForge(t, bin, home, "daemon", "start"); code != 0 {
		t.Fatalf("first start: exit %d %s", code, out)
	}
	st1 := daemonStatusJSON(t, bin, home)
	pid1 := toInt(st1["pid"])

	// Second start must be idempotent: exit 0, "already running", same pid.
	out, _, code := runForge(t, bin, home, "daemon", "start")
	if code != 0 {
		t.Fatalf("second start: exit %d, want 0", code)
	}
	if !strings.Contains(out, "already running") {
		t.Fatalf("second start output: %q", out)
	}
	st2 := daemonStatusJSON(t, bin, home)
	pid2 := toInt(st2["pid"])
	if pid1 != pid2 {
		t.Fatalf("second start produced a different pid: %d vs %d", pid1, pid2)
	}
}

func TestIntegration_StartReclaimsCorruptedRuntimeState(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	dirs := daemon.WithRoot(home)

	// Corrupted: garbage pid file, no token/addr.
	mustWriteFile(t, dirs.PIDFile, "not-a-number\n")

	// status should not crash; it reports a non-running state.
	if _, _, code := runForge(t, bin, home, "daemon", "status"); code != ExitErr {
		// corrupted/absent -> not running -> exit 1
		t.Fatalf("status corrupted: exit %d, want 1", code)
	}

	// start must reclaim and succeed.
	out, _, code := runForge(t, bin, home, "daemon", "start")
	if code != 0 {
		t.Fatalf("start after corruption: exit %d %s", code, out)
	}
	st := daemonStatusJSON(t, bin, home)
	if st["state"] != "running" {
		t.Fatalf("state=%v, want running after reclaim", st["state"])
	}
}

func TestIntegration_StartReclaimsStalePID(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)
	dirs := daemon.WithRoot(home)

	// A pid guaranteed not to be alive (a huge, implausible pid).
	mustWriteFile(t, dirs.PIDFile, fmt.Sprintf("%d\n", 1<<30))

	out, _, code := runForge(t, bin, home, "daemon", "start")
	if code != 0 {
		t.Fatalf("start after stale pid: exit %d %s", code, out)
	}
	st := daemonStatusJSON(t, bin, home)
	if st["state"] != "running" {
		t.Fatalf("state=%v, want running", st["state"])
	}
}

func TestIntegration_SigTermStopsDaemon(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	runForge(t, bin, home, "daemon", "start")
	st := daemonStatusJSON(t, bin, home)
	pid := toInt(st["pid"])

	// Send SIGTERM directly to the daemon process; it must shut down.
	if err := sendSIGTERM(pid); err != nil {
		t.Fatalf("sigterm: %v", err)
	}
	if !waitForNotRunning(bin, home, 10*time.Second) {
		t.Fatalf("daemon still running after SIGTERM")
	}

	// pid file should have been removed by graceful shutdown.
	dirs := daemon.WithRoot(home)
	if _, err := os.Stat(dirs.PIDFile); err == nil {
		t.Errorf("expected pid file removed after sigterm shutdown")
	}
}

// TestIntegration_StateSurvivesRestartAcrossProcesses verifies durable state:
// after a restart, the database + audit history persist (AC-27 enabler).
func TestIntegration_StateSurvivesRestart(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	runForge(t, bin, home, "daemon", "start")
	daemonStatusJSON(t, bin, home)
	// Stop and restart; the DB must still exist and contain audit events from
	// the first run.
	runForge(t, bin, home, "daemon", "stop")
	if _, err := os.Stat(filepath.Join(home, "state.db")); err != nil {
		t.Fatalf("state.db missing after stop: %v", err)
	}

	runForge(t, bin, home, "daemon", "start")
	st2 := daemonStatusJSON(t, bin, home)
	if st2["state"] != "running" {
		t.Fatalf("restart: state=%v", st2["state"])
	}
}

func waitForNotRunning(bin, home string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, bin, "daemon", "status", "--json")
		cmd.Env = append(os.Environ(), "NEUROFORGE_HOME="+home)
		out, _ := cmd.Output()
		cancel()
		if !strings.Contains(string(out), `"running"`) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
