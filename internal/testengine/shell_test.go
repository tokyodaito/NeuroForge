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
