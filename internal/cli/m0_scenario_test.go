package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/daemon"
)

// TestM0_DemonstrableScenario is the automated end-to-end proof for milestone
// M0 (§36.20). It runs entirely in an isolated temp dir: no network, no real AI
// providers, no user directories touched. It is part of `make check`.
//
// Steps: config -> start -> health/status -> loopback bind -> API auth ->
// audit write (daemon lifecycle) -> audit read -> stop -> restart -> durable
// state preserved -> no second daemon -> clean exit.
func TestM0_DemonstrableScenario(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)
	dirs := daemon.WithRoot(home)

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("M0 scenario FAILED at step %q: %s", name, detail)
		}
		t.Logf("M0 scenario step ok: %s", name)
	}

	// 1. Configuration is the isolated NEUROFORGE_HOME (temp dir).
	step("1.config", dirs.Root != "", "no temp home")

	// 2. Start the daemon.
	out, _, code := runForge(t, bin, home, "daemon", "start")
	step("2.start", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 3. Health/status -> running.
	st := daemonStatusJSON(t, bin, home)
	step("3.status-running", st["state"] == "running", fmt.Sprintf("state=%v", st["state"]))

	// 4. Loopback binding.
	addr := fmt.Sprintf("%v", st["addr"])
	step("4.loopback", strings.HasPrefix(addr, "http://127.0.0.1:"), "addr="+addr)

	// 5. Local API authentication: no token -> 401, with token -> 200.
	noTokenStatus := httpStatus(t, dirs, "/healthz", false)
	step("5a.auth-no-token-rejected", noTokenStatus == http.StatusUnauthorized,
		fmt.Sprintf("no-token status=%d want 401", noTokenStatus))
	withTokenStatus := httpStatus(t, dirs, "/healthz", true)
	step("5b.auth-token-accepted", withTokenStatus == http.StatusOK,
		fmt.Sprintf("token status=%d want 200", withTokenStatus))

	// 6 + 7. Audit write (daemon lifecycle recorded daemon.started) and read
	// (GET /audit returns it).
	entries := auditEntries(t, dirs)
	hasStarted := false
	for _, e := range entries {
		if e["type"] == "daemon.started" {
			hasStarted = true
		}
	}
	step("6.audit-write", hasStarted, "daemon.started not in audit")
	step("7.audit-read", len(entries) > 0, "audit empty")

	// 8. Stop the daemon.
	out, _, code = runForge(t, bin, home, "daemon", "stop")
	step("8.stop", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 9. Restart the daemon.
	out, _, code = runForge(t, bin, home, "daemon", "start")
	step("9.restart", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 10. Durable state preserved across restart: the DB exists and the audit
	// holds events from BOTH runs (two daemon.started entries).
	_, err := os.Stat(filepath.Join(home, "state.db"))
	step("10a.durable-db", err == nil, fmt.Sprintf("state.db: %v", err))
	entries = auditEntries(t, dirs)
	startedCount := 0
	for _, e := range entries {
		if e["type"] == "daemon.started" {
			startedCount++
		}
	}
	step("10b.durable-audit", startedCount >= 2,
		fmt.Sprintf("daemon.started appears %d times, want >=2 (durable across restart)", startedCount))

	// 11. No second daemon: start while running is idempotent (same pid).
	pidBefore := toInt(daemonStatusJSON(t, bin, home)["pid"])
	out, _, code = runForge(t, bin, home, "daemon", "start")
	pidAfter := toInt(daemonStatusJSON(t, bin, home)["pid"])
	step("11.no-second-daemon",
		code == ExitOK && strings.Contains(out, "already running") && pidBefore == pidAfter,
		fmt.Sprintf("code=%d pid %d->%d out=%s", code, pidBefore, pidAfter, out))

	// 12. Clean exit.
	out, _, code = runForge(t, bin, home, "daemon", "stop")
	step("12.clean-exit", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	// And the daemon is actually gone.
	_, _, sc := runForge(t, bin, home, "daemon", "status")
	step("12b.gone", sc == ExitErr, fmt.Sprintf("status exit=%d want 1 (not running)", sc))
}

// httpStatus performs a GET against the daemon API, optionally authenticated.
func httpStatus(t *testing.T, dirs daemon.Dirs, path string, withToken bool) int {
	t.Helper()
	addr, err := daemon.ReadAddr(dirs)
	if err != nil || addr == "" {
		t.Fatalf("read addr: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), "GET", addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withToken {
		tok, err := daemon.ReadToken(dirs)
		if err != nil || tok == "" {
			t.Fatalf("read token: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http %s: %v", path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// auditEntries reads the audit history through the daemon /audit endpoint.
func auditEntries(t *testing.T, dirs daemon.Dirs) []map[string]any {
	t.Helper()
	addr, err := daemon.ReadAddr(dirs)
	if err != nil || addr == "" {
		t.Fatalf("read addr: %v", err)
	}
	tok, err := daemon.ReadToken(dirs)
	if err != nil || tok == "" {
		t.Fatalf("read token: %v", err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", addr+"/audit?limit=200", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("audit get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit status=%d body=%s", resp.StatusCode, body)
	}
	var out []map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(body), &out); err != nil {
		t.Fatalf("audit decode: %v body=%s", err, body)
	}
	return out
}
