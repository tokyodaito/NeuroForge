package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/transport"
)

// This file is the compiled-binary black-box evidence layer for M14-03
// (Task Compiler production API, CLI and restart flow). Every assertion goes
// through the real `forge` binary driving the real daemon against an isolated
// NEUROFORGE_HOME — no in-memory handles, no test doubles.
//
// Scenario (mandatory task brief):
//
//	create task -> compile (spec save) -> show -> lock -> daemon restart -> show
//
// Plus the required negative cases:
//
//	invalid task (spec save / show on a missing task),
//	locked update (locked spec cannot be mutated through spec save),
//	duplicate request (idempotent re-save returns the same version).
func TestSpecSave_BlackBox_CreateCompileShowLockRestart(t *testing.T) {
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
			t.Fatalf("M14-03 scenario FAILED at step %q: %s", name, detail)
		}
		t.Logf("M14-03 scenario ok: %s", name)
	}

	// 1. Start daemon.
	out, _, code := runForge(t, bin, home, "daemon", "start")
	step("1.daemon-start", code == ExitOK && strings.Contains(out, "started"),
		fmt.Sprintf("exit=%d out=%s", code, out))
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon did not reach running state")
	}

	// 2. Add a project (the production registry path).
	out, _, code = runForge(t, bin, home, "project", "add", "--json", repoPath)
	step("2.project-add", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var proj transport.ProjectDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &proj); err != nil {
		t.Fatalf("decode project json: %v\n%s", err, out)
	}
	projectID := proj.ID
	step("2b.project-id", projectID != "", "no project id")

	// 3. Add a task with explicit Objective + dash-list ACs so the compiler
	//    emits a HIGH-confidence spec with stable AC IDs.
	desc := "Objective: Persist compiled specification across daemon restart.\n" +
		"Acceptance Criteria:\n" +
		"- Spec is durable in SQLite.\n" +
		"- Lock state survives restart."
	out, _, code = runForge(t, bin, home, "task", "add", "-p", projectID, "--json", desc)
	step("3.task-add", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var taskDTO transport.TaskDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &taskDTO); err != nil {
		t.Fatalf("decode task json: %v\n%s", err, out)
	}
	taskID := taskDTO.ID
	step("3b.task-id", taskID != "", "no task id")

	// 4. Compile-and-save through the daemon (`forge spec save`).
	out, _, code = runForge(t, bin, home, "spec", "save", "-t", taskID, "--by", "alice", "--json")
	step("4.spec-save", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var saved struct {
		Specification transport.SpecificationDTO `json:"specification"`
		Confidence    string                     `json:"confidence"`
		Created       bool                       `json:"created"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &saved); err != nil {
		t.Fatalf("decode spec save json: %v\n%s", err, out)
	}
	step("4b.spec-save-created-flag", saved.Created,
		fmt.Sprintf("Created=%v want true", saved.Created))
	step("4c.spec-save-version-1", saved.Specification.Version == 1,
		fmt.Sprintf("Version=%d want 1", saved.Specification.Version))
	step("4d.spec-save-taskid", saved.Specification.TaskID == taskID,
		fmt.Sprintf("TaskID=%q want %q", saved.Specification.TaskID, taskID))
	step("4e.spec-save-objective-nonempty", saved.Specification.Objective != "",
		"empty objective; compiler did not run")
	step("4f.spec-save-ac-count", len(saved.Specification.AcceptanceCriteria) >= 2,
		fmt.Sprintf("AC count=%d want >=2", len(saved.Specification.AcceptanceCriteria)))
	step("4g.spec-save-unlocked", !saved.Specification.Locked,
		"fresh spec must not be locked")
	wantObjective := saved.Specification.Objective
	wantAC1ID := saved.Specification.AcceptanceCriteria[0].ID
	wantAC1Stmt := saved.Specification.AcceptanceCriteria[0].Statement

	// 5. Show latest via the daemon (`forge spec show`).
	out, _, code = runForge(t, bin, home, "spec", "show", "-t", taskID, "--json")
	step("5.spec-show-latest", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var shown transport.SpecificationDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &shown); err != nil {
		t.Fatalf("decode spec show json: %v\n%s", err, out)
	}
	step("5b.spec-show-version", shown.Version == 1,
		fmt.Sprintf("Version=%d want 1", shown.Version))
	step("5c.spec-show-objective", shown.Objective == wantObjective,
		fmt.Sprintf("Objective=%q want %q", shown.Objective, wantObjective))
	step("5d.spec-show-ac1", shown.AcceptanceCriteria[0].ID == wantAC1ID &&
		shown.AcceptanceCriteria[0].Statement == wantAC1Stmt,
		"AC-1 differs from the saved spec")

	// 5e. Show specific version (-v 1).
	out, _, code = runForge(t, bin, home, "spec", "show", "-t", taskID, "-v", "1", "--json")
	step("5f.spec-show-v1", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 6. Duplicate request: spec save again — idempotent, Created=false.
	out, _, code = runForge(t, bin, home, "spec", "save", "-t", taskID, "--json")
	step("6.spec-save-idempotent", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var saved2 struct {
		Specification transport.SpecificationDTO `json:"specification"`
		Created       bool                       `json:"created"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &saved2); err != nil {
		t.Fatalf("decode spec save #2 json: %v\n%s", err, out)
	}
	step("6b.spec-save-idempotent-created-false", !saved2.Created,
		"second spec save must NOT create a new version (idempotent)")
	step("6c.spec-save-idempotent-same-version", saved2.Specification.Version == 1,
		fmt.Sprintf("Version=%d want 1 (idempotent)", saved2.Specification.Version))

	// 7. Lock v1.
	out, _, code = runForge(t, bin, home, "spec", "lock", "-t", taskID, "-v", "1", "--by", "alice", "--json")
	step("7.spec-lock", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var locked transport.SpecificationDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &locked); err != nil {
		t.Fatalf("decode spec lock json: %v\n%s", err, out)
	}
	step("7b.spec-lock-locked", locked.Locked, "Locked=false want true")
	step("7c.spec-lock-locked-by-alice", locked.LockedBy == "alice",
		fmt.Sprintf("LockedBy=%q want alice", locked.LockedBy))
	step("7d.spec-lock-locked-at-nonempty", locked.LockedAt != "",
		"LockedAt empty; lock did not persist")

	// 8. Versions list.
	out, _, code = runForge(t, bin, home, "spec", "versions", "-t", taskID, "--json")
	step("8.spec-versions", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var versions []int
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &versions); err != nil {
		t.Fatalf("decode spec versions json: %v\n%s", err, out)
	}
	step("8b.spec-versions-one", len(versions) == 1 && versions[0] == 1,
		fmt.Sprintf("versions=%v want [1]", versions))

	// 9. Daemon restart: stop, then start again. State must persist.
	out, _, code = runForge(t, bin, home, "daemon", "stop")
	step("9.daemon-stop", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	if !waitForDaemonStopped(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon did not stop")
	}
	out, _, code = runForge(t, bin, home, "daemon", "start")
	step("9b.daemon-restart", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon did not restart")
	}

	// 10. Show after restart: same TaskID + Version + Objective + AC IDs +
	//     Locked + LockedBy. This is the headline restart-persistence proof.
	out, _, code = runForge(t, bin, home, "spec", "show", "-t", taskID, "--json")
	step("10.spec-show-after-restart", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var after transport.SpecificationDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &after); err != nil {
		t.Fatalf("decode spec show after restart: %v\n%s", err, out)
	}
	step("10b.spec-after-restart-version", after.Version == 1,
		fmt.Sprintf("Version=%d want 1", after.Version))
	step("10c.spec-after-restart-objective", after.Objective == wantObjective,
		fmt.Sprintf("Objective=%q want %q", after.Objective, wantObjective))
	step("10d.spec-after-restart-ac1-id", after.AcceptanceCriteria[0].ID == wantAC1ID,
		fmt.Sprintf("AC ID=%q want %q (AC IDs must be stable across restart)",
			after.AcceptanceCriteria[0].ID, wantAC1ID))
	step("10e.spec-after-restart-ac1-stmt", after.AcceptanceCriteria[0].Statement == wantAC1Stmt,
		"AC statement differs after restart")
	step("10f.spec-after-restart-locked", after.Locked,
		"lock state did NOT survive restart")
	step("10g.spec-after-restart-locked-by", after.LockedBy == "alice",
		fmt.Sprintf("LockedBy=%q want alice (provenance did not survive)", after.LockedBy))

	// 11. Versions list after restart.
	out, _, code = runForge(t, bin, home, "spec", "versions", "-t", taskID, "--json")
	step("11.spec-versions-after-restart", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &versions); err != nil {
		t.Fatalf("decode spec versions json after restart: %v\n%s", err, out)
	}
	step("11b.spec-versions-after-restart-one", len(versions) == 1 && versions[0] == 1,
		fmt.Sprintf("versions=%v want [1]", versions))

	// 12. Idempotent compile after restart: locked content matches, must not
	//     mint a new version.
	out, _, code = runForge(t, bin, home, "spec", "save", "-t", taskID, "--json")
	step("12.spec-save-after-restart", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var saved3 struct {
		Specification transport.SpecificationDTO `json:"specification"`
		Created       bool                       `json:"created"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &saved3); err != nil {
		t.Fatalf("decode spec save after restart: %v\n%s", err, out)
	}
	step("12b.spec-save-after-restart-idempotent", !saved3.Created && saved3.Specification.Version == 1,
		fmt.Sprintf("Created=%v Version=%d, want false/1", saved3.Created, saved3.Specification.Version))
}

// TestSpecSave_BlackBox_InvalidTask proves spec save / show / lock on a
// missing task produce a non-zero exit and a clear "not found" error message
// (the "invalid task" mandatory case).
func TestSpecSave_BlackBox_InvalidTask(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	if out, _, code := runForge(t, bin, home, "daemon", "start"); code != ExitOK {
		t.Fatalf("daemon start: exit %d out=%s", code, out)
	}
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon not running")
	}

	// spec save on missing task → exit 1, stderr mentions "not found".
	_, stderr, code := runForge(t, bin, home, "spec", "save", "-t", "no-such-project-9999")
	if code != ExitErr {
		t.Errorf("spec save missing task: exit=%d, want %d", code, ExitErr)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("spec save missing task stderr=%q, want 'not found'", stderr)
	}

	// spec show on missing task → exit 1, "not found".
	_, stderr, code = runForge(t, bin, home, "spec", "show", "-t", "no-such-project-9999")
	if code != ExitErr {
		t.Errorf("spec show missing task: exit=%d, want %d", code, ExitErr)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("spec show missing task stderr=%q, want 'not found'", stderr)
	}

	// spec lock on missing version → exit 1, "not found".
	_, stderr, code = runForge(t, bin, home, "spec", "lock", "-t", "no-such-project-9999", "-v", "1")
	if code != ExitErr {
		t.Errorf("spec lock missing task: exit=%d, want %d", code, ExitErr)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("spec lock missing task stderr=%q, want 'not found'", stderr)
	}

	// spec versions on missing task → currently returns empty list, exit 0
	// (no rows is not an error for a list endpoint). Pin this behaviour.
	out, _, code := runForge(t, bin, home, "spec", "versions", "-t", "no-such-project-9999", "--json")
	if code != ExitOK {
		t.Errorf("spec versions missing task: exit=%d, want 0 (empty list, not error)", code)
	}
	if trimmed := strings.TrimSpace(out); trimmed != "[]" && trimmed != "null" {
		// either form is acceptable; assert at least it's not a non-empty list.
		var vs []int
		if err := json.Unmarshal([]byte(trimmed), &vs); err != nil {
			t.Fatalf("decode versions: %v\n%s", err, out)
		}
		if len(vs) != 0 {
			t.Errorf("spec versions missing task=%v, want empty", vs)
		}
	}
}

// TestSpecSave_BlackBox_LockedSpecNoNewVersion proves the "locked update"
// case at the binary level: when the latest spec is locked, calling
// `forge spec save` on the same task (whose compiled content matches the
// locked version) does NOT mint a new version — the locked snapshot is
// preserved. This is the duplicate-request / locked-update negative case.
func TestSpecSave_BlackBox_LockedSpecNoNewVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)
	repoPath := makeTestGitRepo(t)

	if out, _, code := runForge(t, bin, home, "daemon", "start"); code != ExitOK {
		t.Fatalf("daemon start: exit %d out=%s", code, out)
	}
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon not running")
	}

	out, _, code := runForge(t, bin, home, "project", "add", "--json", repoPath)
	if code != ExitOK {
		t.Fatalf("project add: exit %d out=%s", code, out)
	}
	var proj transport.ProjectDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &proj); err != nil {
		t.Fatalf("decode project: %v\n%s", err, out)
	}

	out, _, code = runForge(t, bin, home, "task", "add", "-p", proj.ID, "--json",
		"Objective: locked update case.\nAcceptance Criteria:\n- invariant holds.")
	if code != ExitOK {
		t.Fatalf("task add: exit %d out=%s", code, out)
	}
	var taskDTO transport.TaskDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &taskDTO); err != nil {
		t.Fatalf("decode task: %v\n%s", err, out)
	}
	taskID := taskDTO.ID

	// Save → v1.
	if out, _, code := runForge(t, bin, home, "spec", "save", "-t", taskID, "--json"); code != ExitOK {
		t.Fatalf("spec save: exit %d out=%s", code, out)
	}
	// Lock v1.
	if out, _, code := runForge(t, bin, home, "spec", "lock", "-t", taskID, "-v", "1", "--json"); code != ExitOK {
		t.Fatalf("spec lock: exit %d out=%s", code, out)
	}

	// Save again (locked content matches) → MUST NOT mint v2.
	out, _, code = runForge(t, bin, home, "spec", "save", "-t", taskID, "--json")
	if code != ExitOK {
		t.Fatalf("spec save after lock: exit %d out=%s", code, out)
	}
	var saved struct {
		Specification transport.SpecificationDTO `json:"specification"`
		Created       bool                       `json:"created"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &saved); err != nil {
		t.Fatalf("decode spec save after lock: %v\n%s", err, out)
	}
	if saved.Created {
		t.Errorf("spec save after lock Created=true, want false (idempotent; locked content matches)")
	}
	if saved.Specification.Version != 1 {
		t.Errorf("spec save after lock Version=%d, want 1 (must not mint a new version)", saved.Specification.Version)
	}
	if !saved.Specification.Locked {
		t.Errorf("spec save after lock Locked=false, want true (must reflect locked state)")
	}

	// Versions list → exactly [1]; no v2 was minted.
	out, _, code = runForge(t, bin, home, "spec", "versions", "-t", taskID, "--json")
	if code != ExitOK {
		t.Fatalf("spec versions: exit %d out=%s", code, out)
	}
	var versions []int
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &versions); err != nil {
		t.Fatalf("decode versions: %v\n%s", err, out)
	}
	if len(versions) != 1 || versions[0] != 1 {
		t.Errorf("versions=%v, want [1] (locked spec must not be replaced)", versions)
	}
}

// TestSpecSave_BlackBox_TextOutput proves the default (no --json) output path
// of the daemon-mediated commands is human-readable text containing the
// headline fields (MINOR-1 analogue of M14-02 for the new commands).
func TestSpecSave_BlackBox_TextOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)
	repoPath := makeTestGitRepo(t)

	if out, _, code := runForge(t, bin, home, "daemon", "start"); code != ExitOK {
		t.Fatalf("daemon start: exit %d out=%s", code, out)
	}
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon not running")
	}
	out, _, code := runForge(t, bin, home, "project", "add", "--json", repoPath)
	if code != ExitOK {
		t.Fatalf("project add: exit %d out=%s", code, out)
	}
	var proj transport.ProjectDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &proj); err != nil {
		t.Fatalf("decode project: %v\n%s", err, out)
	}
	out, _, code = runForge(t, bin, home, "task", "add", "-p", proj.ID, "--json",
		"Objective: text output case.\nAcceptance Criteria:\n- readable text.")
	if code != ExitOK {
		t.Fatalf("task add: exit %d out=%s", code, out)
	}
	var taskDTO transport.TaskDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &taskDTO); err != nil {
		t.Fatalf("decode task: %v\n%s", err, out)
	}
	taskID := taskDTO.ID

	// Text mode (no --json).
	out, _, code = runForge(t, bin, home, "spec", "save", "-t", taskID)
	if code != ExitOK {
		t.Fatalf("spec save text: exit %d out=%s", code, out)
	}
	for _, want := range []string{"TaskID:", "Version:", "Objective:", "Risk:", "Complexity:", "Confidence:", "Saved:"} {
		if !strings.Contains(out, want) {
			t.Errorf("spec save text output missing %q\noutput:\n%s", want, out)
		}
	}
	// Must NOT be JSON: no leading "{" on the first non-empty line.
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("spec save text output is JSON, want text:\n%s", out)
	}

	// spec show text mode.
	out, _, code = runForge(t, bin, home, "spec", "show", "-t", taskID)
	if code != ExitOK {
		t.Fatalf("spec show text: exit %d out=%s", code, out)
	}
	for _, want := range []string{"TaskID:", "Version:", "Objective:"} {
		if !strings.Contains(out, want) {
			t.Errorf("spec show text output missing %q\noutput:\n%s", want, out)
		}
	}

	// spec lock text mode.
	out, _, code = runForge(t, bin, home, "spec", "lock", "-t", taskID, "-v", "1")
	if code != ExitOK {
		t.Fatalf("spec lock text: exit %d out=%s", code, out)
	}
	if !strings.Contains(out, "Locked:     true") {
		t.Errorf("spec lock text output missing Locked line:\n%s", out)
	}

	// spec versions text mode.
	out, _, code = runForge(t, bin, home, "spec", "versions", "-t", taskID)
	if code != ExitOK {
		t.Fatalf("spec versions text: exit %d out=%s", code, out)
	}
	if !strings.Contains(out, "v1") {
		t.Errorf("spec versions text output missing 'v1':\n%s", out)
	}
}

// TestSpecSave_BlackBox_FlagValidation proves the CLI flag validation rejects
// bad input at the edge (missing --task, --version <= 0 for lock, unknown
// subcommand).
func TestSpecSave_BlackBox_FlagValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	if out, _, code := runForge(t, bin, home, "daemon", "start"); code != ExitOK {
		t.Fatalf("daemon start: exit %d out=%s", code, out)
	}
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon not running")
	}

	// save without --task → exit 1, no daemon round-trip (rejected at the CLI).
	_, stderr, code := runForge(t, bin, home, "spec", "save")
	if code != ExitErr {
		t.Errorf("spec save without --task: exit=%d, want %d", code, ExitErr)
	}
	if !strings.Contains(stderr, "--task") {
		t.Errorf("spec save without --task stderr=%q, want '--task'", stderr)
	}

	// lock without --version → exit 1.
	_, stderr, code = runForge(t, bin, home, "spec", "lock", "-t", "any")
	if code != ExitErr {
		t.Errorf("spec lock without --version: exit=%d, want %d", code, ExitErr)
	}
	if !strings.Contains(stderr, "--version") {
		t.Errorf("spec lock without --version stderr=%q, want '--version'", stderr)
	}

	// unknown spec subcommand → exit 1.
	_, stderr, code = runForge(t, bin, home, "spec", "bogus")
	if code != ExitErr {
		t.Errorf("spec bogus: exit=%d, want %d", code, ExitErr)
	}
	if !strings.Contains(stderr, "unknown spec subcommand") {
		t.Errorf("spec bogus stderr=%q, want 'unknown spec subcommand'", stderr)
	}
}

// ---- helpers ----

// waitForDaemonStopped polls `forge daemon status --json` until the daemon is
// no longer "running" or the timeout elapses.
func waitForDaemonStopped(t *testing.T, bin, home string, timeout time.Duration) bool {
	t.Helper()
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
