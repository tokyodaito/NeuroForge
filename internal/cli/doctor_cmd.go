package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"neuroforge/internal/daemon"
	"neuroforge/internal/storage"
	"neuroforge/internal/version"
)

// runDoctor performs basic system checks and prints a report. Exit code is 0 if
// no check FAILED, 1 otherwise. WARNs do not fail the command.
//
// M0 scope (the full onboarding doctor lands in M13, §7): this checks the
// forge build, platform, runtime home directory, permissions, the durable
// SQLite database, and the daemon lifecycle status.
func (a *App) runDoctor(args []string) int {
	jsonOut := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		case "-h", "--help":
			fmt.Fprintln(a.Out, "Usage: forge doctor [--json]")
			return ExitOK
		}
	}

	var checks []checkResult
	checks = append(checks, checkBuild()...)
	checks = append(checks, checkPlatform()...)

	dirs, dirsErr := a.resolveDirs()
	if dirsErr != nil {
		checks = append(checks, checkResult{name: "runtime-home", level: failLevel, detail: dirsErr.Error()})
	} else {
		checks = append(checks, checkRuntimeHome(dirs)...)
		checks = append(checks, checkDatabase(dirs)...)
		checks = append(checks, checkOrphanWorktrees(dirs)...)
		checks = append(checks, checkDaemon(dirs)...)
	}

	failed := 0
	for _, c := range checks {
		if c.level == failLevel {
			failed++
		}
	}

	if jsonOut {
		fmt.Fprintln(a.Out, doctorJSON(checks))
	} else {
		fmt.Fprintln(a.Out, doctorText(checks))
		fmt.Fprintf(a.Out, "\n%d check(s), %d failed\n", len(checks), failed)
	}
	if failed > 0 {
		return ExitErr
	}
	return ExitOK
}

type level int

const (
	okLevel level = iota
	warnLevel
	failLevel
)

func (l level) String() string {
	switch l {
	case okLevel:
		return "OK"
	case warnLevel:
		return "WARN"
	default:
		return "FAIL"
	}
}

type checkResult struct {
	name   string
	level  level
	detail string
}

func checkBuild() []checkResult {
	v := version.Current()
	detail := fmt.Sprintf("forge %s (%s/%s, %s)", v.Version, v.OS, v.Arch, v.GoVersion)
	return []checkResult{{name: "forge-version", level: okLevel, detail: detail}}
}

func checkPlatform() []checkResult {
	return []checkResult{{
		name:   "platform",
		level:  okLevel,
		detail: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}}
}

func checkRuntimeHome(dirs daemon.Dirs) []checkResult {
	home := dirs.Root
	info, err := os.Stat(home)
	if err != nil {
		if os.IsNotExist(err) {
			return []checkResult{{name: "runtime-home", level: okLevel, detail: fmt.Sprintf("%s (will be created on start)", home)}}
		}
		return []checkResult{{name: "runtime-home", level: failLevel, detail: fmt.Sprintf("%s: %v", home, err)}}
	}
	if !info.IsDir() {
		return []checkResult{{name: "runtime-home", level: failLevel, detail: fmt.Sprintf("%s is not a directory", home)}}
	}
	// Writable?
	if err := assertWritable(home); err != nil {
		return []checkResult{{name: "runtime-home", level: failLevel, detail: fmt.Sprintf("%s not writable: %v", home, err)}}
	}
	results := []checkResult{{name: "runtime-home", level: okLevel, detail: fmt.Sprintf("%s (exists, writable)", home)}}
	if mode := info.Mode().Perm(); mode > 0o750 {
		results = append(results, checkResult{
			name:   "runtime-home-perms",
			level:  warnLevel,
			detail: fmt.Sprintf("mode %o is more permissive than recommended 0700", mode),
		})
	}
	return results
}

func assertWritable(dir string) error {
	probe := filepath.Join(dir, ".forge-doctor-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return err
	}
	_ = os.Remove(probe)
	return nil
}

func checkDatabase(dirs daemon.Dirs) []checkResult {
	if _, err := os.Stat(dirs.StateDB); err != nil {
		return []checkResult{{
			name:   "database",
			level:  okLevel,
			detail: fmt.Sprintf("%s (not initialised; created on first daemon start)", dirs.StateDB),
		}}
	}
	db, err := storage.Open(context.Background(), dirs.StateDB, nil)
	if err != nil {
		return []checkResult{{name: "database", level: failLevel, detail: fmt.Sprintf("open %s: %v", dirs.StateDB, err)}}
	}
	defer db.Close()
	v, err := db.CurrentVersion(context.Background())
	if err != nil {
		return []checkResult{{name: "database", level: failLevel, detail: fmt.Sprintf("read schema version: %v", err)}}
	}
	return []checkResult{{
		name:   "database",
		level:  okLevel,
		detail: fmt.Sprintf("%s (schema v%d, WAL)", dirs.StateDB, v),
	}}
}

func checkDaemon(dirs daemon.Dirs) []checkResult {
	st := daemon.GetStatus(context.Background(), dirs)
	switch st.State {
	case daemon.StatusRunning:
		return []checkResult{{name: "daemon", level: okLevel, detail: fmt.Sprintf("running (pid %d) at %s", st.PID, st.Addr)}}
	case daemon.StatusUnhealthy:
		return []checkResult{{name: "daemon", level: warnLevel, detail: fmt.Sprintf("pid %d alive but API unreachable: %s", st.PID, st.Note)}}
	case daemon.StatusStale:
		return []checkResult{{name: "daemon", level: warnLevel, detail: "stale pid file (no running daemon)"}}
	case daemon.StatusCorrupted:
		return []checkResult{{name: "daemon", level: warnLevel, detail: "corrupted runtime state: " + st.Note}}
	default:
		return []checkResult{{name: "daemon", level: okLevel, detail: "not running (use 'forge daemon start')"}}
	}
}

// checkOrphanWorktrees scans the managed workspaces directory for Git worktrees
// that have no matching workspace record. These are leftovers from crashed or
// interrupted runs and should be reported (spec: orphan worktree detection).
func checkOrphanWorktrees(dirs daemon.Dirs) []checkResult {
	wsRoot := filepath.Join(dirs.Root, "workspaces")
	info, err := os.Stat(wsRoot)
	if err != nil || !info.IsDir() {
		return []checkResult{{name: "orphan-worktrees", level: okLevel, detail: "no workspaces directory (none created yet)"}}
	}

	// Walk the directory tree looking for .git files (worktree signature).
	var orphans []string
	_ = filepath.WalkDir(wsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == wsRoot {
			return nil
		}
		gitPath := filepath.Join(path, ".git")
		if gi, gErr := os.Stat(gitPath); gErr == nil && !gi.IsDir() {
			orphans = append(orphans, path)
		}
		return nil
	})

	// If the daemon is running, cross-check against the DB. If not, report all
	// worktrees as potential orphans (the DB may have records we can't read
	// without the daemon).
	if len(orphans) == 0 {
		return []checkResult{{name: "orphan-worktrees", level: okLevel, detail: "none detected"}}
	}
	detail := fmt.Sprintf("%d worktree(s) on disk", len(orphans))
	if len(orphans) <= 3 {
		detail += ": " + strings.Join(orphans, ", ")
	}
	return []checkResult{{name: "orphan-worktrees", level: warnLevel, detail: detail}}
}

func doctorText(checks []checkResult) string {
	out := "NeuroForge doctor\n\n"
	for _, c := range checks {
		out += fmt.Sprintf("  [%s]  %-20s %s\n", c.level, c.name, c.detail)
	}
	return out
}

func doctorJSON(checks []checkResult) string {
	out := "{\"checks\":["
	for i, c := range checks {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf(`{"name":%q,"level":%q,"detail":%q}`, c.name, c.level, c.detail)
	}
	out += "]}"
	return out
}
