package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkspaceRun_PromptAndPromptFileMutuallyExclusive proves --prompt and
// --prompt-file cannot be combined (blocker 3). This is validated CLI-side
// before any daemon connection.
func TestWorkspaceRun_PromptAndPromptFileMutuallyExclusive(t *testing.T) {
	a, _, errOut := newTestApp(t)
	code := a.Run([]string{"workspace", "run", "ws-1", "--prompt", "hi", "--prompt-file", "/tmp/x"})
	if code != ExitErr {
		t.Fatalf("code = %d, want ExitErr", code)
	}
	if !strings.Contains(errOut.String(), "mutually exclusive") {
		t.Errorf("stderr = %q, want mutual-exclusion message", errOut.String())
	}
}

// TestWorkspaceRun_PromptFileReadMissingFile proves a missing --prompt-file is
// reported clearly (the CLI reads the file locally).
func TestWorkspaceRun_PromptFileReadMissingFile(t *testing.T) {
	a, _, errOut := newTestApp(t)
	code := a.Run([]string{"workspace", "run", "ws-1", "--prompt-file", "/no/such/file.md"})
	if code != ExitErr {
		t.Fatalf("code = %d, want ExitErr", code)
	}
	if !strings.Contains(errOut.String(), "read --prompt-file") {
		t.Errorf("stderr = %q, want read --prompt-file error", errOut.String())
	}
}

// TestWorkspaceRun_PromptFileReadSucceeds proves a present --prompt-file is
// read and the run proceeds past the CLI (reaching the daemon connect step,
// which fails because no daemon is running — proving the file content was
// consumed, not the path forwarded).
func TestWorkspaceRun_PromptFileReadSucceeds(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "task with spaces.md")
	if err := os.WriteFile(promptPath, []byte("# Task\n\nDo the thing."), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _, errOut := newTestApp(t)
	code := a.Run([]string{"workspace", "run", "ws-1", "--prompt-file", promptPath, "--engine", "fake"})
	// No daemon running → ExitErr, but the error must be about connecting to
	// the daemon, NOT about reading the prompt file (proving it was read).
	if code != ExitErr {
		t.Fatalf("code = %d, want ExitErr (no daemon)", code)
	}
	if strings.Contains(errOut.String(), "read --prompt-file") {
		t.Errorf("prompt file read failed unexpectedly: %s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "daemon") && !strings.Contains(errOut.String(), "connect") {
		t.Errorf("stderr = %q, want a daemon-connect error (file should have been read)", errOut.String())
	}
}

// TestWorkspaceRun_MissingIDErrors proves a missing workspace id is rejected.
func TestWorkspaceRun_MissingIDErrors(t *testing.T) {
	a, _, errOut := newTestApp(t)
	code := a.Run([]string{"workspace", "run", "--engine", "opencode"})
	if code != ExitErr {
		t.Fatalf("code = %d, want ExitErr", code)
	}
	if !strings.Contains(errOut.String(), "Usage") {
		t.Errorf("stderr = %q, want usage message", errOut.String())
	}
}

// TestWorkspaceRun_MultipleIDsRejected proves more than one positional id is an
// error (blocker 2 requirement: several workspace IDs → error).
func TestWorkspaceRun_MultipleIDsRejected(t *testing.T) {
	a, _, errOut := newTestApp(t)
	code := a.Run([]string{"workspace", "run", "ws-1", "ws-2", "--engine", "fake"})
	if code != ExitErr {
		t.Fatalf("code = %d, want ExitErr", code)
	}
	if !strings.Contains(errOut.String(), "exactly one workspace id") {
		t.Errorf("stderr = %q, want single-id message", errOut.String())
	}
}

// TestWorkspaceRun_UnknownFlagErrors proves an unknown flag is rejected in both
// orderings (blocker 2: unknown flag → error, never silently ignored).
func TestWorkspaceRun_UnknownFlagErrors(t *testing.T) {
	for _, args := range [][]string{
		{"workspace", "run", "ws-1", "--bogus", "x"},
		{"workspace", "run", "--bogus", "x", "ws-1"},
	} {
		a, _, _ := newTestApp(t)
		code := a.Run(args)
		if code != ExitErr {
			t.Errorf("args=%v: code = %d, want ExitErr", args, code)
		}
	}
}
