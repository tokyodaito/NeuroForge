package bootstrap

import (
	"context"
	"sync"
)

// FakeInstaller is a no-op installer used by tests and CI (rule §33: installer
// tests never install real system packages). It records every step it would
// perform and asserts the confirmation token is present (defence in depth).
type FakeInstaller struct {
	mu           sync.Mutex
	steps        []InstallStep
	failOn       map[string]error // toolID -> error to inject
	sudoAsserted bool
}

// NewFakeInstaller returns a fake installer that succeeds for every step unless
// FailOn is configured.
func NewFakeInstaller() *FakeInstaller {
	return &FakeInstaller{failOn: map[string]error{}}
}

// ID reports the installer id.
func (f *FakeInstaller) ID() string { return "fake" }

// Platforms reports universal support (so tests run on any GOOS).
func (f *FakeInstaller) Platforms() []string { return []string{"*"} }

// FailOn makes the next Install for toolID return err (deterministic).
func (f *FakeInstaller) FailOn(toolID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[toolID] = err
}

// Install records the step and returns a successful manifest entry. It asserts
// plan-level confirmation is present (no silent install).
func (f *FakeInstaller) Install(_ context.Context, step InstallStep, conf Confirmation) (ManifestEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !conf.PlanApproved {
		return ManifestEntry{}, ErrNotConfirmed
	}
	if step.NeedsSudo && !conf.SudoApproved {
		return ManifestEntry{}, ErrNotConfirmed
	}
	if step.ShellProfileChange != "" && !conf.ShellApproved {
		return ManifestEntry{}, ErrShellProfileNotApproved
	}
	if step.ToolID == conf.StepToolID {
		f.sudoAsserted = step.NeedsSudo
	}
	f.steps = append(f.steps, step)
	if err, ok := f.failOn[step.ToolID]; ok {
		return ManifestEntry{
			Installer: f.ID(), ToolID: step.ToolID, Action: step.Action,
			Outcome: "failed", Detail: err.Error(),
		}, err
	}
	outcome := "installed"
	switch step.Action {
	case ActionAuth:
		outcome = "authenticated"
	case ActionStartDaemon:
		outcome = "started"
	case ActionSkipHave, ActionSkipGlobal:
		outcome = "skipped"
	}
	return ManifestEntry{
		Installer: f.ID(), ToolID: step.ToolID, Action: step.Action,
		Outcome: outcome,
	}, nil
}

// Steps returns the recorded steps (what the fake "did").
func (f *FakeInstaller) Steps() []InstallStep {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]InstallStep, len(f.steps))
	copy(out, f.steps)
	return out
}

// RecordedToolIDs returns the tool ids the fake installed, in order.
func (f *FakeInstaller) RecordedToolIDs() []string {
	steps := f.Steps()
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.ToolID)
	}
	return out
}

// --- FakeDetector (read-only environment probe for tests/CI) ---

// FakeDetector returns canned scan results. It performs NO real command
// execution (rule §33).
type FakeDetector struct {
	paths    map[string]string // name -> path
	outputs  map[string]string // name -> version output
	shell    string
	home     string
	elevated bool
}

// NewFakeDetector builds a detector with the given canned lookups.
func NewFakeDetector(paths, outputs map[string]string, shell, home string) *FakeDetector {
	return &FakeDetector{paths: paths, outputs: outputs, shell: shell, home: home}
}

// SetElevated marks the process as elevated (to test the no-silent-escalation
// warning).
func (d *FakeDetector) SetElevated(v bool) { d.elevated = v }

// AddPath adds (or overrides) a canned LookPath result.
func (d *FakeDetector) AddPath(name, path string) {
	if d.paths == nil {
		d.paths = map[string]string{}
	}
	d.paths[name] = path
}

// LookPath returns the canned path, if present.
func (d *FakeDetector) LookPath(name string) (string, error) {
	if p, ok := d.paths[name]; ok {
		return p, nil
	}
	return "", errNotFound
}

// Output returns the canned version output for the command name, if present.
func (d *FakeDetector) Output(_ context.Context, name string, _ ...string) (string, error) {
	if out, ok := d.outputs[name]; ok {
		return out, nil
	}
	return "", errNotFound
}

// UserShell returns the canned shell.
func (d *FakeDetector) UserShell() string { return d.shell }

// HomeDir returns the canned home.
func (d *FakeDetector) HomeDir() string { return d.home }

// IsElevated reports the canned elevation flag.
func (d *FakeDetector) IsElevated() bool { return d.elevated }

// --- recording confirmer ---

// AutoConfirmer approves everything but records what it was asked. Used by tests
// to verify the wizard actually asked for plan/sudo/shell approval.
type AutoConfirmer struct {
	mu           sync.Mutex
	planAsked    int
	sudoAsked    []InstallStep
	shellAsked   int
	approvePlan  bool
	approveSudo  bool
	approveShell bool
}

// NewAutoConfirmer builds a confirmer that approves (or denies) everything
// according to the flags.
func NewAutoConfirmer(approvePlan, approveSudo, approveShell bool) *AutoConfirmer {
	return &AutoConfirmer{approvePlan: approvePlan, approveSudo: approveSudo, approveShell: approveShell}
}

// ConfirmPlan records + returns the plan decision.
func (c *AutoConfirmer) ConfirmPlan(_ *InstallPlan) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.planAsked++
	return c.approvePlan
}

// ConfirmSudo records + returns the per-step sudo decision.
func (c *AutoConfirmer) ConfirmSudo(step InstallStep) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sudoAsked = append(c.sudoAsked, step)
	return c.approveSudo
}

// ConfirmShellProfile records + returns the shell-profile decision.
func (c *AutoConfirmer) ConfirmShellProfile(_ string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shellAsked++
	return c.approveShell
}

// PlanAsked reports how many times the plan was confirmed.
func (c *AutoConfirmer) PlanAsked() int { c.mu.Lock(); defer c.mu.Unlock(); return c.planAsked }

// ShellAsked reports how many times the shell diff was confirmed.
func (c *AutoConfirmer) ShellAsked() int { c.mu.Lock(); defer c.mu.Unlock(); return c.shellAsked }

// SudoAskedFor reports whether sudo was asked for the given tool.
func (c *AutoConfirmer) SudoAskedFor(toolID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sudoAsked {
		if s.ToolID == toolID {
			return true
		}
	}
	return false
}
