package testengine

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/proctree"
)

// DefaultShellCmdTimeout bounds a single verification command.
const DefaultShellCmdTimeout = 10 * time.Minute

// slicedOutputCap is the maximum number of bytes kept in Result.SlicedOutput
// (§22.4 — the repair agent gets a summary, never a multi-MB log).
const slicedOutputCap = 8 * 1024

// ShellRunnerOptions configures a [ShellRunner].
type ShellRunnerOptions struct {
	// Logger receives debug logs. Nil disables logging.
	Logger *slog.Logger
	// CmdTimeout bounds one command invocation. Defaults to
	// [DefaultShellCmdTimeout].
	CmdTimeout time.Duration
	// GoBin is the go toolchain binary. Defaults to "go" (resolved via PATH).
	GoBin string
	// Race enables the race detector for LevelFull test runs.
	Race bool
}

// ShellRunner is a [Runner] that executes real repository checks (gofmt,
// go build, go vet, go test) inside a workspace (typically a git worktree).
//
// Guarantees:
//   - Commands run with Dir = WorkspacePath, no shell (argv only).
//   - The environment is inherited from the daemon process (already
//     allowlisted upstream) with GOFLAGS forced to -mod=readonly.
//   - The workspace is never mutated: no gofmt -w, no go mod tidy, no go get.
//   - Cancellation and timeouts kill the whole process group.
type ShellRunner struct {
	log     *slog.Logger
	timeout time.Duration
	goBin   string
	race    bool
}

// NewShellRunner creates a ShellRunner.
func NewShellRunner(opts ShellRunnerOptions) *ShellRunner {
	timeout := opts.CmdTimeout
	if timeout <= 0 {
		timeout = DefaultShellCmdTimeout
	}
	goBin := opts.GoBin
	if goBin == "" {
		goBin = "go"
	}
	return &ShellRunner{
		log:     opts.Logger,
		timeout: timeout,
		goBin:   goBin,
		race:    opts.Race,
	}
}

// Run implements Runner.
func (r *ShellRunner) Run(ctx context.Context, req RunRequest) (Result, error) {
	if req.WorkspacePath == "" {
		return Result{}, fmt.Errorf("testengine: shell runner requires WorkspacePath")
	}
	start := time.Now()
	res := Result{Level: req.Level}

	switch req.Level {
	case LevelSyntax:
		r.runSyntax(ctx, req, &res)
	case LevelCompile:
		r.runCompile(ctx, req, &res)
	case LevelTargeted:
		r.runTargeted(ctx, req, &res)
	case LevelModule, LevelFull:
		r.runModule(ctx, req, &res, req.Level == LevelFull)
	default:
		return Result{}, fmt.Errorf("testengine: unknown verification level %d", req.Level)
	}

	res.Duration = time.Since(start)
	return res, nil
}

// runSyntax checks formatting with `gofmt -l .`. Any listed file (or a gofmt
// invocation failure) means StatusFailed. gofmt is run read-only; `go fmt` is
// mutating and must never be used here.
func (r *ShellRunner) runSyntax(ctx context.Context, req RunRequest, res *Result) {
	gofmt, err := exec.LookPath("gofmt")
	if err != nil {
		res.Status = StatusFailed
		res.Failures = []TestFailure{{Message: "gofmt binary not found in PATH"}}
		res.SlicedOutput = "gofmt: executable not found: " + err.Error()
		return
	}
	out, runErr := r.runCmd(ctx, req.WorkspacePath, gofmt, "-l", ".")
	res.SlicedOutput = sliceTail(out)
	if runErr != nil {
		res.Status = StatusFailed
		res.Failures = []TestFailure{{Message: "gofmt failed: " + runErr.Error()}}
		return
	}
	files := nonEmptyLines(string(out))
	if len(files) > 0 {
		res.Status = StatusFailed
		for _, f := range files {
			res.Failures = append(res.Failures, TestFailure{
				File:    f,
				Message: "gofmt: file is not formatted",
			})
		}
		return
	}
	res.Status = StatusPassed
}

// runCompile runs `go build ./...`.
func (r *ShellRunner) runCompile(ctx context.Context, req RunRequest, res *Result) {
	out, err := r.runCmd(ctx, req.WorkspacePath, r.goBin, "build", "./...")
	res.SlicedOutput = sliceTail(out)
	if err != nil {
		res.Status = StatusFailed
		res.Failed = 1
		res.Failures = parseCompileFailures(string(out))
		if len(res.Failures) == 0 {
			res.Failures = []TestFailure{{Message: "go build failed: " + err.Error()}}
		}
		return
	}
	res.Status = StatusPassed
}

// runTargeted runs tests only for the packages containing changed .go files.
// With no changed Go files the level is skipped; when the changed directories
// cannot be resolved to packages it falls back to module behaviour.
func (r *ShellRunner) runTargeted(ctx context.Context, req RunRequest, res *Result) {
	patterns := packagePatterns(req.WorkspacePath, req.ChangedFiles)
	if len(patterns) == 0 {
		res.Status = StatusSkipped
		return
	}
	args := append([]string{"test", "-count=1"}, patterns...)
	out, err := r.runCmd(ctx, req.WorkspacePath, r.goBin, args...)
	if err != nil && isPackageResolutionError(string(out)) {
		if r.log != nil {
			r.log.Debug("testengine: targeted package resolution failed, falling back to module",
				"patterns", patterns)
		}
		r.runModule(ctx, req, res, false)
		return
	}
	res.SlicedOutput = sliceTail(out)
	if err != nil {
		res.Status = StatusFailed
	} else {
		res.Status = StatusPassed
	}
	counts, failures := parseTestOutput(string(out))
	res.Passed = counts.passed
	res.Failed = counts.failed
	res.Skipped = counts.skipped
	res.Failures = failures
	if err != nil && len(res.Failures) == 0 {
		res.Failures = parseCompileFailures(string(out))
	}
	if err != nil && len(res.Failures) == 0 {
		res.Failures = []TestFailure{{Message: "go test failed: " + err.Error()}}
	}
}

// runModule runs `go vet ./...` and `go test ./...`. A vet failure is a static-
// analysis failure entry. LevelFull adds -race when configured.
func (r *ShellRunner) runModule(ctx context.Context, req RunRequest, res *Result, full bool) {
	var combined strings.Builder

	vetOut, vetErr := r.runCmd(ctx, req.WorkspacePath, r.goBin, "vet", "./...")
	combined.Write(vetOut)

	testArgs := []string{"test", "-count=1"}
	if full && r.race {
		testArgs = append(testArgs, "-race")
	}
	testArgs = append(testArgs, "./...")
	testOut, testErr := r.runCmd(ctx, req.WorkspacePath, r.goBin, testArgs...)
	combined.Write(testOut)

	out := combined.String()
	res.SlicedOutput = sliceTail([]byte(out))

	counts, failures := parseTestOutput(out)
	res.Passed = counts.passed
	res.Failed = counts.failed
	res.Skipped = counts.skipped

	if vetErr != nil {
		vetFailures := parseCompileFailures(string(vetOut))
		if len(vetFailures) == 0 {
			vetFailures = []TestFailure{{Message: "go vet failed: " + vetErr.Error()}}
		}
		for i := range vetFailures {
			vetFailures[i].Package = "go vet"
			vetFailures[i].Message = "static analysis: " + vetFailures[i].Message
		}
		failures = append(vetFailures, failures...)
	}
	res.Failures = failures

	if vetErr != nil || testErr != nil {
		res.Status = StatusFailed
		if len(res.Failures) == 0 {
			res.Failures = []TestFailure{{Message: "module verification failed"}}
		}
		if res.Failed == 0 {
			res.Failed = 1
		}
		return
	}
	res.Status = StatusPassed
}

// runCmd executes one verification command with the timeout applied, output
// combined (stdout+stderr), and process-group kill on cancellation.
func (r *ShellRunner) runCmd(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if r.log != nil {
		r.log.Debug("testengine: exec", "dir", dir, "cmd", name, "args", args)
	}

	cmd := proctree.NewGroupCommand(name, args...)
	cmd.Dir = dir
	// Inherit the (already allowlisted) daemon environment; force module
	// read-only mode so nothing in the worktree is rewritten. os/exec keeps the
	// last value of a duplicated key, so this overrides any ambient GOFLAGS.
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly")

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return buf.Bytes(), err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return buf.Bytes(), err
	case <-ctx.Done():
		_ = proctree.KillGroup(cmd, proctree.SigKill)
		<-done
		return buf.Bytes(), ctx.Err()
	}
}

// packagePatterns maps changed files to `./<dir>/...` go test patterns,
// deduplicated and sorted. Only .go files contribute.
func packagePatterns(workspace string, changed []string) []string {
	seen := map[string]struct{}{}
	for _, f := range changed {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		rel := f
		if filepath.IsAbs(f) {
			r, err := filepath.Rel(workspace, f)
			if err != nil || strings.HasPrefix(r, "..") {
				continue // outside the workspace — not targetable
			}
			rel = r
		}
		dir := filepath.Dir(rel)
		var pattern string
		if dir == "." {
			pattern = "./..."
		} else {
			pattern = "./" + filepath.ToSlash(dir) + "/..."
		}
		seen[pattern] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// isPackageResolutionError reports whether a go test failure is caused by
// unresolvable package patterns rather than real test results.
func isPackageResolutionError(out string) bool {
	if strings.Contains(out, "pattern ") && strings.Contains(out, "no such file or directory") {
		return true
	}
	for _, marker := range []string{
		"matched no packages",
		"no Go files in",
		"no required module provides",
		"cannot find package",
		"is not in std",
	} {
		if strings.Contains(out, marker) {
			return true
		}
	}
	return false
}

var (
	// Build/vet diagnostics: file.go:12:3: message.
	buildErrRe = regexp.MustCompile(`^([^\s:]+\.go):(\d+):(\d+):\s*(.+)$`)
	// go test events.
	failTestRe  = regexp.MustCompile(`^--- FAIL: ([^\s]+)`)
	skipTestRe  = regexp.MustCompile(`^--- SKIP: ([^\s]+)`)
	failPkgRe   = regexp.MustCompile(`^FAIL\s+(\S+)`)
	okPkgRe     = regexp.MustCompile(`^ok\s+\S+`)
	testLocRe   = regexp.MustCompile(`^\s+([^\s:]+\.go):(\d+):\s*(.+)$`)
	passTestStr = "--- PASS: "
)

type testCounts struct{ passed, failed, skipped int }

// parseTestOutput extracts pass/fail/skip counts and failing tests from
// `go test` output (best effort; file:line hints inside a failing test are
// attached to that test).
func parseTestOutput(out string) (testCounts, []TestFailure) {
	var counts testCounts
	var failures []TestFailure
	var failPkgs []string

	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, passTestStr):
			counts.passed++
		case failTestRe.MatchString(line):
			counts.failed++
			failures = append(failures, TestFailure{TestName: failTestRe.FindStringSubmatch(line)[1]})
		case skipTestRe.MatchString(line):
			counts.skipped++
		default:
			if m := failPkgRe.FindStringSubmatch(line); m != nil {
				failPkgs = append(failPkgs, m[1])
			} else if m := testLocRe.FindStringSubmatch(line); m != nil && len(failures) > 0 {
				last := &failures[len(failures)-1]
				if last.File == "" {
					last.File = m[1]
					last.Line = atoi(m[2])
					if last.Message == "" {
						last.Message = m[3]
					}
				}
			}
		}
	}
	// Attach the failing package when it is unambiguous.
	if len(failPkgs) == 1 {
		for i := range failures {
			if failures[i].Package == "" {
				failures[i].Package = failPkgs[0]
			}
		}
	}
	// Fallback: no per-test lines at all — count packages instead.
	if counts.passed == 0 && counts.failed == 0 && counts.skipped == 0 {
		for _, line := range strings.Split(out, "\n") {
			switch {
			case okPkgRe.MatchString(line):
				counts.passed++
			case failPkgRe.MatchString(line) && line != "FAIL":
				counts.failed++
			}
		}
	}
	return counts, failures
}

// parseCompileFailures extracts file:line diagnostics from go build / go vet
// output.
func parseCompileFailures(out string) []TestFailure {
	var failures []TestFailure
	for _, line := range strings.Split(out, "\n") {
		if m := buildErrRe.FindStringSubmatch(line); m != nil {
			failures = append(failures, TestFailure{
				File:    m[1],
				Line:    atoi(m[2]),
				Message: m[4],
			})
		}
	}
	return failures
}

// sliceTail keeps the last slicedOutputCap bytes of output.
func sliceTail(out []byte) string {
	if len(out) > slicedOutputCap {
		out = out[len(out)-slicedOutputCap:]
	}
	return string(out)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
