package cli_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/bootstrap"
	"neuroforge/internal/cli"
	"neuroforge/internal/daemon"
)

// newInitApp builds a CLI App wired with fake bootstrap deps so `forge init` is
// fully offline and deterministic (rule §33).
func newInitApp(t *testing.T, installedTools ...string) (*cli.App, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(daemon.EnvHome, home)

	paths := map[string]string{}
	outputs := map[string]string{}
	for _, tool := range installedTools {
		paths[tool] = "/usr/bin/" + tool
		outputs[tool] = tool + " 1.0.0\n"
	}
	paths["brew"] = "/opt/homebrew/bin/brew"
	detector := bootstrap.NewFakeDetector(paths, outputs, "/bin/zsh", home)

	out := &bytes.Buffer{}
	app := cli.New()
	app.Out = out
	app.Err = out
	app.Stdin = strings.NewReader("")
	app.InitDepsForTest(t, detector, out)
	return app, out
}

// TestForgeInitDryRunChangesNothing verifies AC-25: --dry-run shows a plan and
// does NOT touch the filesystem.
func TestForgeInitDryRunChangesNothing(t *testing.T) {
	app, out := newInitApp(t)
	before := snapshotHome(t)

	code := app.Run([]string{"init", "--dry-run", "--profile", "minimal"})
	if code != 0 {
		t.Fatalf("exit code %d; output:\n%s", code, out.String())
	}
	after := snapshotHome(t)
	if before != after {
		t.Errorf("init --dry-run mutated the home directory (AC-25)")
	}
	if !strings.Contains(out.String(), "Dry-run complete") {
		t.Errorf("dry-run output missing completion marker:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "nothing was changed") {
		t.Errorf("dry-run did not state it changed nothing (AC-25)")
	}
}

// TestForgeInitDryRunShowsPlan verifies the plan is rendered (§7.2 stage 3).
func TestForgeInitDryRunShowsPlan(t *testing.T) {
	app, out := newInitApp(t)
	app.Run([]string{"init", "--dry-run", "--profile", "standard", "--json"})
	if !strings.Contains(out.String(), `"profile":"standard"`) {
		t.Errorf("json dry-run missing profile:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "will_install") {
		t.Errorf("json dry-run missing will_install:\n%s", out.String())
	}
}

// TestForgeInitInvalidProfile rejects an unknown profile.
func TestForgeInitInvalidProfile(t *testing.T) {
	app, _ := newInitApp(t)
	if code := app.Run([]string{"init", "--profile", "bogus", "--dry-run"}); code == 0 {
		t.Errorf("invalid profile should exit non-zero")
	}
}

// TestForgeInitDryRunSkipsPresentTools verifies a present tool is not reinstalled
// (§7.2 stage 4 forbids removing/reinstalling existing versions).
func TestForgeInitDryRunSkipsPresentTools(t *testing.T) {
	app, out := newInitApp(t, "git")
	app.Run([]string{"init", "--dry-run", "--profile", "minimal"})
	if !strings.Contains(out.String(), "skip_have") && !strings.Contains(strings.ToLower(out.String()), "already") {
		// The json form uses skip_have; the text form says "already installed".
		t.Errorf("present git not marked as skipped:\n%s", out.String())
	}
}

// TestForgeInitFullRunAppliesAndWritesLock verifies a real (non-dry-run) init
// writes the toolchain lock and runs the manifest (AC-26 core).
func TestForgeInitFullRunAppliesAndWritesLock(t *testing.T) {
	app, out := newInitApp(t)
	home := os.Getenv(daemon.EnvHome)
	code := app.Run([]string{"init", "--yes", "--profile", "minimal"})
	if code != 0 {
		t.Fatalf("exit %d; output:\n%s", code, out.String())
	}
	lockPath := filepath.Join(home, "toolchain.json")
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("toolchain lock not written: %v", err)
	}
	if !strings.Contains(out.String(), "manifest") {
		t.Errorf("manifest not rendered")
	}
}

// TestForgeInitRepairReinstallsMissing verifies --repair reinstalls a missing
// tool recorded in the lock.
func TestForgeInitRepairReinstallsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv(daemon.EnvHome, home)
	// Seed a lock that says git+codex are present.
	lock := bootstrap.NewToolchainLock(filepath.Join(home, "toolchain.json"))
	lock.Save(bootstrap.LockFile{Toolchain: []bootstrap.LockedVersion{
		{ID: "git", Version: "1.0.0", Source: "detected"},
		{ID: "codex", Version: "1.0.0", Source: "detected"},
	}})

	// Detector finds only git (codex missing → repair reinstalls it).
	paths := map[string]string{"git": "/usr/bin/git", "brew": "/x/brew"}
	outputs := map[string]string{"git": "git 1.0.0\n"}
	detector := bootstrap.NewFakeDetector(paths, outputs, "/bin/zsh", home)
	out := &bytes.Buffer{}
	app := cli.New()
	app.Out = out
	app.Err = out
	app.Stdin = strings.NewReader("")
	app.InitDepsForTest(t, detector, out)

	code := app.Run([]string{"init", "--repair", "--yes"})
	if code != 0 {
		t.Fatalf("exit %d; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "codex") {
		t.Errorf("repair output missing codex:\n%s", out.String())
	}
}

// TestForgeUpdateRuns verifies `forge update` produces a plan and completes.
func TestForgeUpdateRuns(t *testing.T) {
	app, out := newInitApp(t, "git")
	code := app.Run([]string{"update", "--yes"})
	if code != 0 {
		t.Fatalf("exit %d; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "Profile: standard") {
		t.Errorf("update did not render a plan:\n%s", out.String())
	}
}

// snapshotHome returns a coarse snapshot of the test home dir contents to detect
// any mutation by --dry-run.
func snapshotHome(t *testing.T) string {
	t.Helper()
	home := os.Getenv(daemon.EnvHome)
	var paths []string
	_ = filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	return strings.Join(paths, "|")
}

var _ io.Writer = (*bytes.Buffer)(nil)
