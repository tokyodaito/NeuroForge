package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/daemon"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workgraph"
)

// This file is the compiled-binary black-box evidence layer for M14-05
// (mandatory AC: "Black-box graph show через daemon"). Every assertion goes
// through the real `forge` binary driving the real daemon against an isolated
// NEUROFORGE_HOME — no in-memory handles, no test doubles for the user path.
//
// The dispatch path that calls WorkGraphStore.Save / Scheduler.Claim is a
// later milestone, so the test seeds the durable Work Graph + lease substrate
// through the same WorkGraphStore / LeaseManager the daemon uses (opening a
// second read/write handle to the same WAL DB) and then exercises the
// user-facing CLI: `forge workgraph show -t <id>`.

// TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict proves:
//
//  1. `forge workgraph show -t <id>` reads the durable work graph through the
//     daemon and prints every package + readiness verdict.
//  2. A dependency-not-succeeded reason surfaces in the output.
//  3. A path-lease conflict surfaces with an explainable cause (the path + the
//     holding workspace).
//  4. Daemon restart preserves the graph and leases (mandatory AC).
func TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)
	repoPath := makeTestGitRepo(t)

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("M14-05 scenario FAILED at step %q: %s", name, detail)
		}
		t.Logf("M14-05 scenario ok: %s", name)
	}

	// 1. Start daemon.
	out, _, code := runForge(t, bin, home, "daemon", "start")
	step("1.daemon-start", code == ExitOK && strings.Contains(out, "started"),
		fmt.Sprintf("exit=%d out=%s", code, out))
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon did not reach running state")
	}

	// 2. Add project + task through the production CLI.
	out, _, code = runForge(t, bin, home, "project", "add", "--json", repoPath)
	step("2.project-add", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var proj transport.ProjectDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &proj); err != nil {
		t.Fatalf("decode project json: %v\n%s", err, out)
	}

	desc := "Objective: Persist work graph across restart.\n" +
		"Acceptance Criteria:\n" +
		"- Graph is durable.\n" +
		"- Leases survive restart."
	out, _, code = runForge(t, bin, home, "task", "add", "-p", proj.ID, "--json", desc)
	step("3.task-add", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var taskDTO transport.TaskDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &taskDTO); err != nil {
		t.Fatalf("decode task json: %v\n%s", err, out)
	}
	taskID := taskDTO.ID

	// 4. Compile a spec through the daemon so a Specification is durable.
	out, _, code = runForge(t, bin, home, "spec", "save", "-t", taskID, "--json")
	step("4.spec-save", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 5. Seed the durable Work Graph through the SAME WorkGraphStore the
	//    daemon uses (opening a second handle against the home's DB file).
	//    The dispatch layer that does this internally is a later milestone;
	//    for M14-05 we prove the read path + readiness surface are correct
	//    by seeding the substrate directly.
	dirs := daemon.WithRoot(home)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db2, err := storage.Open(ctx, dirs.StateDB, nil)
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	defer db2.Close()
	specStore := task.NewSpecificationStore(db2, nil, nil)
	spec, err := specStore.GetLatest(ctx, taskID)
	if err != nil {
		t.Fatalf("GetLatest spec: %v", err)
	}
	v, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	// Partition AllowedScope per package (mirrors the dispatch shape).
	{
		g := v.Graph()
		for i := range g.Packages {
			g.Packages[i].AllowedScope = []string{"src/pkg" + fmt.Sprint(i) + "/"}
		}
		v2, verr := workgraph.ValidateWorkGraph(g)
		if verr != nil {
			t.Fatalf("ValidateWorkGraph: %v", verr)
		}
		v = v2
	}
	graphStore := workgraph.NewWorkGraphStore(db2, nil, nil)
	if _, err := graphStore.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 6. `forge workgraph show` (text). Must list every package and a
	//    "ready" verdict for the first, "blocked" verdicts for the rest.
	out, _, code = runForge(t, bin, home, "workgraph", "show", "-t", taskID)
	step("6.workgraph-show-text", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	step("6b.show-lists-packages", strings.Contains(out, "Packages:") &&
		strings.Contains(out, taskID), fmt.Sprintf("out=%s", out))
	// First package must be "ready" (no deps); subsequent packages "blocked"
	// (dependency not succeeded).
	step("6c.show-has-ready", strings.Contains(out, "Readiness: ready"),
		fmt.Sprintf("expected at least one 'ready' verdict; out=%s", out))
	step("6d.show-has-blocked", strings.Contains(out, "Readiness: blocked"),
		fmt.Sprintf("expected at least one 'blocked' verdict; out=%s", out))
	step("6e.show-reports-dependency", strings.Contains(out, "not succeeded"),
		fmt.Sprintf("expected dependency-not-succeeded reason; out=%s", out))

	// 7. `forge workgraph show --json` returns machine-readable output.
	out, _, code = runForge(t, bin, home, "workgraph", "show", "-t", taskID, "--json")
	step("7.workgraph-show-json", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var dto transport.WorkGraphDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto); err != nil {
		t.Fatalf("decode workgraph json: %v\n%s", err, out)
	}
	step("7b.dto-taskid", dto.TaskID == taskID,
		fmt.Sprintf("TaskID=%q want %q", dto.TaskID, taskID))
	step("7c.dto-packages", len(dto.Packages) == len(spec.AcceptanceCriteria),
		fmt.Sprintf("packages=%d want %d", len(dto.Packages), len(spec.AcceptanceCriteria)))

	// 8. Acquire a path lease for the FIRST package's scope from a different
	//    workspace, then re-run the show. The verdict must surface the
	//    explainable cause.
	firstPkg := dto.Packages[0]
	if len(firstPkg.AllowedScope) == 0 {
		t.Fatalf("first package has empty AllowedScope; cannot test path-lease conflict")
	}
	lm := workgraph.NewLeaseManager(db2)
	if _, err := lm.AcquirePath(ctx, taskID, "other-ws", firstPkg.AllowedScope[0]); err != nil {
		t.Fatalf("AcquirePath: %v", err)
	}
	out, _, code = runForge(t, bin, home, "workgraph", "show", "-t", taskID, "--json")
	step("8.workgraph-show-after-lease", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var dto2 transport.WorkGraphDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto2); err != nil {
		t.Fatalf("decode workgraph json #2: %v\n%s", err, out)
	}
	if dto2.Packages[0].Readiness == nil || dto2.Packages[0].Readiness.Ready {
		t.Fatalf("first package should now be blocked by lease: %+v", dto2.Packages[0].Readiness)
	}
	{
		gotReason := false
		for _, r := range dto2.Packages[0].Readiness.BlockedReasons {
			if strings.Contains(r, firstPkg.AllowedScope[0]) && strings.Contains(r, "other-ws") {
				gotReason = true
			}
		}
		step("8b.explainable-cause", gotReason,
			fmt.Sprintf("expected reason naming path %q and workspace other-ws; reasons=%v",
				firstPkg.AllowedScope[0], dto2.Packages[0].Readiness.BlockedReasons))
	}
	step("8c.active-leases-exposed", len(dto2.ActiveLeases) > 0,
		"expected ActiveLeases to expose the held path lease")

	// 9. Restart recovery (mandatory AC). Stop the daemon, re-start it, and
	//    re-run `forge workgraph show`. The graph AND the active lease must
	//    survive the restart.
	out, _, code = runForge(t, bin, home, "daemon", "stop")
	step("9.daemon-stop", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	out, _, code = runForge(t, bin, home, "daemon", "start")
	step("9b.daemon-restart", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon did not reach running state after restart")
	}
	out, _, code = runForge(t, bin, home, "workgraph", "show", "-t", taskID, "--json")
	step("9c.workgraph-show-after-restart", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var dto3 transport.WorkGraphDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto3); err != nil {
		t.Fatalf("decode workgraph json #3: %v\n%s", err, out)
	}
	step("9d.graph-survives-restart", len(dto3.Packages) == len(spec.AcceptanceCriteria),
		fmt.Sprintf("packages after restart=%d want %d", len(dto3.Packages), len(spec.AcceptanceCriteria)))
	step("9e.lease-survives-restart", len(dto3.ActiveLeases) > 0,
		fmt.Sprintf("active leases after restart=%d want >0 (other-ws path lease)", len(dto3.ActiveLeases)))
	// The first package must still be reported blocked by the lease.
	if dto3.Packages[0].Readiness == nil || dto3.Packages[0].Readiness.Ready {
		t.Fatalf("first package should still be blocked by lease after restart: %+v", dto3.Packages[0].Readiness)
	}

	// 10. Negative: show on a task with no graph yields a 404 and a clean
	//     error (no internal-error blob).
	out, _, code = runForge(t, bin, home, "workgraph", "show", "-t", "no-such-task", "--json")
	step("10.missing-task-nonzero", code == ExitErr,
		fmt.Sprintf("expected non-zero exit for missing task; got exit=%d", code))
	step("10b.missing-task-mentions-not-found", strings.Contains(strings.ToLower(out), "not found"),
		fmt.Sprintf("expected 'not found' in output; got=%s", out))
}

// TestWorkGraphShow_BlackBox_MigrationV9Applied proves the production daemon
// applies migration v9 (the work_packages / dependencies / attempts tables +
// leases.expires_at) and is observable through the compiled binary. Mirrors
// the M14-01 v8 migration black-box test.
func TestWorkGraphShow_BlackBox_MigrationV9Applied(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box daemon lifecycle test spawns the real daemon")
	}
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	out, _, code := runForge(t, bin, home, "daemon", "start")
	if code != 0 {
		t.Fatalf("daemon start: exit %d out=%s", code, out)
	}
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon did not reach running state")
	}

	// doctor must report schema version >= 9.
	out, _, code = runForge(t, bin, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor: exit %d out=%s", code, out)
	}
	version := parseSchemaVersion(t, out)
	if version < 9 {
		t.Fatalf("schema version %d < 9; migration v9 not applied by daemon", version)
	}

	// The new tables must physically exist in the production DB.
	dirs := daemon.WithRoot(home)
	if err := assertWorkGraphTablesExist(dirs.StateDB); err != nil {
		t.Fatalf("work graph tables missing from production DB: %v", err)
	}

	// Idempotent restart recovery.
	if out, _, code := runForge(t, bin, home, "daemon", "stop"); code != 0 {
		t.Fatalf("daemon stop: exit %d out=%s", code, out)
	}
	if out, _, code := runForge(t, bin, home, "daemon", "start"); code != 0 {
		t.Fatalf("daemon restart: exit %d out=%s", code, out)
	}
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon did not reach running state after restart")
	}
	out, _, code = runForge(t, bin, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor after restart: exit %d out=%s", code, out)
	}
	versionAfter := parseSchemaVersion(t, out)
	if versionAfter != version {
		t.Fatalf("schema version changed on restart: %d -> %d", version, versionAfter)
	}
}

// assertWorkGraphTablesExist opens the production DB read-only through the
// real SQLite driver and confirms the migration-v9 tables + the
// leases.expires_at column + the partial UNIQUE index are present.
func assertWorkGraphTablesExist(dbPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := sql.Open(storage.Driver, dbPath+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	for _, table := range []string{
		"work_packages", "work_package_dependencies", "work_package_attempts",
	} {
		var name string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("table %s not present", table)
		}
		if err != nil {
			return err
		}
	}
	// leases.expires_at must be selectable (column added by migration v9).
	var s string
	err = db.QueryRowContext(ctx, `SELECT expires_at FROM leases LIMIT 0`).Scan(&s)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("leases.expires_at not selectable: %w", err)
	}
	// The active-resource UNIQUE partial index must exist.
	var idxName string
	err = db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`,
		"idx_leases_unique_active_resource").Scan(&idxName)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("idx_leases_unique_active_resource not present")
	}
	if err != nil {
		return err
	}
	return nil
}
