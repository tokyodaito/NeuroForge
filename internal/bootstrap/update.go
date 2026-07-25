package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// UpdateOptions configures `forge update` (§7.5).
type UpdateOptions struct {
	// Components restricts the update to specific tool ids; empty = all.
	Components []string
	// ConformanceTest, when non-nil, is the adapter conformance suite the update
	// must pass after applying (§7.5 step 4). If it fails, the previous lock is
	// restored (§7.5 step 5: rollback).
	ConformanceTest func(ctx context.Context, candidate LockFile) error
	// ActiveTaskGuard enforces §36.19 (no update during active task).
	ActiveTaskGuard ActiveTaskGuard
}

// UpdateResult is the outcome of `forge update`.
type UpdateResult struct {
	Applied        bool
	NewLock        LockFile
	RolledBack     bool
	RollbackReason string
}

// ErrConformanceFailed signals that the post-update conformance suite failed
// and the previous lock was restored (§7.5 step 5).
var ErrConformanceFailed = errors.New("bootstrap: post-update conformance suite failed; rolled back to previous toolchain")

// Update performs the §7.5 update flow:
//  1. compatibility check (no active task, §36.19);
//  2. compute the candidate plan + lock;
//  3. show the plan (the caller renders ComputePlan output);
//  4. apply via the executor (with confirmation);
//  5. run the conformance suite; on failure, restore the previous lock
//     (rollback, §7.5 step 5).
//
// Update never runs silently: it requires an executor with a confirmer.
func Update(ctx context.Context, lock *ToolchainLock, opts UpdateOptions, plan *InstallPlan, exec *Executor) (UpdateResult, error) {
	res := UpdateResult{}

	// §36.19: never update a provider CLI during an active task.
	if opts.ActiveTaskGuard != nil {
		active, err := opts.ActiveTaskGuard.HasActiveTask(ctx)
		if err != nil {
			return res, fmt.Errorf("bootstrap: active-task check failed: %w", err)
		}
		if active {
			return res, ErrActiveTask
		}
	}

	// Snapshot the previous lock for rollback (§7.5 step 5).
	prev, _ := lock.Load()

	// Apply the plan through the confirmation gate.
	manifest, err := exec.Apply(ctx, plan)
	if err != nil {
		return res, fmt.Errorf("bootstrap: update apply failed: %w", err)
	}

	// Build the candidate lock from the manifest.
	candidate := MergeInstalled(prev, manifest.Entries, lock.now())
	if err := lock.Save(candidate); err != nil {
		return res, fmt.Errorf("bootstrap: persist updated lock failed: %w", err)
	}

	// §7.5 step 4: run the adapter conformance suite against the candidate.
	if opts.ConformanceTest != nil {
		if err := opts.ConformanceTest(ctx, candidate); err != nil {
			// §7.5 step 5: rollback to the previous working configuration.
			_ = lock.Save(prev)
			res.RolledBack = true
			res.RollbackReason = err.Error()
			return res, fmt.Errorf("%w: %v", ErrConformanceFailed, err)
		}
	}

	res.Applied = true
	res.NewLock = candidate
	return res, nil
}

// RepairOptions configures `forge init --repair`.
type RepairOptions struct {
	Scan SystemScan
	Lock LockFile
	Exec *Executor
}

// ErrRepairNeedsConfirmation is returned when --repair would change the system
// but no executor/confirmation is wired.
var ErrRepairNeedsConfirmation = errors.New("bootstrap: repair requires a confirmed executor (no silent repair §36.17)")

// Repair reconciles the toolchain with the lock: it re-scans, computes a plan
// for any tool that drifted or went missing, and applies it through the
// confirmation gate. It never silently installs (§36.17).
func Repair(ctx context.Context, profile Profile, scan SystemScan, lockFile LockFile, exec *Executor) (UpdateResult, error) {
	res := UpdateResult{}
	if exec == nil {
		return res, ErrRepairNeedsConfirmation
	}
	// Build a plan: install anything in the lock that is now missing.
	plan := &InstallPlan{Profile: profile}
	missing := map[string]bool{}
	for _, lv := range lockFile.Toolchain {
		if present, _ := scan.Find(lv.ID); !present.Present {
			missing[lv.ID] = true
		}
	}
	if len(missing) == 0 {
		res.Applied = true
		return res, nil
	}
	// Pull canonical requests for the missing tools.
	spec := ProfileSpecFor(profile)
	for _, req := range spec.Tools {
		if missing[req.ID] {
			plan.WillInstall = append(plan.WillInstall, InstallStep{
				ToolID: req.ID, Category: req.Category, Action: ActionInstall,
				InstallHint: req.InstallHint, NeedsSudo: req.NeedsSudo,
				ShellProfileChange: req.ShellProfileChange, Reason: "missing (repair)",
			})
		}
	}
	manifest, err := exec.Apply(ctx, plan)
	if err != nil {
		return res, fmt.Errorf("bootstrap: repair apply failed: %w", err)
	}
	res.Applied = true
	res.NewLock = MergeInstalled(lockFile, manifest.Entries, time.Now().UTC())
	return res, nil
}
