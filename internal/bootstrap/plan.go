package bootstrap

import (
	"fmt"
	"sort"
	"strings"
)

// InstallStep is one concrete action the installer will perform. It is computed
// by diffing the profile's requests against what the system scan already found
// (a tool that is present is NOT reinstalled — §7.2 stage 4 "удалять
// существующие версии" is forbidden).
type InstallStep struct {
	ToolID      string
	Category    ToolCategory
	Action      StepAction
	Reason      string
	InstallHint string
	NeedsSudo   bool
	// ShellProfileChange is the proposed line to add to the shell profile. The
	// plan shows this as a diff BEFORE applying it (§7.2 stage 4). Empty = no
	// shell change.
	ShellProfileChange string
}

// StepAction classifies what a step does.
type StepAction string

const (
	ActionInstall     StepAction = "install"        // install a missing tool
	ActionSkipHave    StepAction = "skip_have"      // already present, do nothing
	ActionSkipGlobal  StepAction = "skip_no_global" // --no-global: would-be global install skipped
	ActionAuth        StepAction = "authenticate"   // launch the official provider auth flow
	ActionStartDaemon StepAction = "start_daemon"
)

// InstallPlan is the §7.2 stage-3 installation plan. It is computed WITHOUT
// touching the system, so `--dry-run` simply renders it (AC-25).
type InstallPlan struct {
	Profile     Profile
	WillInstall []InstallStep
	WontInstall []InstallStep
	// ShellProfileDiff is the unified diff of proposed shell-profile changes
	// (§7.2 stage 4). Shown to the user before any change is applied.
	ShellProfileDiff string
	// RequiresSudo summarises whether ANY step needs privilege escalation
	// (§36.18). The wizard gates this behind explicit confirmation.
	RequiresSudo bool
	// RequiresShellProfileChange summarises whether ANY step touches the shell
	// profile.
	RequiresShellProfileChange bool
}

// PlanOptions controls plan computation.
type PlanOptions struct {
	Profile Profile
	Scan    SystemScan
	// CustomSelection is only honoured for ProfileCustom.
	CustomSelection []string
	// NoGlobal disables installing global packages (forge init --no-global).
	NoGlobal bool
	// Offline suppresses any network calls during init (forge init --offline).
	Offline bool
	// SkipAgents skips coding-agent installation (forge init --skip-agents).
	SkipAgents bool
	// ShellProfilePath is the target shell profile (e.g. ~/.zshrc) used to render
	// the diff header.
	ShellProfilePath string
}

// ComputePlan builds the installation plan by diffing the profile against the
// scan. It is pure: it never installs, never escalates, never writes the shell
// profile (AC-25: --dry-run is just rendering this).
func ComputePlan(opts PlanOptions) (*InstallPlan, error) {
	if !opts.Profile.IsValid() {
		return nil, fmt.Errorf("bootstrap: invalid profile %q", opts.Profile)
	}
	spec := ProfileSpecFor(opts.Profile)
	if opts.Profile == ProfileCustom {
		spec = customSpec(opts.CustomSelection)
	}
	if opts.SkipAgents {
		filtered := spec.Tools[:0]
		for _, t := range spec.Tools {
			if t.Category != CatCodingAgent {
				filtered = append(filtered, t)
			}
		}
		spec.Tools = filtered
	}

	plan := &InstallPlan{Profile: opts.Profile}
	for _, req := range spec.SortedTools() {
		step := InstallStep{
			ToolID: req.ID, Category: req.Category, InstallHint: req.InstallHint,
			NeedsSudo: req.NeedsSudo, ShellProfileChange: req.ShellProfileChange,
		}
		if present, _ := opts.Scan.Find(req.ID); present.Present {
			step.Action = ActionSkipHave
			step.Reason = fmt.Sprintf("already installed (%s)", firstNonEmpty(present.Version, "detected"))
			plan.WontInstall = append(plan.WontInstall, step)
			continue
		}
		if opts.NoGlobal && isGlobalInstall(req) {
			step.Action = ActionSkipGlobal
			step.Reason = "--no-global: global package install skipped"
			plan.WontInstall = append(plan.WontInstall, step)
			continue
		}
		if req.Category == CatCodingAgent || req.Category == CatImageProvider {
			// Auth is a separate step shown in the plan; never asked for a
			// password here (§7.2 stage 6).
			plan.WillInstall = append(plan.WillInstall, step)
			authStep := step
			authStep.Action = ActionAuth
			authStep.Reason = "launch official provider login (no password requested in NeuroForge)"
			plan.WillInstall = append(plan.WillInstall, authStep)
			continue
		}
		if req.Category == CatDaemon {
			step.Action = ActionStartDaemon
			plan.WillInstall = append(plan.WillInstall, step)
			continue
		}
		step.Action = ActionInstall
		step.Reason = "not present"
		plan.WillInstall = append(plan.WillInstall, step)
	}

	// Aggregate flags + build the shell-profile diff.
	for i := range plan.WillInstall {
		s := &plan.WillInstall[i]
		if s.NeedsSudo {
			plan.RequiresSudo = true
		}
		if s.ShellProfileChange != "" {
			plan.RequiresShellProfileChange = true
		}
	}
	plan.ShellProfileDiff = renderShellDiff(opts.ShellProfilePath, plan.WillInstall)
	return plan, nil
}

func customSpec(selection []string) ProfileSpec {
	spec := ProfileSpec{Profile: ProfileCustom}
	want := map[string]bool{}
	for _, id := range selection {
		want[id] = true
	}
	// Pull the canonical tool requests for every known tool.
	known := map[string]ToolRequest{}
	for _, p := range []Profile{ProfileMinimal, ProfileStandard, ProfileAndroid, ProfileWeb, ProfileFull} {
		for _, t := range ProfileSpecFor(p).Tools {
			if _, ok := known[t.ID]; !ok {
				known[t.ID] = t
			}
		}
	}
	var ids []string
	for id := range known {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if want[id] {
			spec.Tools = append(spec.Tools, known[id])
		}
	}
	return spec
}

func isGlobalInstall(req ToolRequest) bool {
	switch req.Category {
	case CatCodingAgent, CatRuntime, CatVCS, CatContainer:
		return true
	}
	return false
}

// renderShellDiff produces a unified-style diff of the proposed shell-profile
// additions (§7.2 stage 4). Only proposed additions are shown (we never remove
// existing lines). The diff is shown BEFORE any change is applied.
func renderShellDiff(profilePath string, steps []InstallStep) string {
	var additions []string
	for _, s := range steps {
		if s.ShellProfileChange != "" {
			additions = append(additions, s.ShellProfileChange)
		}
	}
	if len(additions) == 0 {
		return ""
	}
	// Stable order.
	sort.Strings(additions)
	var b strings.Builder
	if profilePath != "" {
		b.WriteString("--- " + profilePath + " (current)\n")
		b.WriteString("+++ " + profilePath + " (proposed)\n")
	}
	for _, line := range additions {
		b.WriteString("+")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// RenderPlan produces the human-readable §7.2 stage-3 view ("Будет установлено /
// Не будет установлено"). It is what `forge init --dry-run` prints (AC-25).
func RenderPlan(p *InstallPlan) string {
	var b strings.Builder
	b.WriteString("Profile: ")
	b.WriteString(string(p.Profile))
	b.WriteString("\n\nБудет установлено / выполнено:\n")
	if len(p.WillInstall) == 0 {
		b.WriteString("  (nothing)\n")
	}
	for _, s := range p.WillInstall {
		b.WriteString(fmt.Sprintf("  - %-18s [%s] %s\n", s.ToolID, s.Action, s.InstallHint))
		if s.NeedsSudo {
			b.WriteString("      (requires privilege escalation — will ask)\n")
		}
	}
	b.WriteString("\nНе будет установлено:\n")
	if len(p.WontInstall) == 0 {
		b.WriteString("  (nothing)\n")
	}
	for _, s := range p.WontInstall {
		b.WriteString(fmt.Sprintf("  - %-18s %s\n", s.ToolID, s.Reason))
	}
	if p.RequiresSudo {
		b.WriteString("\nNOTE: some steps require privilege escalation. NeuroForge will NEVER\n")
		b.WriteString("escalate silently — each such step asks for explicit confirmation (§36.18).\n")
	}
	if p.ShellProfileDiff != "" {
		b.WriteString("\nProposed shell profile changes (shown before applying):\n")
		b.WriteString(p.ShellProfileDiff)
	}
	return b.String()
}
