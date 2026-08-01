package testengine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeModule creates a stdlib-only Go module fixture (offline-safe).
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module example.com/fixture\n\ngo 1.21\n"
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

const passingModuleFile = `package calc

func Add(a, b int) int {
	return a + b
}
`

const passingTestFile = `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatalf("Add(2, 3) != 5")
	}
}
`

func newRunner(t *testing.T) *ShellRunner {
	t.Helper()
	return NewShellRunner(ShellRunnerOptions{CmdTimeout: 2 * time.Minute})
}

func TestShellRunnerSyntaxClean(t *testing.T) {
	dir := writeModule(t, map[string]string{"calc/calc.go": passingModuleFile})
	res, err := newRunner(t).Run(context.Background(), RunRequest{Level: LevelSyntax, WorkspacePath: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusPassed {
		t.Fatalf("status = %s, want passed (output: %s)", res.Status, res.SlicedOutput)
	}
}

func TestShellRunnerSyntaxViolation(t *testing.T) {
	// Valid Go, but space-indented: gofmt -l must list it.
	bad := "package calc\n\nfunc Add(a, b int) int {\n        return a + b\n}\n"
	dir := writeModule(t, map[string]string{"calc/calc.go": bad})
	res, err := newRunner(t).Run(context.Background(), RunRequest{Level: LevelSyntax, WorkspacePath: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if len(res.Failures) == 0 {
		t.Fatal("no failures extracted from gofmt output")
	}
	if !strings.HasSuffix(res.Failures[0].File, "calc.go") {
		t.Fatalf("failure file = %q, want calc.go", res.Failures[0].File)
	}
}

func TestShellRunnerCompileOK(t *testing.T) {
	dir := writeModule(t, map[string]string{"calc/calc.go": passingModuleFile})
	res, err := newRunner(t).Run(context.Background(), RunRequest{Level: LevelCompile, WorkspacePath: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusPassed {
		t.Fatalf("status = %s, want passed (output: %s)", res.Status, res.SlicedOutput)
	}
}

func TestShellRunnerCompileError(t *testing.T) {
	broken := "package calc\n\nfunc Add(a, b int) int {\n\treturn undefinedFn(a, b)\n}\n"
	dir := writeModule(t, map[string]string{"calc/calc.go": broken})
	res, err := newRunner(t).Run(context.Background(), RunRequest{Level: LevelCompile, WorkspacePath: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if len(res.Failures) == 0 {
		t.Fatalf("no failures extracted (output: %s)", res.SlicedOutput)
	}
	f := res.Failures[0]
	if !strings.HasSuffix(f.File, "calc.go") {
		t.Fatalf("failure file = %q, want calc.go", f.File)
	}
	if f.Line <= 0 {
		t.Fatalf("failure line = %d, want > 0", f.Line)
	}
	if !strings.Contains(f.Message, "undefinedFn") {
		t.Fatalf("failure message = %q, want mention of undefinedFn", f.Message)
	}
}

func TestShellRunnerTargetedPassing(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"calc/calc.go":      passingModuleFile,
		"calc/calc_test.go": passingTestFile,
	})
	res, err := newRunner(t).Run(context.Background(), RunRequest{
		Level:         LevelTargeted,
		WorkspacePath: dir,
		ChangedFiles:  []string{"calc/calc.go", "calc/calc_test.go", "README.md"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusPassed {
		t.Fatalf("status = %s, want passed (output: %s)", res.Status, res.SlicedOutput)
	}
	if res.Passed < 1 {
		t.Fatalf("passed = %d, want >= 1 (output: %s)", res.Passed, res.SlicedOutput)
	}
}

func TestShellRunnerTargetedNoGoFiles(t *testing.T) {
	dir := writeModule(t, map[string]string{"calc/calc.go": passingModuleFile})
	res, err := newRunner(t).Run(context.Background(), RunRequest{
		Level:         LevelTargeted,
		WorkspacePath: dir,
		ChangedFiles:  []string{"README.md", "docs/spec.txt"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusSkipped {
		t.Fatalf("status = %s, want skipped", res.Status)
	}
}

func TestShellRunnerTargetedFailing(t *testing.T) {
	failing := `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 42 {
		t.Fatalf("Add(2, 3) = %d, want 42", Add(2, 3))
	}
}
`
	dir := writeModule(t, map[string]string{
		"calc/calc.go":      passingModuleFile,
		"calc/calc_test.go": failing,
	})
	res, err := newRunner(t).Run(context.Background(), RunRequest{
		Level:         LevelTargeted,
		WorkspacePath: dir,
		ChangedFiles:  []string{"calc/calc.go"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if res.Failed < 1 {
		t.Fatalf("failed = %d, want >= 1", res.Failed)
	}
	found := false
	for _, f := range res.Failures {
		if f.TestName == "TestAdd" {
			found = true
			if !strings.HasSuffix(f.File, "calc_test.go") {
				t.Fatalf("failure file = %q, want calc_test.go", f.File)
			}
			if f.Line <= 0 {
				t.Fatalf("failure line = %d, want > 0", f.Line)
			}
		}
	}
	if !found {
		t.Fatalf("no TestAdd failure extracted: %+v", res.Failures)
	}
}

func TestShellRunnerTargetedFallbackToModule(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"calc/calc.go":      passingModuleFile,
		"calc/calc_test.go": passingTestFile,
	})
	// A .go file in a nonexistent directory must not crash the runner; it
	// falls back to module behaviour and the passing module passes.
	res, err := newRunner(t).Run(context.Background(), RunRequest{
		Level:         LevelTargeted,
		WorkspacePath: dir,
		ChangedFiles:  []string{"does/not/exist.go"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusPassed {
		t.Fatalf("status = %s, want passed via module fallback (output: %s)", res.Status, res.SlicedOutput)
	}
}

func TestShellRunnerModuleVetFailure(t *testing.T) {
	// Builds fine, but `go vet` rejects the Printf format mismatch.
	vetBad := `package calc

import "fmt"

func Describe(n int) string {
	return fmt.Sprintf("%d", "not-a-number" + fmt.Sprint(n))
}
`
	dir := writeModule(t, map[string]string{"calc/calc.go": vetBad})
	res, err := newRunner(t).Run(context.Background(), RunRequest{Level: LevelModule, WorkspacePath: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed (output: %s)", res.Status, res.SlicedOutput)
	}
	found := false
	for _, f := range res.Failures {
		if f.Package == "go vet" && strings.HasPrefix(f.Message, "static analysis: ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no static-analysis failure entry: %+v", res.Failures)
	}
}

func TestShellRunnerModuleFailingTest(t *testing.T) {
	failing := `package calc

import "testing"

func TestAdd(t *testing.T) {
	t.Fatal("always fails")
}
`
	dir := writeModule(t, map[string]string{
		"calc/calc.go":      passingModuleFile,
		"calc/calc_test.go": failing,
	})
	res, err := newRunner(t).Run(context.Background(), RunRequest{Level: LevelModule, WorkspacePath: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if res.Failed < 1 {
		t.Fatalf("failed = %d, want >= 1", res.Failed)
	}
}

// TestShellRunnerPackageRetry exercises the flake re-run: `go test -count=1`
// restricted to exactly the requested packages (import-path patterns).
func TestShellRunnerPackageRetry(t *testing.T) {
	failing := `package flaky

import "testing"

func TestFlaky(t *testing.T) {
	t.Fatal("always fails")
}
`
	dir := writeModule(t, map[string]string{
		"calc/calc.go":        passingModuleFile,
		"calc/calc_test.go":   passingTestFile,
		"flaky/flaky.go":      "package flaky\n",
		"flaky/flaky_test.go": failing,
	})

	// Retrying only the passing package passes — the failing package is never
	// re-run.
	res, err := newRunner(t).Run(context.Background(), RunRequest{
		Level:         LevelModule,
		WorkspacePath: dir,
		RetryPackages: []string{"example.com/fixture/calc"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusPassed {
		t.Fatalf("status = %s, want passed (output: %s)", res.Status, res.SlicedOutput)
	}
	if res.Passed < 1 {
		t.Fatalf("passed = %d, want >= 1", res.Passed)
	}

	// Retrying the failing package fails with test attribution.
	res, err = newRunner(t).Run(context.Background(), RunRequest{
		Level:         LevelModule,
		WorkspacePath: dir,
		RetryPackages: []string{"example.com/fixture/flaky"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	found := false
	for _, f := range res.Failures {
		if f.TestName == "TestFlaky" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no TestFlaky failure extracted: %+v", res.Failures)
	}
}

func TestShellRunnerFullPassing(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"calc/calc.go":      passingModuleFile,
		"calc/calc_test.go": passingTestFile,
	})
	res, err := newRunner(t).Run(context.Background(), RunRequest{Level: LevelFull, WorkspacePath: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusPassed {
		t.Fatalf("status = %s, want passed (output: %s)", res.Status, res.SlicedOutput)
	}
}

func TestShellRunnerTimeoutKillsProcessGroup(t *testing.T) {
	blocking := `package calc

import "testing"

func TestBlock(t *testing.T) {
	select {}
}
`
	dir := writeModule(t, map[string]string{
		"calc/calc.go":      passingModuleFile,
		"calc/calc_test.go": blocking,
	})
	r := NewShellRunner(ShellRunnerOptions{CmdTimeout: 5 * time.Second})
	start := time.Now()
	res, err := r.Run(context.Background(), RunRequest{Level: LevelModule, WorkspacePath: dir})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed after timeout", res.Status)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("timeout kill took %v, want < 30s", elapsed)
	}
}

func TestShellRunnerRequiresWorkspace(t *testing.T) {
	_, err := newRunner(t).Run(context.Background(), RunRequest{Level: LevelCompile})
	if err == nil {
		t.Fatal("expected error for empty WorkspacePath")
	}
}

// envProbeTestFile fails when a daemon secret is visible to agent-authored
// test code (security review H3).
const envProbeTestFile = `package calc

import (
	"os"
	"testing"
)

func TestNoDaemonSecrets(t *testing.T) {
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		t.Fatalf("GITHUB_TOKEN leaked into verification environment")
	}
}
`

// TestShellRunnerStripsDaemonSecrets proves (H3) that agent-authored test
// code executed by the module verification level never sees daemon secrets:
// GITHUB_TOKEN is set in this process's environment (as the daemon would have
// it) and must be stripped from the go test child.
func TestShellRunnerStripsDaemonSecrets(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-token-value")
	dir := writeModule(t, map[string]string{
		"calc/calc.go":      passingModuleFile,
		"calc/calc_test.go": envProbeTestFile,
	})
	res, err := newRunner(t).Run(context.Background(), RunRequest{Level: LevelModule, WorkspacePath: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusPassed {
		t.Fatalf("status = %s, want passed — a secret leaked into the verification env (output: %s)", res.Status, res.SlicedOutput)
	}
}

// TestVerifyEnv unit-tests the environment construction: forbidden vars are
// stripped, the Go essentials survive, and GOFLAGS is forced to
// -mod=readonly (overriding any ambient value).
func TestVerifyEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-token-value")
	t.Setenv("NEUROFORGE_DAEMON_TOKEN", "daemon-secret")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	env := verifyEnv()
	vals := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vals[k] = v
	}
	for _, forbidden := range []string{"GITHUB_TOKEN", "NEUROFORGE_DAEMON_TOKEN"} {
		if _, ok := vals[forbidden]; ok {
			t.Errorf("%s present in verification env", forbidden)
		}
	}
	if vals["GOPROXY"] != "off" {
		t.Errorf("GOPROXY = %q, want forwarded value %q", vals["GOPROXY"], "off")
	}
	if vals["GOFLAGS"] != "-mod=readonly" {
		t.Errorf("GOFLAGS = %q, want -mod=readonly", vals["GOFLAGS"])
	}
	if vals["PATH"] == "" {
		t.Error("PATH missing from verification env")
	}
}
