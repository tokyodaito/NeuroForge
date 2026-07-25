package runapp_test

import (
	"os/exec"
	"strings"
	"testing"
)

// runGit runs git in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// readHeadSHA returns `git rev-parse HEAD` in dir.
func readHeadSHA(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gitOutput(t, "git", "-C", dir, "rev-parse", "HEAD"))
}

// gitOutput runs a git command and returns its stdout (failure is fatal).
func gitOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return string(out)
}
