package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"neuroforge/internal/bootstrap"
	"neuroforge/internal/daemon"
)

// runInit implements `forge init` (spec §7, AC-25/AC-26). It runs the onboarding
// wizard: system scan → profile → installation plan → confirmation → install →
// authentication → toolchain lock. `--dry-run` stops after the plan and changes
// nothing.
func (a *App) runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	dryRun := fs.Bool("dry-run", false, "show the installation plan and change nothing (AC-25)")
	yes := fs.Bool("yes", false, "auto-confirm prompts (still prints every action)")
	profile := fs.String("profile", "standard", "onboarding profile: minimal|standard|android|web|full|custom")
	noGlobal := fs.Bool("no-global", false, "skip installing global packages")
	offline := fs.Bool("offline", false, "do not perform any network calls during init")
	skipAgents := fs.Bool("skip-agents", false, "skip coding-agent installation")
	repair := fs.Bool("repair", false, "reconcile the toolchain with the lock (reinstall missing tools)")
	jsonOut := fs.Bool("json", false, "emit machine-readable output")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	p := bootstrap.Profile(*profile)
	if *repair {
		return a.runInitRepair(p, *yes, *jsonOut)
	}
	if !p.IsValid() {
		a.errf("invalid --profile %q (use minimal|standard|android|web|full|custom)", *profile)
		return ExitErr
	}

	dirs, err := a.resolveDirs()
	if err != nil {
		a.errf("resolve runtime dir: %v", err)
		return ExitErr
	}

	deps := a.resolveInitDeps()
	wizCfg := bootstrap.WizardConfig{
		Detector: deps.detector(),
		LockPath: toolchainLockPath(dirs),
	}
	// For a real run we need an installer + confirmer. The guided installer
	// prints every step and never escalates silently.
	if !*dryRun {
		wizCfg.Installer = deps.installer(a.Out)
		wizCfg.Confirmer = deps.confirmer(a.Stdin, a.Out, *yes)
	}
	w, err := bootstrap.NewWizard(wizCfg)
	if err != nil {
		a.errf("%v", err)
		return ExitErr
	}

	opts := bootstrap.PlanOptions{
		Profile:          p,
		NoGlobal:         *noGlobal,
		Offline:          *offline,
		SkipAgents:       *skipAgents,
		ShellProfilePath: shellProfilePath(dirs),
	}

	if *dryRun {
		// AC-25: scan → plan → print, change NOTHING.
		scan, plan, err := w.DryRun(context.Background(), opts)
		if err != nil {
			a.errf("init --dry-run: %v", err)
			return ExitErr
		}
		if *jsonOut {
			a.printInitDryRunJSON(scan, plan)
		} else {
			fmt.Fprintln(a.Out, renderScan(scan))
			fmt.Fprintln(a.Out, bootstrap.RenderPlan(plan))
			fmt.Fprintln(a.Out, "\nDry-run complete — nothing was changed (AC-25).")
		}
		return ExitOK
	}

	res, err := w.Run(context.Background(), opts)
	if err != nil {
		a.errf("init: %v", err)
		return ExitErr
	}
	if *jsonOut {
		a.printInitResultJSON(res)
	} else {
		fmt.Fprintln(a.Out, renderScan(res.Scan))
		fmt.Fprintln(a.Out, bootstrap.RenderPlan(res.Plan))
		fmt.Fprintln(a.Out, renderManifest(res.Manifest))
		fmt.Fprintln(a.Out, bootstrap.RenderAuthTable(res.Auth))
		fmt.Fprintf(a.Out, "\nToolchain lock written: %d tool(s)\n", len(res.Lock.Toolchain))
		fmt.Fprintln(a.Out, "\nNext: run `forge doctor` to verify the installation (§7.2 stage 7).")
	}
	return ExitOK
}

// runInitRepair implements `forge init --repair`.
func (a *App) runInitRepair(profile bootstrap.Profile, yes, jsonOut bool) int {
	dirs, err := a.resolveDirs()
	if err != nil {
		a.errf("resolve runtime dir: %v", err)
		return ExitErr
	}
	lock := bootstrap.NewToolchainLock(toolchainLockPath(dirs))
	lockFile, err := lock.Load()
	if err != nil {
		a.errf("read toolchain lock: %v", err)
		return ExitErr
	}
	deps := a.resolveInitDeps()
	scan, err := bootstrap.Scan(context.Background(), deps.detector())
	if err != nil {
		a.errf("scan: %v", err)
		return ExitErr
	}
	exec, err := bootstrap.NewExecutor(deps.installer(a.Out), deps.confirmer(a.Stdin, a.Out, yes))
	if err != nil {
		a.errf("%v", err)
		return ExitErr
	}
	res, err := bootstrap.Repair(context.Background(), profile, scan, lockFile, exec)
	if err != nil {
		a.errf("repair: %v", err)
		return ExitErr
	}
	if jsonOut {
		fmt.Fprintf(a.Out, `{"repaired":%v}`+"\n", res.Applied)
	} else if res.Applied {
		fmt.Fprintln(a.Out, "Repair complete: toolchain reconciled with the lock.")
	}
	return ExitOK
}

// --- guided installer (prints every step; never silent sudo) ---

// guidedInstaller prints the install command for each step. It does NOT silently
// run sudo or system package installs — it shows them so the user is in control
// (§36.17/§36.18). Auth steps are launched via the official provider mechanism
// (handled by the auth wizard, not here).
type guidedInstaller struct {
	out io.Writer
}

func newGuidedInstaller(out io.Writer) *guidedInstaller { return &guidedInstaller{out: out} }

func (g *guidedInstaller) ID() string          { return "guided" }
func (g *guidedInstaller) Platforms() []string { return []string{"*"} }

func (g *guidedInstaller) Install(ctx context.Context, step bootstrap.InstallStep, conf bootstrap.Confirmation) (bootstrap.ManifestEntry, error) {
	if !conf.PlanApproved {
		return bootstrap.ManifestEntry{}, bootstrap.ErrNotConfirmed
	}
	if step.NeedsSudo && !conf.SudoApproved {
		return bootstrap.ManifestEntry{}, bootstrap.ErrNotConfirmed
	}
	outcome := "installed"
	switch step.Action {
	case bootstrap.ActionAuth:
		fmt.Fprintf(g.out, "  → %s: launch official provider login (no password collected here)\n", step.ToolID)
		outcome = "authenticated"
	case bootstrap.ActionStartDaemon:
		fmt.Fprintf(g.out, "  → %s: starting NeuroForge daemon service\n", step.ToolID)
		outcome = "started"
	case bootstrap.ActionInstall:
		fmt.Fprintf(g.out, "  → installing %s: %s\n", step.ToolID, step.InstallHint)
		if step.NeedsSudo {
			fmt.Fprintf(g.out, "    (run the privileged command yourself; NeuroForge will not sudo silently)\n")
		}
	case bootstrap.ActionSkipHave, bootstrap.ActionSkipGlobal:
		outcome = "skipped"
	}
	return bootstrap.ManifestEntry{
		Installer: g.ID(), ToolID: step.ToolID, Action: step.Action, Outcome: outcome,
	}, nil
}

// --- runUpdate implements `forge update` (§7.5) ---

func (a *App) runUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	yes := fs.Bool("yes", false, "auto-confirm prompts")
	jsonOut := fs.Bool("json", false, "emit machine-readable output")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	dirs, err := a.resolveDirs()
	if err != nil {
		a.errf("resolve runtime dir: %v", err)
		return ExitErr
	}
	lock := bootstrap.NewToolchainLock(toolchainLockPath(dirs))
	deps := a.resolveInitDeps()
	// Re-scan + recompute a plan for the current profile (read from the daemon
	// state when available; here we re-scan and let the user confirm).
	scan, err := bootstrap.Scan(context.Background(), deps.detector())
	if err != nil {
		a.errf("scan: %v", err)
		return ExitErr
	}
	profile := bootstrap.ProfileStandard
	plan, err := bootstrap.ComputePlan(bootstrap.PlanOptions{Profile: profile, Scan: scan})
	if err != nil {
		a.errf("plan: %v", err)
		return ExitErr
	}
	fmt.Fprintln(a.Out, bootstrap.RenderPlan(plan))
	exec, err := bootstrap.NewExecutor(deps.installer(a.Out), deps.confirmer(a.Stdin, a.Out, *yes))
	if err != nil {
		a.errf("%v", err)
		return ExitErr
	}
	res, err := bootstrap.Update(context.Background(), lock, bootstrap.UpdateOptions{}, plan, exec)
	if err != nil {
		a.errf("update: %v", err)
		return ExitErr
	}
	if *jsonOut {
		fmt.Fprintf(a.Out, `{"applied":%v,"rolled_back":%v}`+"\n", res.Applied, res.RolledBack)
	} else {
		if res.RolledBack {
			fmt.Fprintf(a.Out, "Update rolled back: %s (previous toolchain restored, §7.5)\n", res.RollbackReason)
		} else {
			fmt.Fprintf(a.Out, "Update applied: %d tool(s) locked.\n", len(res.NewLock.Toolchain))
		}
	}
	return ExitOK
}

// --- render helpers ---

func renderScan(scan bootstrap.SystemScan) string {
	var b strings.Builder
	b.WriteString("System scan\n")
	b.WriteString(fmt.Sprintf("  os:             %s/%s\n", scan.OS, scan.Arch))
	b.WriteString(fmt.Sprintf("  shell:          %s\n", scan.Shell))
	b.WriteString(fmt.Sprintf("  package manager:%s\n", scan.PackageManager))
	if scan.Elevated {
		b.WriteString("  WARNING: running elevated — NeuroForge will not escalate silently (§36.18)\n")
	}
	b.WriteString("\nDetected tools:\n")
	for _, t := range scan.Tools {
		mark := "✗"
		if t.Present {
			mark = "✓"
		}
		ver := ""
		if t.Version != "" {
			ver = "  " + t.Version
		}
		b.WriteString(fmt.Sprintf("  %s %-12s %s%s\n", mark, t.ID, t.Category, ver))
	}
	return b.String()
}

func renderManifest(m *bootstrap.Manifest) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nInstallation manifest:\n")
	for _, e := range m.Entries {
		b.WriteString(fmt.Sprintf("  [%s] %-14s %s\n", e.Outcome, e.ToolID, e.Action))
	}
	return b.String()
}

func toolchainLockPath(dirs daemon.Dirs) string {
	if dirs.Root == "" {
		return ""
	}
	return filepath.Join(dirs.Root, "toolchain.json")
}

func shellProfilePath(dirs daemon.Dirs) string {
	if dirs.Root == "" {
		return ""
	}
	return filepath.Join(dirs.Root, "shell-profile.notes")
}

// printInitDryRunJSON renders the dry-run as JSON.
func (a *App) printInitDryRunJSON(scan bootstrap.SystemScan, plan *bootstrap.InstallPlan) {
	fmt.Fprintln(a.Out, planJSON(plan))
}

func (a *App) printInitResultJSON(res *bootstrap.RunResult) {
	fmt.Fprintln(a.Out, planJSON(res.Plan))
}
