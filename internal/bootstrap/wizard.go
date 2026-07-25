package bootstrap

import (
	"context"
	"fmt"
)

// Wizard orchestrates the §7.2 onboarding stages. It is the in-process driver
// behind `forge init`. Each stage is a separate method so `forge init
// --dry-run` can stop after stage 3 (the plan) without touching the system
// (AC-25).
type Wizard struct {
	detector     Detector
	installer    Installer
	confirmer    Confirmer
	authLauncher LoginLauncher
	lockPath     string
}

// WizardConfig wires the wizard. Installer and Confirmer are REQUIRED for any
// non-dry-run invocation; DryRun works with neither (it never mutates).
type WizardConfig struct {
	Detector     Detector
	Installer    Installer
	Confirmer    Confirmer
	AuthLauncher LoginLauncher
	LockPath     string
}

// NewWizard builds the wizard.
func NewWizard(cfg WizardConfig) (*Wizard, error) {
	if cfg.Detector == nil {
		return nil, fmt.Errorf("bootstrap: detector is required")
	}
	return &Wizard{
		detector:     cfg.Detector,
		installer:    cfg.Installer,
		confirmer:    cfg.Confirmer,
		authLauncher: cfg.AuthLauncher,
		lockPath:     cfg.LockPath,
	}, nil
}

// DryRun executes stages 1–3 only (scan → profile → plan) and returns the plan.
// It changes NOTHING (AC-25). This is the implementation of `forge init
// --dry-run`.
func (w *Wizard) DryRun(ctx context.Context, opts PlanOptions) (SystemScan, *InstallPlan, error) {
	scan, err := Scan(ctx, w.detector)
	if err != nil {
		return SystemScan{}, nil, err
	}
	opts.Scan = scan
	plan, err := ComputePlan(opts)
	if err != nil {
		return scan, nil, err
	}
	return scan, plan, nil
}

// Run executes the full onboarding (stages 1–8). It requires an installer + a
// confirmer; without them it returns ErrNeedsExecutor (no silent install,
// §36.17).
//
// The stages:
//  1. system scan;
//  2. compute the plan from the chosen profile (stage 2 selection);
//  3. render the plan + shell diff for confirmation (stage 3/4);
//  4. apply via the executor (stage 5) — gated by explicit confirmation;
//  5. launch the auth wizard (stage 6) — official mechanisms only;
//  6. write the toolchain lock (§7.4);
//  7. return the manifest + auth entries for the caller to run `forge doctor`.
func (w *Wizard) Run(ctx context.Context, opts PlanOptions) (*RunResult, error) {
	if w.installer == nil || w.confirmer == nil {
		return nil, ErrNeedsExecutor
	}
	exec, err := NewExecutor(w.installer, w.confirmer)
	if err != nil {
		return nil, err
	}
	scan, plan, err := w.DryRun(ctx, opts)
	if err != nil {
		return nil, err
	}
	manifest, err := exec.Apply(ctx, plan)
	if err != nil {
		return nil, err
	}
	// Stage 6: authentication wizard (official mechanisms only).
	var auth []AuthEntry
	if w.authLauncher != nil {
		auth = NewAuthWizard(w.authLauncher).Run(ctx, plan)
	}
	// Stage 8 / §7.4: write the toolchain lock.
	var lockFile LockFile
	if w.lockPath != "" {
		lock := NewToolchainLock(w.lockPath)
		base := LockFromScan(scan, lock.now())
		merged := MergeInstalled(base, manifest.Entries, lock.now())
		if err := lock.Save(merged); err != nil {
			return nil, err
		}
		lockFile = merged
	}
	return &RunResult{
		Scan: scan, Plan: plan, Manifest: manifest, Auth: auth, Lock: lockFile,
	}, nil
}

// ErrNeedsExecutor is returned when Run is invoked without an installer/confirmer
// (use DryRun for a no-op plan).
var ErrNeedsExecutor = fmt.Errorf("bootstrap: a confirmed installer is required for a real init (no silent install §36.17); use --dry-run")

// RunResult aggregates the outcome of a full `forge init` run.
type RunResult struct {
	Scan     SystemScan
	Plan     *InstallPlan
	Manifest *Manifest
	Auth     []AuthEntry
	Lock     LockFile
}
