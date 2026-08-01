package bootstrap_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"neuroforge/internal/bootstrap"
)

// --- shared fixtures ---

func detectorWith(tools ...string) *bootstrap.FakeDetector {
	paths := map[string]string{}
	outputs := map[string]string{}
	for _, t := range tools {
		paths[t] = "/usr/bin/" + t
		outputs[t] = t + " version 1.2.3\n"
	}
	return bootstrap.NewFakeDetector(paths, outputs, "/bin/zsh", "/home/user")
}

// --- system scan ---

func TestScanIsReadOnlyAndDetectsTools(t *testing.T) {
	d := detectorWith("git", "codex", "node")
	// Add a package manager so detection succeeds. The scanner probes brew on
	// darwin and apt/dnf/... on linux, so the fake must match the host OS.
	if runtime.GOOS == "darwin" {
		d.AddPath("brew", "/opt/homebrew/bin/brew")
	} else {
		d.AddPath("apt", "/usr/bin/apt")
	}
	scan, err := bootstrap.Scan(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if scan.OS == "" || scan.Arch == "" {
		t.Errorf("os/arch not detected")
	}
	if scan.Shell != "/bin/zsh" {
		t.Errorf("shell = %q", scan.Shell)
	}
	if gt, ok := scan.Find("git"); !ok || !gt.Present {
		t.Errorf("git not detected")
	} else if gt.Version != "git version 1.2.3" {
		t.Errorf("git version = %q", gt.Version)
	}
	if scan.PackageManager == "" {
		t.Errorf("package manager not detected")
	}
	if scan.Elevated {
		t.Errorf("should not be elevated")
	}
}

func TestScanFlagsElevation(t *testing.T) {
	d := detectorWith("git")
	d.SetElevated(true)
	scan, _ := bootstrap.Scan(context.Background(), d)
	if !scan.Elevated {
		t.Errorf("elevation not flagged")
	}
}

// --- profiles ---

func TestProfilesValid(t *testing.T) {
	for _, p := range bootstrap.AllProfiles() {
		if !p.IsValid() {
			t.Errorf("%q not valid", p)
		}
	}
}

func TestProfileSpecsDisjoint(t *testing.T) {
	// Minimal must request fewer tools than Standard.
	min := bootstrap.ProfileSpecFor(bootstrap.ProfileMinimal)
	std := bootstrap.ProfileSpecFor(bootstrap.ProfileStandard)
	if len(min.Tools) >= len(std.Tools) {
		t.Errorf("minimal should request fewer tools than standard")
	}
	// Android profile includes the Android SDK + Java.
	and := bootstrap.ProfileSpecFor(bootstrap.ProfileAndroid)
	foundSDK := false
	for _, tool := range and.Tools {
		if tool.ID == "android-sdk" {
			foundSDK = true
			if tool.ShellProfileChange == "" {
				t.Errorf("android-sdk must propose a shell profile change")
			}
		}
	}
	if !foundSDK {
		t.Errorf("android profile missing android-sdk")
	}
}

// --- plan / dry-run (AC-25) ---

func TestDryRunChangesNothing(t *testing.T) {
	dir := t.TempDir()
	// Record the directory state before.
	before := dirSnapshot(t, dir)

	w, err := bootstrap.NewWizard(bootstrap.WizardConfig{Detector: detectorWith()})
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := w.DryRun(context.Background(), bootstrap.PlanOptions{
		Profile:          bootstrap.ProfileMinimal,
		ShellProfilePath: filepath.Join(dir, ".zshrc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		t.Fatal("plan is nil")
	}
	// AC-25: nothing on disk changed.
	after := dirSnapshot(t, dir)
	if before != after {
		t.Errorf("dry-run mutated the filesystem (AC-25)")
	}
}

func TestPlanSkipsPresentTools(t *testing.T) {
	// git already installed → should be skipped, not reinstalled (§7.2 stage 4).
	d := detectorWith("git")
	plan, err := bootstrap.ComputePlan(bootstrap.PlanOptions{
		Profile: bootstrap.ProfileMinimal, Scan: mustScan(t, d),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.WontInstall) == 0 {
		t.Errorf("expected git in wont-install")
	}
	for _, s := range plan.WillInstall {
		if s.ToolID == "git" {
			t.Errorf("git should be skipped (already present), not installed")
		}
	}
}

func TestPlanShowsShellProfileDiff(t *testing.T) {
	// android profile proposes ANDROID_HOME → diff must be shown.
	d := detectorWith()
	plan, err := bootstrap.ComputePlan(bootstrap.PlanOptions{
		Profile:          bootstrap.ProfileAndroid,
		Scan:             mustScan(t, d),
		ShellProfilePath: "/home/user/.zshrc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresShellProfileChange {
		t.Errorf("plan should require shell profile change")
	}
	if !strings.Contains(plan.ShellProfileDiff, "ANDROID_HOME") {
		t.Errorf("shell diff missing ANDROID_HOME:\n%s", plan.ShellProfileDiff)
	}
	if !strings.Contains(plan.ShellProfileDiff, ".zshrc") {
		t.Errorf("shell diff missing the profile path header")
	}
}

func TestNoGlobalSkipsGlobalInstalls(t *testing.T) {
	d := detectorWith()
	plan, err := bootstrap.ComputePlan(bootstrap.PlanOptions{
		Profile: bootstrap.ProfileStandard, Scan: mustScan(t, d), NoGlobal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	skipped := false
	for _, s := range plan.WontInstall {
		if s.Action == bootstrap.ActionSkipGlobal {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("--no-global should skip global installs")
	}
}

func TestSkipAgentsDropsCodingAgents(t *testing.T) {
	d := detectorWith()
	plan, err := bootstrap.ComputePlan(bootstrap.PlanOptions{
		Profile: bootstrap.ProfileStandard, Scan: mustScan(t, d), SkipAgents: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.WillInstall {
		if s.Category == bootstrap.CatCodingAgent {
			t.Errorf("skip-agents should drop coding agents; found %s", s.ToolID)
		}
	}
}

// --- executor: no silent install / sudo / shell change ---

func TestApplyRequiresPlanConfirmation(t *testing.T) {
	plan := &bootstrap.InstallPlan{Profile: bootstrap.ProfileMinimal}
	plan.WillInstall = []bootstrap.InstallStep{{ToolID: "git", Action: bootstrap.ActionInstall}}
	inst := bootstrap.NewFakeInstaller()
	// Deny plan → abort, nothing installed.
	conf := bootstrap.NewAutoConfirmer(false, true, true)
	exec, _ := bootstrap.NewExecutor(inst, conf)
	_, err := exec.Apply(context.Background(), plan)
	if !errors.Is(err, bootstrap.ErrNotConfirmed) {
		t.Fatalf("expected ErrNotConfirmed, got %v", err)
	}
	if len(inst.RecordedToolIDs()) != 0 {
		t.Errorf("nothing should be installed when plan denied")
	}
}

func TestApplyRequiresSudoConfirmation(t *testing.T) {
	plan := &bootstrap.InstallPlan{Profile: bootstrap.ProfileFull, RequiresSudo: true}
	plan.WillInstall = []bootstrap.InstallStep{{
		ToolID: "docker", Action: bootstrap.ActionInstall, NeedsSudo: true,
	}}
	inst := bootstrap.NewFakeInstaller()
	// Approve plan, DENY sudo → abort at the sudo step.
	conf := bootstrap.NewAutoConfirmer(true, false, true)
	exec, _ := bootstrap.NewExecutor(inst, conf)
	_, err := exec.Apply(context.Background(), plan)
	if !errors.Is(err, bootstrap.ErrNotConfirmed) {
		t.Fatalf("expected ErrNotConfirmed for sudo, got %v", err)
	}
	if !conf.SudoAskedFor("docker") {
		t.Errorf("sudo for docker was never asked (§36.18)")
	}
}

func TestApplyRequiresShellProfileConfirmation(t *testing.T) {
	plan := &bootstrap.InstallPlan{
		Profile:                    bootstrap.ProfileAndroid,
		RequiresShellProfileChange: true,
		ShellProfileDiff:           "+export ANDROID_HOME=...",
	}
	plan.WillInstall = []bootstrap.InstallStep{{
		ToolID: "android-sdk", Action: bootstrap.ActionInstall,
		ShellProfileChange: "export ANDROID_HOME=...",
	}}
	inst := bootstrap.NewFakeInstaller()
	// Approve plan, DENY shell change → abort.
	conf := bootstrap.NewAutoConfirmer(true, true, false)
	exec, _ := bootstrap.NewExecutor(inst, conf)
	_, err := exec.Apply(context.Background(), plan)
	if !errors.Is(err, bootstrap.ErrShellProfileNotApproved) {
		t.Fatalf("expected ErrShellProfileNotApproved, got %v", err)
	}
	if conf.ShellAsked() == 0 {
		t.Errorf("shell diff was never shown/asked (§7.2 stage 4)")
	}
}

func TestApplyHappyPathRecordsManifest(t *testing.T) {
	d := detectorWith()
	plan, err := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal, Scan: mustScan(t, d)})
	if err != nil {
		t.Fatal(err)
	}
	inst := bootstrap.NewFakeInstaller()
	conf := bootstrap.NewAutoConfirmer(true, true, true)
	exec, _ := bootstrap.NewExecutor(inst, conf)
	manifest, err := exec.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) == 0 {
		t.Errorf("manifest empty after successful apply")
	}
	// Every entry must be a positive outcome.
	for _, e := range manifest.Entries {
		if e.Outcome == "failed" {
			t.Errorf("unexpected failed entry: %+v", e)
		}
	}
}

func TestNewExecutorRejectsNilConfirmer(t *testing.T) {
	_, err := bootstrap.NewExecutor(bootstrap.NewFakeInstaller(), nil)
	if err == nil {
		t.Errorf("nil confirmer must be rejected (silent install forbidden)")
	}
}

// --- toolchain lock: no update during active task (§36.19) ---

type fakeGuard struct{ active bool }

func (f fakeGuard) HasActiveTask(context.Context) (bool, error) { return f.active, nil }

func TestToolchainLockRefusesUpdateDuringActiveTask(t *testing.T) {
	lock := bootstrap.NewToolchainLock(filepath.Join(t.TempDir(), "toolchain.json"))
	called := false
	_, err := lock.Update(context.Background(), fakeGuard{active: true}, func(context.Context) (bootstrap.LockFile, error) {
		called = true
		return bootstrap.LockFile{}, nil
	})
	if !errors.Is(err, bootstrap.ErrActiveTask) {
		t.Fatalf("expected ErrActiveTask, got %v", err)
	}
	if called {
		t.Errorf("updateFn must NOT run during an active task (§36.19)")
	}
}

func TestToolchainLockPersistsAndDrifts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toolchain.json")
	lock := bootstrap.NewToolchainLock(path)
	d := detectorWith("git", "node")
	scan := mustScan(t, d)
	if err := lock.Save(bootstrap.LockFromScan(scan, lock.Now())); err != nil {
		t.Fatal(err)
	}
	// New scan where node was upgraded → drift detected.
	d2 := detectorWith("git")
	d2Outputs := map[string]string{"git": "git version 1.2.3\n", "node": "node version 99.0.0\n"}
	d2 = bootstrap.NewFakeDetector(map[string]string{"git": "/usr/bin/git", "node": "/usr/bin/node"}, d2Outputs, "/bin/zsh", "/h")
	scan2 := mustScan(t, d2)
	drift := lock.Drift(scan2)
	if len(drift) == 0 {
		t.Errorf("expected drift for upgraded node")
	}
}

// --- auth wizard never asks passwords ---

type fakeLauncher struct {
	asked  []string
	status bootstrap.AuthStatus
}

func (f *fakeLauncher) LaunchOfficialLogin(_ context.Context, provider string) (bootstrap.AuthStatus, error) {
	f.asked = append(f.asked, provider)
	return f.status, nil
}

func TestAuthWizardUsesOfficialMechanism(t *testing.T) {
	d := detectorWith()
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileStandard, Scan: mustScan(t, d)})
	launch := &fakeLauncher{status: bootstrap.AuthConnected}
	entries := bootstrap.NewAuthWizard(launch).Run(context.Background(), plan)
	if len(launchedProviders(launch)) == 0 {
		t.Errorf("auth wizard launched no official flows")
	}
	for _, e := range entries {
		if e.Status != bootstrap.AuthConnected {
			t.Errorf("provider %s not connected: %s", e.Provider, e.Status)
		}
	}
}

func launchedProviders(l *fakeLauncher) []string { return l.asked }

func TestAuthWizardNeverFabricatesConnection(t *testing.T) {
	// No launcher wired → every provider recorded as login_required, NEVER
	// fabricated as connected (§7.2 stage 6).
	d := detectorWith()
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal, Scan: mustScan(t, d)})
	entries := bootstrap.NewAuthWizard(nil).Run(context.Background(), plan)
	for _, e := range entries {
		if e.Status == bootstrap.AuthConnected {
			t.Errorf("wizard fabricated a connection without a launcher")
		}
	}
}

// --- update rollback (§7.5 step 5) ---

func TestUpdateRollsBackOnConformanceFailure(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "toolchain.json")
	lock := bootstrap.NewToolchainLock(lockPath)
	// Persist a previous working lock.
	prev := bootstrap.LockFile{Toolchain: []bootstrap.LockedVersion{{ID: "git", Version: "1.0.0", Source: "detected"}}}
	if err := lock.Save(prev); err != nil {
		t.Fatal(err)
	}

	d := detectorWith()
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal, Scan: mustScan(t, d)})
	exec, _ := bootstrap.NewExecutor(bootstrap.NewFakeInstaller(), bootstrap.NewAutoConfirmer(true, true, true))

	// Conformance fails → rollback.
	conf := func(context.Context, bootstrap.LockFile) error { return errors.New("adapter conformance failed") }
	_, err := bootstrap.Update(context.Background(), lock, bootstrap.UpdateOptions{ConformanceTest: conf}, plan, exec)
	if !errors.Is(err, bootstrap.ErrConformanceFailed) {
		t.Fatalf("expected ErrConformanceFailed, got %v", err)
	}
	// Previous lock must be restored.
	got, _ := lock.Load()
	if len(got.Toolchain) != 1 || got.Toolchain[0].Version != "1.0.0" {
		t.Errorf("previous lock not restored after rollback: %+v", got.Toolchain)
	}
}

func TestUpdateBlockedDuringActiveTask(t *testing.T) {
	lock := bootstrap.NewToolchainLock(filepath.Join(t.TempDir(), "toolchain.json"))
	plan := &bootstrap.InstallPlan{Profile: bootstrap.ProfileMinimal}
	exec, _ := bootstrap.NewExecutor(bootstrap.NewFakeInstaller(), bootstrap.NewAutoConfirmer(true, true, true))
	_, err := bootstrap.Update(context.Background(), lock, bootstrap.UpdateOptions{ActiveTaskGuard: fakeGuard{active: true}}, plan, exec)
	if !errors.Is(err, bootstrap.ErrActiveTask) {
		t.Fatalf("expected ErrActiveTask, got %v", err)
	}
}

// --- repair ---

func TestRepairReinstallsMissingTools(t *testing.T) {
	// Lock says git + codex present.
	lockFile := bootstrap.LockFile{Toolchain: []bootstrap.LockedVersion{
		{ID: "git", Version: "1.0.0", Source: "detected"},
		{ID: "codex", Version: "1.0.0", Source: "detected"},
	}}
	// Scan finds only git (codex missing).
	d := detectorWith("git")
	scan := mustScan(t, d)
	inst := bootstrap.NewFakeInstaller()
	conf := bootstrap.NewAutoConfirmer(true, true, true)
	exec, _ := bootstrap.NewExecutor(inst, conf)
	res, err := bootstrap.Repair(context.Background(), bootstrap.ProfileStandard, scan, lockFile, exec)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied {
		t.Errorf("repair should apply")
	}
	// codex should have been reinstalled.
	reinstalled := inst.RecordedToolIDs()
	found := false
	for _, id := range reinstalled {
		if id == "codex" {
			found = true
		}
	}
	if !found {
		t.Errorf("repair did not reinstall missing codex; installed: %v", reinstalled)
	}
}

func TestRepairNoopWhenNothingMissing(t *testing.T) {
	d := detectorWith("git")
	scan := mustScan(t, d)
	lockFile := bootstrap.LockFile{Toolchain: []bootstrap.LockedVersion{{ID: "git", Version: "1.0.0", Source: "detected"}}}
	exec, _ := bootstrap.NewExecutor(bootstrap.NewFakeInstaller(), bootstrap.NewAutoConfirmer(true, true, true))
	res, err := bootstrap.Repair(context.Background(), bootstrap.ProfileMinimal, scan, lockFile, exec)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied {
		t.Errorf("repair should report applied (no-op success)")
	}
}

// --- full wizard run writes the lock ---

func TestWizardRunFullLifecycle(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "toolchain.json")
	w, err := bootstrap.NewWizard(bootstrap.WizardConfig{
		Detector:     detectorWith(),
		Installer:    bootstrap.NewFakeInstaller(),
		Confirmer:    bootstrap.NewAutoConfirmer(true, true, true),
		AuthLauncher: &fakeLauncher{status: bootstrap.AuthConnected},
		LockPath:     lockPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.Run(context.Background(), bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest == nil || len(res.Manifest.Entries) == 0 {
		t.Errorf("manifest empty")
	}
	// Lock file written.
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("toolchain lock not written: %v", err)
	}
}

func TestWizardRunWithoutInstallerErrors(t *testing.T) {
	w, _ := bootstrap.NewWizard(bootstrap.WizardConfig{Detector: detectorWith()})
	_, err := w.Run(context.Background(), bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal})
	if !errors.Is(err, bootstrap.ErrNeedsExecutor) {
		t.Errorf("expected ErrNeedsExecutor, got %v", err)
	}
}

// --- helpers ---

func mustScan(t *testing.T, d *bootstrap.FakeDetector) bootstrap.SystemScan {
	t.Helper()
	s, err := bootstrap.Scan(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func dirSnapshot(t *testing.T, dir string) string {
	t.Helper()
	var paths []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	return strings.Join(paths, "|")
}
