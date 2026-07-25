package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ToolchainLock is the §7.4 toolchain lock. It persists the detected + installed
// versions of the toolchain so the daemon can detect drift, and it REFUSES to
// update a provider CLI while an active task is running (§36.19: "Не обновлять
// provider CLI во время active run").
type ToolchainLock struct {
	path string
	mu   sync.Mutex
	now  func() time.Time
}

// LockedVersion is one entry in the lock file.
type LockedVersion struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Source   string `json:"source"` // "detected" | "installed"
	LockedAt string `json:"locked_at"`
}

// LockFile is the on-disk shape of the toolchain lock (§7.4).
type LockFile struct {
	Toolchain []LockedVersion `json:"toolchain"`
}

// NewToolchainLock opens (or creates) the lock at path.
func NewToolchainLock(path string) *ToolchainLock {
	return &ToolchainLock{path: path, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock injects a clock (tests).
func (l *ToolchainLock) SetClock(now func() time.Time) {
	if now != nil {
		l.now = now
	}
}

// Now returns the lock's current time (used when building locks from scans).
func (l *ToolchainLock) Now() time.Time { return l.now() }

// Load reads the lock file. Returns an empty lock if it does not exist (not an
// error — fresh installs have no lock yet).
func (l *ToolchainLock) Load() (LockFile, error) {
	b, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return LockFile{}, nil
		}
		return LockFile{}, fmt.Errorf("bootstrap: read toolchain lock: %w", err)
	}
	var lf LockFile
	if err := json.Unmarshal(b, &lf); err != nil {
		return LockFile{}, fmt.Errorf("bootstrap: parse toolchain lock: %w", err)
	}
	return lf, nil
}

// Save writes the lock atomically (mode 0o600 — version info is not secret but
// we keep runtime files private).
func (l *ToolchainLock) Save(lf LockFile) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("bootstrap: create lock dir: %w", err)
	}
	sort.Slice(lf.Toolchain, func(i, j int) bool { return lf.Toolchain[i].ID < lf.Toolchain[j].ID })
	b, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("bootstrap: write toolchain lock: %w", err)
	}
	return os.Rename(tmp, l.path)
}

// LockFromScan builds a lock file from a system scan (§7.4: detected versions).
func LockFromScan(scan SystemScan, now time.Time) LockFile {
	lf := LockFile{}
	ts := now.Format(time.RFC3339Nano)
	for _, t := range scan.Tools {
		if !t.Present {
			continue
		}
		lf.Toolchain = append(lf.Toolchain, LockedVersion{
			ID: t.ID, Version: t.Version, Source: "detected", LockedAt: ts,
		})
	}
	return lf
}

// MergeInstalled merges freshly-installed tool versions into the lock (replacing
// any prior entry for the same id).
func MergeInstalled(lf LockFile, entries []ManifestEntry, now time.Time) LockFile {
	byID := map[string]int{}
	for i, e := range lf.Toolchain {
		byID[e.ID] = i
	}
	ts := now.Format(time.RFC3339Nano)
	for _, m := range entries {
		if m.Outcome == "failed" || m.Outcome == "skipped" {
			continue
		}
		v := LockedVersion{ID: m.ToolID, Version: "installed", Source: "installed", LockedAt: ts}
		if idx, ok := byID[m.ToolID]; ok {
			lf.Toolchain[idx] = v
		} else {
			lf.Toolchain = append(lf.Toolchain, v)
			byID[m.ToolID] = len(lf.Toolchain) - 1
		}
	}
	return lf
}

// ActiveTaskGuard is the capability the toolchain lock consults to enforce
// §36.19 (never update a provider CLI during an active task). The daemon
// supplies the real implementation; tests supply a fake.
type ActiveTaskGuard interface {
	HasActiveTask(ctx context.Context) (bool, error)
}

// ErrActiveTask is returned when an update is attempted while a task is active.
// It is NOT a transient error — the caller MUST surface it to the user (§36.19).
var ErrActiveTask = errors.New("bootstrap: cannot update toolchain during an active task (§36.19)")

// Update checks whether updating the toolchain is allowed (§36.19) and, if so,
// returns the candidate versions to lock. It does NOT itself mutate the system
// (the installer does); it only guards the decision.
//
// updateFn is the caller's update routine (e.g. re-scan + recompute plan). It is
// invoked ONLY when no active task is running.
func (l *ToolchainLock) Update(ctx context.Context, guard ActiveTaskGuard, updateFn func(ctx context.Context) (LockFile, error)) (LockFile, error) {
	if guard != nil {
		active, err := guard.HasActiveTask(ctx)
		if err != nil {
			return LockFile{}, fmt.Errorf("bootstrap: active-task check failed: %w", err)
		}
		if active {
			return LockFile{}, ErrActiveTask
		}
	}
	if updateFn == nil {
		return l.Load()
	}
	nl, err := updateFn(ctx)
	if err != nil {
		return LockFile{}, err
	}
	if err := l.Save(nl); err != nil {
		return LockFile{}, err
	}
	return nl, nil
}

// Drift returns the set of tool ids whose locked version differs from the given
// scan (e.g. a tool was upgraded outside NeuroForge).
func (l *ToolchainLock) Drift(scan SystemScan) []string {
	lf, err := l.Load()
	if err != nil {
		return nil
	}
	locked := map[string]string{}
	for _, e := range lf.Toolchain {
		locked[e.ID] = e.Version
	}
	var drift []string
	for _, t := range scan.Tools {
		if !t.Present {
			continue
		}
		if lv, ok := locked[t.ID]; ok && lv != "" && lv != t.Version {
			drift = append(drift, t.ID)
		}
	}
	sort.Strings(drift)
	return drift
}
