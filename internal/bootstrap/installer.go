package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
)

// Installer is the platform-specific installer abstraction (§7.2 stage 5). Every
// concrete installer (homebrew, apt, winget, npm-global, manual) implements this
// interface. The bootstrap package never shells out directly — it goes through
// an Installer so tests inject a FakeInstaller (rule §33: no real system package
// installs in CI).
//
// Install MUST:
//   - refuse to run unless [Confirmer] approved the step (§36.17: no silent
//     install);
//   - refuse to escalate privilege unless the user explicitly approved sudo for
//     THIS step (§36.18: no silent privilege escalation);
//   - record the outcome in the installation manifest.
type Installer interface {
	// ID is the installer identifier (e.g. "brew", "apt", "npm-global",
	// "fake").
	ID() string
	// Platforms reports which GOOS values this installer supports.
	Platforms() []string
	// Install performs one step. It receives the confirmed step + the
	// confirmation token so it can assert approval was granted.
	Install(ctx context.Context, step InstallStep, conf Confirmation) (ManifestEntry, error)
}

// Confirmer is the explicit-confirmation gate (§7.2 stage 4). The wizard
// implementation asks the user once per distinct action; production wires a TTY
// confirmer, tests wire an auto-confirmer that still records what was asked.
type Confirmer interface {
	// ConfirmPlan asks the user to approve the whole plan. Required before any
	// step runs.
	ConfirmPlan(plan *InstallPlan) bool
	// ConfirmSudo asks for explicit approval of a privilege-escalating step
	// (§36.18). The shell-profile diff is shown via ConfirmShellProfile.
	ConfirmSudo(step InstallStep) bool
	// ConfirmShellProfile shows the shell-profile diff and asks for approval
	// before applying it (§7.2 stage 4).
	ConfirmShellProfile(diff string) bool
}

// Confirmation is the token proving the user approved the actions in a step.
// Installers MUST verify the token matches the step before acting (defense in
// depth against a caller that forgets to confirm).
type Confirmation struct {
	PlanApproved  bool
	SudoApproved  bool
	ShellApproved bool
	StepToolID    string
}

// ForStep returns a confirmation token scoped to a single step.
func (c Confirmation) ForStep(step InstallStep) Confirmation {
	return Confirmation{
		PlanApproved:  c.PlanApproved,
		SudoApproved:  c.SudoApproved,
		ShellApproved: c.ShellApproved,
		StepToolID:    step.ToolID,
	}
}

// ManifestEntry records the outcome of one install step (§7.2 stage 5 "каждый
// шаг записывается в installation manifest").
type ManifestEntry struct {
	Installer string
	ToolID    string
	Action    StepAction
	Outcome   string // "installed" | "skipped" | "failed" | "authenticated" | "started"
	Detail    string
}

// Manifest is the full installation manifest for one `forge init` run.
type Manifest struct {
	Profile Profile
	Entries []ManifestEntry
}

// --- installer registry ---

// Registry maps installer ids to implementations.
type Registry struct {
	mu         sync.Mutex
	installers []Installer
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds an installer (no silent override of duplicates).
func (r *Registry) Register(i Installer) error {
	if i == nil {
		return errors.New("bootstrap: nil installer")
	}
	if i.ID() == "" {
		return errors.New("bootstrap: installer with empty id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.installers {
		if existing.ID() == i.ID() {
			return fmt.Errorf("bootstrap: installer %q already registered", i.ID())
		}
	}
	r.installers = append(r.installers, i)
	return nil
}

// SelectForPlatform returns the first installer that supports the current OS.
func (r *Registry) SelectForPlatform() (Installer, error) {
	goos := runtime.GOOS
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, i := range r.installers {
		for _, p := range i.Platforms() {
			if p == goos || p == "*" {
				return i, nil
			}
		}
	}
	return nil, fmt.Errorf("bootstrap: no installer registered for %s", goos)
}

// --- the executor: runs a plan through the confirmation gate ---

// Executor applies an installation plan via an installer, enforcing every safety
// rule (§7.2 stage 4/5, §36.17/§36.18). It is the only thing that actually
// mutates the system during `forge init`.
type Executor struct {
	installer Installer
	confirmer Confirmer
}

// NewExecutor builds an executor. Both arguments are required: the confirmer
// guarantees nothing happens silently.
func NewExecutor(installer Installer, confirmer Confirmer) (*Executor, error) {
	if installer == nil {
		return nil, errors.New("bootstrap: nil installer")
	}
	if confirmer == nil {
		return nil, errors.New("bootstrap: nil confirmer (silent install forbidden §36.17)")
	}
	return &Executor{installer: installer, confirmer: confirmer}, nil
}

// ErrNotConfirmed is returned when a required confirmation was not granted. The
// executor aborts WITHOUT applying anything (no partial silent mutation).
var ErrNotConfirmed = errors.New("bootstrap: required confirmation not granted (no silent install/escalation §36.17/§36.18)")

// ErrShellProfileNotApproved is returned when a shell-profile change was not
// explicitly approved (§7.2 stage 4).
var ErrShellProfileNotApproved = errors.New("bootstrap: shell profile change not approved (must be shown as diff first §7.2 stage 4)")

// Apply runs the plan. It asks the confirmer for plan-level approval first, then
// per-step sudo/shell approval. On the FIRST unconfirmed step it aborts and
// returns ErrNotConfirmed / ErrShellProfileNotApproved; nothing after that runs.
func (e *Executor) Apply(ctx context.Context, plan *InstallPlan) (*Manifest, error) {
	if !e.confirmer.ConfirmPlan(plan) {
		return nil, ErrNotConfirmed
	}
	// Shell profile diff must be approved up-front if present (§7.2 stage 4).
	if plan.RequiresShellProfileChange && !e.confirmer.ConfirmShellProfile(plan.ShellProfileDiff) {
		return nil, ErrShellProfileNotApproved
	}
	baseConf := Confirmation{
		PlanApproved:  true,
		ShellApproved: plan.RequiresShellProfileChange,
	}
	manifest := &Manifest{Profile: plan.Profile}
	for _, step := range plan.WillInstall {
		// Sudo steps require explicit per-step confirmation (§36.18).
		if step.NeedsSudo && !e.confirmer.ConfirmSudo(step) {
			return manifest, fmt.Errorf("%w: sudo step %q not approved", ErrNotConfirmed, step.ToolID)
		}
		conf := baseConf.ForStep(step)
		conf.SudoApproved = step.NeedsSudo
		entry, err := e.installer.Install(ctx, step, conf)
		if err != nil {
			return manifest, fmt.Errorf("bootstrap: install %q failed: %w", step.ToolID, err)
		}
		manifest.Entries = append(manifest.Entries, entry)
	}
	return manifest, nil
}
