package bootstrap

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"
)

// CommandDetector is the production Detector: it shells out to READ-ONLY
// commands only (LookPath, --version probes). It never installs or mutates. It
// is the default detector wired by the CLI.
type CommandDetector struct {
	home string // override for tests; empty → os.UserHomeDir
}

// NewCommandDetector builds the default detector.
func NewCommandDetector() *CommandDetector { return &CommandDetector{} }

// LookPath wraps exec.LookPath.
func (d *CommandDetector) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// Output runs a read-only command and returns its stdout. It does NOT pass any
// stdin and uses a short timeout. Errors (missing binary, non-zero exit) yield
// an empty result + error.
func (d *CommandDetector) Output(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// UserShell returns the login shell.
func (d *CommandDetector) UserShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	if u, err := user.Current(); err == nil {
		_ = u
	}
	return "/bin/sh"
}

// HomeDir returns the user home directory.
func (d *CommandDetector) HomeDir() string {
	if d.home != "" {
		return d.home
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// IsElevated reports whether the process is running with elevated privileges.
func (d *CommandDetector) IsElevated() bool { return isElevated() }

// TTYConfirmer is the production Confirmer: it asks on the TTY and requires a
// real answer. It NEVER auto-approves (no silent install/escalation). When the
// input is not interactive, it DENIES (safe default) unless --yes was passed.
type TTYConfirmer struct {
	in  *bufio.Reader
	out io.Writer
	yes bool // forge init --yes: still prints, but auto-approves interactively
}

// NewTTYConfirmer builds a confirmer reading from in and prompting to out.
func NewTTYConfirmer(in io.Reader, out io.Writer, yes bool) *TTYConfirmer {
	return &TTYConfirmer{in: bufio.NewReader(in), out: out, yes: yes}
}

// ConfirmPlan asks for whole-plan approval.
func (c *TTYConfirmer) ConfirmPlan(plan *InstallPlan) bool {
	if c.yes {
		fmt.Fprintln(c.out, "Applying plan (--yes).")
		return true
	}
	return c.ask("Proceed with this installation plan? [y/N]: ")
}

// ConfirmSudo asks for per-step sudo approval (§36.18).
func (c *TTYConfirmer) ConfirmSudo(step InstallStep) bool {
	if c.yes {
		fmt.Fprintf(c.out, "Step %q needs privilege escalation (--yes).\n", step.ToolID)
		return true
	}
	return c.ask(fmt.Sprintf("Step %q needs sudo/privilege escalation. Approve THIS step only? [y/N]: ", step.ToolID))
}

// ConfirmShellProfile shows the diff and asks before applying (§7.2 stage 4).
func (c *TTYConfirmer) ConfirmShellProfile(diff string) bool {
	fmt.Fprintln(c.out, "NeuroForge wants to modify your shell profile:")
	fmt.Fprintln(c.out, diff)
	if c.yes {
		fmt.Fprintln(c.out, "Applying shell profile change (--yes).")
		return true
	}
	return c.ask("Apply these shell profile changes? [y/N]: ")
}

func (c *TTYConfirmer) ask(prompt string) bool {
	fmt.Fprint(c.out, prompt)
	line, err := c.in.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}
