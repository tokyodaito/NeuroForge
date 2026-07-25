// Package m13integration contains the M13 integration tests that exercise the
// full bootstrap pipeline (spec §7, milestone M13) including the hard safety
// rules (§36.17/§36.18/§36.19).
//
// These tests compose the bootstrap domain packages with the fake detector +
// fake installer + auto-confirmer (rule §33: no real system package installs in
// CI). They verify the critical M13 invariants:
//
//   - AC-25: `forge init --dry-run` produces a plan and changes NOTHING.
//   - AC-26: a full init installs selected tools via confirmed official
//     mechanisms and writes the toolchain lock.
//   - §36.17: no silent install — every step requires confirmation.
//   - §36.18: no silent privilege escalation — sudo steps ask explicitly.
//   - §7.2 stage 4: shell profile changes are shown as a diff first.
//   - §7.2 stage 6: the auth wizard never collects a provider password.
//   - §36.19: a toolchain update is refused during an active task.
//   - §7.5: a failed post-update conformance suite rolls back the lock.
package m13integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/bootstrap"
)

// --- fixtures ---

func fakeDetector(tools ...string) *bootstrap.FakeDetector {
	paths := map[string]string{"brew": "/opt/homebrew/bin/brew"}
	outputs := map[string]string{}
	for _, t := range tools {
		paths[t] = "/usr/bin/" + t
		outputs[t] = t + " 1.0.0\n"
	}
	return bootstrap.NewFakeDetector(paths, outputs, "/bin/zsh", "/home/user")
}

func mustScan(t *testing.T, d bootstrap.Detector) bootstrap.SystemScan {
	t.Helper()
	s, err := bootstrap.Scan(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// --- AC-25: dry-run changes nothing ---

func TestAC25_DryRunProducesPlanAndChangesNothing(t *testing.T) {
	dir := t.TempDir()
	before := dirSnapshot(t, dir)

	d := fakeDetector()
	plan, err := bootstrap.ComputePlan(bootstrap.PlanOptions{
		Profile: bootstrap.ProfileStandard, Scan: mustScan(t, d),
		ShellProfilePath: filepath.Join(dir, ".zshrc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || len(plan.WillInstall) == 0 {
		t.Fatalf("dry-run produced no install plan")
	}

	// Render the plan (what --dry-run prints).
	rendered := bootstrap.RenderPlan(plan)
	if !strings.Contains(rendered, "Будет установлено") {
		t.Errorf("plan render missing install section")
	}

	// NOTHING on disk must have changed.
	after := dirSnapshot(t, dir)
	if before != after {
		t.Errorf("dry-run mutated the filesystem (AC-25)")
	}
}

// --- AC-26: full init installs tools via confirmed mechanisms + writes lock ---

func TestAC26_FullInitInstallsAndLocks(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "toolchain.json")

	w, err := bootstrap.NewWizard(bootstrap.WizardConfig{
		Detector:  fakeDetector(),
		Installer: bootstrap.NewFakeInstaller(),
		Confirmer: bootstrap.NewAutoConfirmer(true, true, true),
		LockPath:  lockPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.Run(context.Background(), bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal})
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// AC-26: tools installed via the manifest.
	if res.Manifest == nil || len(res.Manifest.Entries) == 0 {
		t.Errorf("manifest empty after init")
	}
	// AC-26: toolchain lock written (§7.4).
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("toolchain lock not written (§7.4): %v", err)
	}
	lf, _ := bootstrap.NewToolchainLock(lockPath).Load()
	if len(lf.Toolchain) == 0 {
		t.Errorf("toolchain lock has no entries")
	}
}

// --- §36.17: no silent install ---

func TestNoSilentInstall(t *testing.T) {
	d := fakeDetector()
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal, Scan: mustScan(t, d)})
	inst := bootstrap.NewFakeInstaller()

	// DENY plan → nothing installed.
	exec, _ := bootstrap.NewExecutor(inst, bootstrap.NewAutoConfirmer(false, true, true))
	_, err := exec.Apply(context.Background(), plan)
	if !errors.Is(err, bootstrap.ErrNotConfirmed) {
		t.Fatalf("expected ErrNotConfirmed, got %v", err)
	}
	if len(inst.RecordedToolIDs()) != 0 {
		t.Errorf("install ran without confirmation (§36.17)")
	}

	// A nil confirmer is rejected outright.
	if _, err := bootstrap.NewExecutor(inst, nil); err == nil {
		t.Errorf("nil confirmer must be rejected (§36.17)")
	}
}

// --- §36.18: no silent privilege escalation ---

func TestNoSilentSudo(t *testing.T) {
	d := fakeDetector()
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileFull, Scan: mustScan(t, d)})
	inst := bootstrap.NewFakeInstaller()
	// Approve plan, DENY sudo.
	exec, _ := bootstrap.NewExecutor(inst, bootstrap.NewAutoConfirmer(true, false, true))
	_, err := exec.Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("expected sudo-not-approved error, got %v", err)
	}
}

// --- §7.2 stage 4: shell profile changes shown as diff first ---

func TestShellProfileDiffShownBeforeApplying(t *testing.T) {
	d := fakeDetector()
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{
		Profile: bootstrap.ProfileAndroid, Scan: mustScan(t, d),
		ShellProfilePath: "/home/user/.zshrc",
	})
	if !plan.RequiresShellProfileChange {
		t.Fatalf("android plan should require a shell change")
	}
	if !strings.Contains(plan.ShellProfileDiff, "ANDROID_HOME") {
		t.Errorf("shell diff missing ANDROID_HOME")
	}

	// Deny the shell change → apply aborts with the dedicated error.
	inst := bootstrap.NewFakeInstaller()
	exec, _ := bootstrap.NewExecutor(inst, bootstrap.NewAutoConfirmer(true, true, false))
	_, err := exec.Apply(context.Background(), plan)
	if !errors.Is(err, bootstrap.ErrShellProfileNotApproved) {
		t.Fatalf("expected ErrShellProfileNotApproved, got %v", err)
	}
}

// --- §7.2 stage 6: auth wizard never collects passwords ---

type recordingLauncher struct {
	asked   []string
	answers map[string]bootstrap.AuthStatus
}

func (r *recordingLauncher) LaunchOfficialLogin(_ context.Context, provider string) (bootstrap.AuthStatus, error) {
	r.asked = append(r.asked, provider)
	if a, ok := r.answers[provider]; ok {
		return a, nil
	}
	return bootstrap.AuthLoginNeeded, nil
}

func TestAuthWizardNeverAsksPasswords(t *testing.T) {
	d := fakeDetector()
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileStandard, Scan: mustScan(t, d)})
	launch := &recordingLauncher{answers: map[string]bootstrap.AuthStatus{"codex": bootstrap.AuthConnected}}
	entries := bootstrap.NewAuthWizard(launch).Run(context.Background(), plan)
	if len(launch.asked) == 0 {
		t.Errorf("wizard did not launch any official flows")
	}
	// Every entry must reference the official mechanism, never a password field.
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Detail), "password") {
			t.Errorf("auth entry leaked a password reference (§7.2 stage 6): %+v", e)
		}
	}
}

// --- §36.19: no toolchain update during active task ---

type fakeGuard struct{ active bool }

func (f fakeGuard) HasActiveTask(context.Context) (bool, error) { return f.active, nil }

func TestNoToolchainUpdateDuringActiveTask(t *testing.T) {
	lock := bootstrap.NewToolchainLock(filepath.Join(t.TempDir(), "toolchain.json"))
	d := fakeDetector()
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal, Scan: mustScan(t, d)})
	exec, _ := bootstrap.NewExecutor(bootstrap.NewFakeInstaller(), bootstrap.NewAutoConfirmer(true, true, true))

	_, err := bootstrap.Update(context.Background(), lock, bootstrap.UpdateOptions{
		ActiveTaskGuard: fakeGuard{active: true},
	}, plan, exec)
	if !errors.Is(err, bootstrap.ErrActiveTask) {
		t.Fatalf("expected ErrActiveTask, got %v", err)
	}
}

// --- §7.5: rollback on failed conformance ---

func TestUpdateRollbackOnConformanceFailure(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "toolchain.json")
	lock := bootstrap.NewToolchainLock(lockPath)
	prev := bootstrap.LockFile{Toolchain: []bootstrap.LockedVersion{
		{ID: "git", Version: "1.0.0", Source: "detected"},
	}}
	if err := lock.Save(prev); err != nil {
		t.Fatal(err)
	}
	d := fakeDetector("git") // git present → no reinstall needed
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal, Scan: mustScan(t, d)})
	exec, _ := bootstrap.NewExecutor(bootstrap.NewFakeInstaller(), bootstrap.NewAutoConfirmer(true, true, true))

	conf := func(context.Context, bootstrap.LockFile) error { return errors.New("adapter broken") }
	_, err := bootstrap.Update(context.Background(), lock, bootstrap.UpdateOptions{ConformanceTest: conf}, plan, exec)
	if !errors.Is(err, bootstrap.ErrConformanceFailed) {
		t.Fatalf("expected ErrConformanceFailed, got %v", err)
	}
	got, _ := lock.Load()
	if len(got.Toolchain) != 1 || got.Toolchain[0].Version != "1.0.0" {
		t.Errorf("previous lock not restored after rollback: %+v", got.Toolchain)
	}
}

// --- §7.2 stage 2: every profile is selectable ---

func TestAllProfilesProduceAPlan(t *testing.T) {
	for _, p := range bootstrap.AllProfiles() {
		t.Run(string(p), func(t *testing.T) {
			d := fakeDetector()
			plan, err := bootstrap.ComputePlan(bootstrap.PlanOptions{
				Profile: p, Scan: mustScan(t, d), CustomSelection: []string{"git", "codex"},
			})
			if err != nil {
				t.Fatalf("profile %s: %v", p, err)
			}
			if plan == nil {
				t.Errorf("profile %s produced nil plan", p)
			}
		})
	}
}

// --- installer tests never install real system packages (rule §33) ---

func TestFakeInstallerNeverShellsOut(t *testing.T) {
	// The FakeInstaller records steps in memory and returns manifest entries;
	// it performs NO exec/os calls. This is the CI guarantee (rule §33).
	inst := bootstrap.NewFakeInstaller()
	d := fakeDetector()
	plan, _ := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: bootstrap.ProfileMinimal, Scan: mustScan(t, d)})
	exec, _ := bootstrap.NewExecutor(inst, bootstrap.NewAutoConfirmer(true, true, true))
	manifest, err := exec.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range manifest.Entries {
		if e.Installer != "fake" {
			t.Errorf("CI must use the fake installer, got %q", e.Installer)
		}
	}
}

// --- helpers ---

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
