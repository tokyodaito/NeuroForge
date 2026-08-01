package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/pipeline"
	"neuroforge/internal/policy"
	"neuroforge/internal/repair"
	"neuroforge/internal/review"
	"neuroforge/internal/runapp"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/task"
	"neuroforge/internal/testengine"
	"neuroforge/internal/workgraph"
	"neuroforge/internal/workspace"
)

// This file holds the concrete pipeline stage handlers the PipelineService
// injects into the pipeline Driver. Every handler is restart-safe: all inputs
// are re-derived from durable state (the run row, the persisted params file,
// the workspace record, the spec/graph stores) — nothing is carried in memory
// between stages.
//
// Failure mapping (handler → StageError category → driver routing):
//   - provider auth/quota/rate-limit/timeout classes from agent runs map to
//     agent_auth_unavailable / quota_exceeded / rate_limited /
//     provider_timeout; quota/rate-limit from execute parks the run in
//     waiting_quota (driver rule), everything else fails the run;
//   - verification failures map to compile_failure / static_analysis_failure
//     / test_failure (progressive levels, stop at first failure);
//   - unparseable review output maps to invalid_agent_output;
//   - a cancelled agent run cancels the run durably (Store.Cancel) unless the
//     emergency stop caused it — then the stage fails as `interrupted` and the
//     driver parks the run (stays active; resumes on estop-off/restart);
//   - a lease conflict in ready maps to lease_lost and the driver parks the
//     run in blocked (resumes via recovery after the lease clears).

// discardWriter silences the default logger when the caller supplies none
// (mirrors pipeline.Store's pattern).
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// ---- compile ----

// handleCompile ensures a persisted compiled specification exists for the
// task (M14-03 daemon compiler path, inlined): the deterministic task.Compile
// runs over the task's durable fields and the result is persisted through
// SpecificationStore.SaveIfChanged (idempotent — no duplicate semantic
// versions across re-drives).
func (s *PipelineService) handleCompile(ctx context.Context, rc *pipeline.RunContext) (string, error) {
	t, err := s.tasks.Get(ctx, rc.TaskID)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureCompile, Reason: fmt.Sprintf("load task: %v", err)}
	}
	res, err := task.Compile(task.CompileInput{
		TaskID:      t.ID,
		Title:       t.Title,
		Description: t.Description,
		Priority:    t.Priority,
		Attachments: t.Attachments,
	})
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureCompile, Reason: err.Error()}
	}
	spec := res.Specification
	spec.CreatedBy = "pipeline"
	saved, _, err := s.specs.SaveIfChanged(ctx, spec)
	if err != nil {
		cat := pipeline.FailureCompile
		if strings.Contains(err.Error(), "is locked") {
			cat = pipeline.FailurePolicyRejection
		}
		return "", &pipeline.StageError{Category: cat, Reason: err.Error()}
	}
	evidence := fmt.Sprintf("spec:v%d", saved.Version)
	s.auditStage(ctx, rc, "completed", evidence, "")
	return evidence, nil
}

// ---- plan ----

// handlePlan decomposes the latest compiled specification into the work graph
// and persists it (idempotent across re-drives: WorkGraphStore.Save preserves
// runtime package state).
func (s *PipelineService) handlePlan(ctx context.Context, rc *pipeline.RunContext) (string, error) {
	spec, err := s.specs.GetLatest(ctx, rc.TaskID)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureInvariantViolation,
			Reason: fmt.Sprintf("load compiled spec: %v", err)}
	}
	vg, err := workgraph.Decompose(spec)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureInvariantViolation,
			Reason: fmt.Sprintf("decompose spec: %v", err)}
	}
	g, err := s.graphs.Save(ctx, vg)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureDatabase, Reason: err.Error()}
	}
	evidence := fmt.Sprintf("workgraph:%s:packages=%d", g.TaskID, len(g.Packages))
	s.auditStage(ctx, rc, "completed", evidence, "")
	return evidence, nil
}

// ---- ready ----

// pipelineLeaseTTL bounds every lease the pipeline claims. A crashed run
// must not hold its leases forever: after the TTL the lease expires and a
// competing run may proceed (review finding H4 — leases were perpetual).
const pipelineLeaseTTL = 4 * time.Hour

// handleReady prepares dispatch: the managed worktree exists (created on
// first pass, reused on re-drive) and the pending, dependency-ready work
// package(s) are claimed under project-scoped, TTL-bounded leases (M14-05).
//
// A claim that fails because of a LEASE/READINESS CONFLICT (another workspace
// holds a path the package needs) is not a stage failure: the run is parked
// in blocked (driver rule: lease_lost from ready) and re-driven by restart
// recovery once the conflicting lease is released or expires. A claim that
// fails only because a chained package's dependency is pending is expected —
// the loop stops there; the dependent package is dispatched after its
// dependency succeeds.
func (s *PipelineService) handleReady(ctx context.Context, rc *pipeline.RunContext) (string, error) {
	params, err := s.loadParams(rc.TaskID)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureInvariantViolation, Reason: err.Error()}
	}
	ws, err := s.workspaceForTask(ctx, rc.TaskID)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureDatabase, Reason: err.Error()}
	}
	if ws == nil {
		created, cerr := s.wm.Create(ctx, workspace.CreateRequest{
			ProjectID:   rc.ProjectID,
			ProjectPath: params.ProjectPath,
			TaskID:      rc.TaskID,
			BaseBranch:  params.BaseBranch,
		})
		if cerr != nil {
			return "", &pipeline.StageError{Category: pipeline.FailureWorktree, Reason: cerr.Error()}
		}
		ws = &created
	}
	vg, err := s.graphs.LoadValidated(ctx, rc.TaskID)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureInvariantViolation,
			Reason: fmt.Sprintf("load work graph: %v", err)}
	}
	claimed := 0
	for _, pkg := range vg.Graph().Packages {
		if pkg.State != workgraph.PackagePending {
			continue // already claimed/executed (re-drive) or terminal
		}
		if _, cerr := s.sched.Claim(ctx, workgraph.ClaimRequest{
			TaskID:      rc.TaskID,
			ProjectID:   rc.ProjectID,
			PackageID:   pkg.ID,
			WorkspaceID: ws.ID,
			TTL:         pipelineLeaseTTL,
		}); cerr != nil {
			if errors.Is(cerr, workgraph.ErrLeaseConflict) {
				return "", &pipeline.StageError{Category: pipeline.FailureLeaseLost,
					Reason: fmt.Sprintf("package %s blocked by conflicting lease: %v", pkg.ID, cerr)}
			}
			if errors.Is(cerr, workgraph.ErrPackageNotReady) {
				if nre, ok := workgraph.AsNotReadyError(cerr); ok && hasLeaseConflictReason(nre) {
					return "", &pipeline.StageError{Category: pipeline.FailureLeaseLost,
						Reason: fmt.Sprintf("package %s blocked by conflicting lease: %s",
							pkg.ID, strings.Join(nre.Reasons, "; "))}
				}
				break // chained packages wait for their dependency; out of scope here
			}
			// Anything else is NOT a lease conflict (a storage failure in
			// GetPackage/LoadValidated/ListActiveByProject, a failed state
			// transition, …). Classifying it lease_lost would park the run in
			// blocked for up to the lease TTL instead of failing it honestly
			// (review follow-up N2).
			return "", &pipeline.StageError{Category: pipeline.FailureDatabase, Reason: cerr.Error()}
		}
		claimed++
	}
	evidence := fmt.Sprintf("workspace:%s;claimed:%d", ws.ID, claimed)
	s.auditStage(ctx, rc, "completed", evidence, "")
	return evidence, nil
}

// hasLeaseConflictReason reports whether a not-ready verdict is caused by an
// active lease held by another workspace (reason text from
// workgraph.ComputeReadiness: `path %q held by workspace %q`), as opposed to
// an unmet dependency.
func hasLeaseConflictReason(nre *workgraph.NotReadyError) bool {
	for _, r := range nre.Reasons {
		if strings.Contains(r, "held by workspace") {
			return true
		}
	}
	return false
}

// ---- execute ----

// handleExecute runs the coding agent once in the managed worktree. The run
// context is registered for per-task cancellation (API cancel / estop). On
// success the worktree inspection yields the changed-file count the driver
// routes on (0 → no_code_changes → repair).
func (s *PipelineService) handleExecute(ctx context.Context, rc *pipeline.RunContext) (string, int, error) {
	params, ws, err := s.runTarget(ctx, rc)
	if err != nil {
		return "", 0, err
	}
	prompt := buildExecutePrompt(ctx, s.specs, rc.TaskID, params)
	res, err := s.runAgent(ctx, rc.TaskID, ws, params, prompt)
	if err != nil {
		return "", 0, err
	}
	if stageErr := s.agentOutcomeError(ctx, rc.TaskID, res); stageErr != nil {
		return "", 0, stageErr
	}
	insp, err := s.wm.InspectWorktree(ctx, *ws)
	if err != nil {
		return "", 0, &pipeline.StageError{Category: pipeline.FailureWorktree, Reason: err.Error()}
	}
	if err := s.wm.SetRunInfo(ctx, ws.ID, params.Engine, params.Model, res.Handle.RunID, res.Handle.SessionID); err != nil {
		s.logger.Warn("pipeline: set run info failed", "workspace", ws.ID, "err", err)
	}
	evidence := fmt.Sprintf("workspace:%s;run:%s;changed:%d", ws.ID, res.Handle.RunID, len(insp.ChangedFiles))
	s.auditStage(ctx, rc, "completed", evidence, "")
	return evidence, len(insp.ChangedFiles), nil
}

// ---- verify ----

// verifyLevels is the progressive verification cascade (spec §24.3): syntax
// (gofmt, read-only) → compile (go build) → targeted tests (packages touching
// changed files) → module (go vet + go test). The first failing level stops
// the cascade.
var verifyLevels = []testengine.VerificationLevel{
	testengine.LevelSyntax,
	testengine.LevelCompile,
	testengine.LevelTargeted,
	testengine.LevelModule,
}

// handleVerify runs the progressive verification cascade in the worktree and
// maps the first failing level to a failure category. Verification never
// mutates the worktree (ShellRunner guarantee).
//
// Flake guard (module level only): timing-sensitive tests occasionally fail
// under load and pass on re-run — without a guard each flake burned a repair
// cycle "fixing" infrastructure noise. When the module level fails and every
// failure is attributable to a concrete test package, the handler re-runs
// `go test -count=1` for exactly those packages ONCE; a passing re-run
// verdicts the level passed and records a flake_retry entry in the evidence.
func (s *PipelineService) handleVerify(ctx context.Context, rc *pipeline.RunContext) (bool, string, pipeline.FailureCategory, error) {
	_, ws, err := s.runTarget(ctx, rc)
	if err != nil {
		return false, "", "", err
	}
	out, err := s.verifyWorktree(ctx, rc.TaskID, ws)
	if err != nil {
		return false, "", "", err
	}
	evidence := s.evidenceJSON(ctx, rc.TaskID, "verify", verifyEvidence{Results: out.results, FlakeRetry: out.retry})
	status := "completed"
	if !out.passed {
		status = "failed"
	}
	s.auditStage(ctx, rc, status, evidence, string(out.category))
	return out.passed, evidence, out.category, nil
}

// verifyOutcome is the result of the verification cascade: the per-level
// results (including the flake re-run, when one happened), the aggregate
// verdict, the mapped failure category of the first failure and the flake
// re-run record for the evidence payload.
type verifyOutcome struct {
	results  []testengine.Result
	passed   bool
	category pipeline.FailureCategory
	retry    *flakeRetryEvidence
}

// verifyEvidence is the persisted verify-stage evidence payload.
type verifyEvidence struct {
	Results []testengine.Result `json:"results"`
	// FlakeRetry is set when a failed module level was re-run per-package
	// exactly once (flake guard).
	FlakeRetry *flakeRetryEvidence `json:"flake_retry,omitempty"`
}

// flakeRetryEvidence records the single module-level flake re-run: the
// retried packages, the original failure messages and the retry verdict.
type flakeRetryEvidence struct {
	Packages         []string `json:"packages"`
	OriginalFailures []string `json:"original_failures"`
	Passed           bool     `json:"passed"`
}

// verifyWorktree runs the cascade and returns the per-level results, the
// aggregate verdict and the mapped failure category of the first failure.
func (s *PipelineService) verifyWorktree(ctx context.Context, taskID string, ws *workspace.Workspace) (verifyOutcome, error) {
	var out verifyOutcome
	insp, err := s.wm.InspectWorktree(ctx, *ws)
	if err != nil {
		return out, &pipeline.StageError{Category: pipeline.FailureWorktree, Reason: err.Error()}
	}
	for _, lvl := range verifyLevels {
		res, rerr := s.runner.Run(ctx, testengine.RunRequest{
			Level:         lvl,
			WorkspacePath: ws.Path,
			ChangedFiles:  insp.ChangedFiles,
		})
		if rerr != nil {
			if errors.Is(rerr, context.Canceled) {
				// Caller cancellation (SIGINT on `forge run` / API cancel)
				// interrupted verification: classify honestly as cancelled —
				// matching the execute handler — never as an invariant
				// violation (review finding M6).
				return out, s.interruptedOrCancelledError(taskID, "verification cancelled")
			}
			return out, &pipeline.StageError{
				Category: pipeline.FailureInvariantViolation,
				Reason:   fmt.Sprintf("verification level %s: %v", lvl, rerr),
			}
		}
		if ctx.Err() != nil {
			// The caller cancelled while this level ran: the shell runner
			// reports the killed command as a failed level, which would be
			// misclassified as a test/compile failure (and drive a bogus
			// repair). Cancellation is not a verification verdict (M6).
			return out, s.interruptedOrCancelledError(taskID, "verification cancelled")
		}
		res.Level = lvl
		out.results = append(out.results, res)
		if res.Status == testengine.StatusFailed {
			if lvl == testengine.LevelModule {
				if retried, rerr := s.flakeRetry(ctx, taskID, ws, res, &out); retried || rerr != nil {
					return out, rerr
				}
			}
			out.category = categoryForLevel(lvl, res)
			return out, nil
		}
		// passed/skipped: continue the cascade.
	}
	out.passed = true
	return out, nil
}

// flakeRetry performs the single allowed module-level flake re-run: when
// every failure of the module run is attributable to a concrete test package,
// it re-runs `go test -count=1` for exactly those packages once and records
// the attempt in out.retry (both attempts stay in out.results). It reports
// whether a retry happened; a retry error is returned like any level error.
func (s *PipelineService) flakeRetry(ctx context.Context, taskID string, ws *workspace.Workspace, moduleRes testengine.Result, out *verifyOutcome) (bool, error) {
	pkgs := flakeRetryPackages(moduleRes)
	if len(pkgs) == 0 {
		return false, nil // unattributed or vet failures are not flake candidates
	}
	out.retry = &flakeRetryEvidence{
		Packages:         pkgs,
		OriginalFailures: failureMessages(moduleRes.Failures),
	}
	retryRes, rerr := s.runner.Run(ctx, testengine.RunRequest{
		Level:         testengine.LevelModule,
		WorkspacePath: ws.Path,
		RetryPackages: pkgs,
	})
	if rerr != nil {
		if errors.Is(rerr, context.Canceled) {
			return true, s.interruptedOrCancelledError(taskID, "verification cancelled")
		}
		return true, &pipeline.StageError{
			Category: pipeline.FailureInvariantViolation,
			Reason:   fmt.Sprintf("verification level module (package retry): %v", rerr),
		}
	}
	if ctx.Err() != nil {
		// Same M6 rule as the main cascade: a caller cancellation mid-retry is
		// not a verification verdict.
		return true, s.interruptedOrCancelledError(taskID, "verification cancelled")
	}
	retryRes.Level = testengine.LevelModule
	out.results = append(out.results, retryRes)
	out.retry.Passed = retryRes.Status == testengine.StatusPassed
	if out.retry.Passed {
		// Flake confirmed: the module level is treated as passed. The original
		// failure stays in the evidence via out.retry.OriginalFailures.
		out.passed = true
		return true, nil
	}
	out.category = categoryForLevel(testengine.LevelModule, moduleRes)
	return true, nil
}

// flakeRetryPackages extracts the distinct failing packages from a failed
// module-level result, or nil when the failure is not a flake candidate: a
// re-run is only allowed when every failure is attributable to a concrete
// test package. A go vet entry or an unattributed failure is a
// static-analysis/compile problem a test re-run cannot fix.
func flakeRetryPackages(res testengine.Result) []string {
	if len(res.Failures) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var pkgs []string
	for _, f := range res.Failures {
		if f.Package == "" || f.Package == "go vet" {
			return nil
		}
		if _, ok := seen[f.Package]; !ok {
			seen[f.Package] = struct{}{}
			pkgs = append(pkgs, f.Package)
		}
	}
	sort.Strings(pkgs)
	return pkgs
}

// failureMessages renders the original module-level failure messages for the
// flake_retry evidence (package-qualified where attribution exists).
func failureMessages(fs []testengine.TestFailure) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		msg := f.Message
		if msg == "" {
			msg = f.TestName
		}
		if f.Package != "" {
			msg = f.Package + ": " + msg
		}
		out = append(out, msg)
	}
	return out
}

// categoryForLevel maps a failing verification level to the pipeline failure
// taxonomy: gofmt/vet are static analysis, go build is a compile failure and
// go test is a test failure.
func categoryForLevel(lvl testengine.VerificationLevel, res testengine.Result) pipeline.FailureCategory {
	switch lvl {
	case testengine.LevelSyntax:
		return pipeline.FailureStaticAnalysis
	case testengine.LevelCompile:
		return pipeline.FailureCompile
	case testengine.LevelTargeted:
		return pipeline.FailureTest
	case testengine.LevelModule, testengine.LevelFull:
		for _, f := range res.Failures {
			if f.Package == "go vet" {
				return pipeline.FailureStaticAnalysis
			}
		}
		return pipeline.FailureTest
	}
	return pipeline.FailureInvariantViolation
}

// ---- review ----

// handleReview runs the review engine over the worktree diff. Approval (or a
// fully-skipped review, e.g. all roles disabled by policy) routes to
// finalize; major/blocker findings route to repair via review_rejection.
//
// The review agent runs with full write tools in the worktree AFTER
// verification, so the handler proves it did not exercise them: the worktree
// fingerprint (HEAD + git status --porcelain) must be identical before and
// after the review pass. A mutated worktree is an honest policy_rejection —
// the run fails, it is NEVER finalized with unverified reviewer edits
// (security review H2).
func (s *PipelineService) handleReview(ctx context.Context, rc *pipeline.RunContext) (bool, string, error) {
	params, ws, err := s.runTarget(ctx, rc)
	if err != nil {
		return false, "", err
	}
	before, err := s.wm.WorktreeFingerprint(ctx, ws.Path)
	if err != nil {
		return false, "", &pipeline.StageError{Category: pipeline.FailureWorktree, Reason: err.Error()}
	}
	res, err := s.reviewWorktree(ctx, rc, params, ws)
	if err != nil {
		var se *pipeline.StageError
		if errors.As(err, &se) {
			return false, "", se
		}
		return false, "", &pipeline.StageError{Category: pipeline.FailureAgentUnavailable, Reason: err.Error()}
	}
	after, err := s.wm.WorktreeFingerprint(ctx, ws.Path)
	if err != nil {
		return false, "", &pipeline.StageError{Category: pipeline.FailureWorktree, Reason: err.Error()}
	}
	if after != before {
		return false, "", &pipeline.StageError{Category: pipeline.FailurePolicyRejection,
			Reason: "reviewer modified the worktree"}
	}
	evidence := s.evidenceJSON(ctx, rc.TaskID, "review", reviewEvidence{
		Label:        res.Label(),
		RolesRun:     res.RolesRun,
		RolesSkipped: res.RolesSkipped,
		Findings:     res.Findings,
	})
	approved := res.OverallStatus() == review.StatusApproved || res.OverallStatus() == review.StatusSkipped
	status := "completed"
	if !approved {
		status = "failed"
	}
	s.auditStage(ctx, rc, status, evidence, string(pipeline.FailureReviewRejection))
	return approved, evidence, nil
}

// reviewEvidence is the persisted review-stage evidence payload. The label
// ("REVIEWED" / "NOT AI-REVIEWED") and the role lists make a policy-skipped
// review distinguishable from a clean reviewed pass (review finding M5).
type reviewEvidence struct {
	Label        string           `json:"label"`
	RolesRun     []review.Role    `json:"roles_run"`
	RolesSkipped []review.Role    `json:"roles_skipped"`
	Findings     []review.Finding `json:"findings"`
}

// reviewWorktree executes one review pass: diff the worktree, resolve the
// project policy, run the enabled review roles.
func (s *PipelineService) reviewWorktree(ctx context.Context, rc *pipeline.RunContext, params pipelineParams, ws *workspace.Workspace) (review.Result, error) {
	insp, err := s.wm.InspectWorktree(ctx, *ws)
	if err != nil {
		return review.Result{}, &pipeline.StageError{Category: pipeline.FailureWorktree, Reason: err.Error()}
	}
	diff, err := s.wm.Diff(ctx, ws.ID)
	if err != nil {
		return review.Result{}, &pipeline.StageError{Category: pipeline.FailureGit, Reason: err.Error()}
	}
	resolved, err := s.resolvedPolicy(ctx, rc.ProjectID)
	if err != nil {
		return review.Result{}, &pipeline.StageError{Category: pipeline.FailurePolicyRejection, Reason: err.Error()}
	}
	reviewer := s.reviewer
	if reviewer == nil {
		reviewer = review.NewAgentReviewer(func(runCtx context.Context, prompt string) (string, error) {
			return s.runAgentText(runCtx, rc.TaskID, ws, params, prompt)
		}, review.AgentReviewerOptions{})
	}
	eng := review.New(review.Options{Reviewer: reviewer})
	res, err := eng.Run(ctx, review.RunInput{
		Policy: resolved,
		Req: review.ReviewRequest{
			Diff:         diff,
			ChangedFiles: insp.ChangedFiles,
			Context:      params.Description,
		},
	})
	if err != nil {
		if errors.Is(err, review.ErrUnparseableReview) {
			return res, &pipeline.StageError{Category: pipeline.FailureInvalidAgentOutput, Reason: err.Error()}
		}
		return res, err
	}
	return res, nil
}

// resolvedPolicy builds the enforceable policy for the run's project the same
// way the scheduler does (policy.NewProject(profile) + Resolve with no task
// override).
func (s *PipelineService) resolvedPolicy(ctx context.Context, projectID string) (policy.Resolved, error) {
	p, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return policy.Resolved{}, err
	}
	resolved, _ := policy.Resolve(policy.NewProject(p.Profile), policy.TaskContext{})
	return resolved, nil
}

// ---- repair ----

// handleRepair performs ONE bounded repair attempt (the driver bounds the
// loop via IncrementRepairAttempt; single-attempt-per-stage is the
// restart-clean shape). Findings are re-derived READ-ONLY from durable state:
// the recorded failure category decides whether to re-verify (test/static/
// compile failures), re-review (review rejection) or simply re-prompt
// (no_code_changes).
func (s *PipelineService) handleRepair(ctx context.Context, rc *pipeline.RunContext) (string, error) {
	params, ws, err := s.runTarget(ctx, rc)
	if err != nil {
		return "", err
	}
	run, err := s.store.CurrentRun(ctx, rc.TaskID)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureDatabase, Reason: err.Error()}
	}

	prompt, err := s.buildRepairPrompt(ctx, rc, run, params, ws)
	if err != nil {
		return "", err
	}
	res, err := s.runAgent(ctx, rc.TaskID, ws, params, prompt)
	if err != nil {
		return "", err
	}
	if stageErr := s.agentOutcomeError(ctx, rc.TaskID, res); stageErr != nil {
		return "", stageErr
	}
	if err := s.wm.SetRunInfo(ctx, ws.ID, params.Engine, params.Model, res.Handle.RunID, res.Handle.SessionID); err != nil {
		s.logger.Warn("pipeline: set run info failed", "workspace", ws.ID, "err", err)
	}
	evidence := fmt.Sprintf("workspace:%s;run:%s;repair:%d", ws.ID, res.Handle.RunID, rc.RepairAttempt)
	s.auditStage(ctx, rc, "completed", evidence, "")
	return evidence, nil
}

// buildRepairPrompt re-derives the current findings read-only and renders the
// targeted repair prompt (§22.5: findings + diff + files, not the transcript).
func (s *PipelineService) buildRepairPrompt(ctx context.Context, rc *pipeline.RunContext, run *pipeline.Run, params pipelineParams, ws *workspace.Workspace) (string, error) {
	if run.FailureCategory == pipeline.FailureNoCodeChanges {
		return fmt.Sprintf("Your previous attempt produced no repository changes. "+
			"Implement the task now and write the required files:\n\n%s", params.Description), nil
	}
	insp, err := s.wm.InspectWorktree(ctx, *ws)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureWorktree, Reason: err.Error()}
	}
	var findings []repair.Finding
	if run.FailureCategory == pipeline.FailureReviewRejection {
		res, rerr := s.reviewWorktree(ctx, rc, params, ws)
		if rerr == nil {
			findings = repair.FromReviewFindings(res.Findings)
		}
	} else {
		out, verr := s.verifyWorktree(ctx, rc.TaskID, ws)
		if verr != nil {
			return "", verr
		}
		findings = repair.FromTestFailures(out.results)
	}
	if len(findings) == 0 {
		// Nothing actionable re-derived (e.g. the state moved on): fall back to
		// re-stating the task so the agent converges on a verifiable change.
		return fmt.Sprintf("The previous attempt did not converge. Re-read the task and "+
			"finish it so verification and review pass:\n\n%s", params.Description), nil
	}
	diff, _ := s.wm.Diff(ctx, ws.ID)
	rctx := repair.RepairContext{
		Findings:     findings,
		Diff:         diff,
		ChangedFiles: insp.ChangedFiles,
		Iteration:    rc.RepairAttempt,
	}
	return rctx.Prompt(), nil
}

// ---- finalize ----

// handleFinalize reuses runapp.Service.Finalize — the crash-consistent,
// idempotent terminal chokepoint (result ref + atomic workspace/task/audit
// commit) — as the pipeline's terminal stage. The outcome is persisted
// alongside the run params so the status endpoint can render it after a
// restart.
//
// Deterministic result commit (pipeline path only — runapp.Finalize semantics
// are unchanged): the factory owns the managed worktree, so when the agent
// left uncommitted changes behind, the handler stages and commits them on the
// attempt branch BEFORE Finalize. EnsureResultRef then points at that commit
// and the outcome is completed-with-commit instead of
// completed-with-uncommitted-changes. A clean worktree commits nothing (no
// empty commits), which also makes a re-driven finalize idempotent.
func (s *PipelineService) handleFinalize(ctx context.Context, rc *pipeline.RunContext) (string, error) {
	params, ws, err := s.runTarget(ctx, rc)
	if err != nil {
		return "", err
	}
	insp, err := s.wm.InspectWorktree(ctx, *ws)
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureWorktree, Reason: err.Error()}
	}
	if strings.TrimSpace(insp.StatusPorcelain) != "" {
		msg := fmt.Sprintf("forge: result for task %s\n\nstage: %s, attempt: %d", rc.TaskID, rc.Stage, rc.Attempt)
		sha, created, cerr := s.wm.CommitWorktreeChanges(ctx, ws.ID, msg)
		if cerr != nil {
			return "", &pipeline.StageError{Category: pipeline.FailureGit,
				Reason: fmt.Sprintf("result commit: %v", cerr)}
		}
		if created {
			s.audit(ctx, "pipeline.finalize_autocommit", rc.TaskID, map[string]any{
				"workspace": ws.ID, "commit_sha": sha,
				"stage": string(rc.Stage), "attempt": rc.Attempt,
			})
			// Re-inspect so Finalize classifies against the NEW head (now
			// ahead of base, clean tree → completed-with-commit).
			insp, err = s.wm.InspectWorktree(ctx, *ws)
			if err != nil {
				return "", &pipeline.StageError{Category: pipeline.FailureWorktree, Reason: err.Error()}
			}
		}
	}
	fin, err := s.fin.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   ws.ID,
		TerminalEvent: protocol.NormalizedEvent{Type: protocol.EventRunCompleted},
		Inspection:    insp,
		TaskID:        rc.TaskID,
		Engine:        params.Engine,
		Model:         params.Model,
		RunID:         ws.RunID,
	})
	if err != nil {
		return "", &pipeline.StageError{Category: pipeline.FailureGit, Reason: err.Error()}
	}
	s.mu.Lock()
	s.lastFin[rc.TaskID] = fin
	s.mu.Unlock()
	s.saveFinalizeRecord(rc.TaskID, fin)
	ref := fin.ResultBranch
	if ref == "" {
		ref = string(fin.Outcome)
	}
	s.auditStage(ctx, rc, "completed", ref, "")
	s.audit(ctx, "pipeline.run_finished", rc.TaskID, map[string]any{
		"outcome": string(fin.Outcome), "result_branch": fin.ResultBranch, "commit_sha": fin.CommitSHA,
	})
	return ref, nil
}

// ---- shared helpers ----

// runTarget loads the durable params and the run's workspace for a handler.
func (s *PipelineService) runTarget(ctx context.Context, rc *pipeline.RunContext) (pipelineParams, *workspace.Workspace, error) {
	params, err := s.loadParams(rc.TaskID)
	if err != nil {
		return pipelineParams{}, nil, &pipeline.StageError{Category: pipeline.FailureInvariantViolation, Reason: err.Error()}
	}
	ws, err := s.workspaceForTask(ctx, rc.TaskID)
	if err != nil {
		return pipelineParams{}, nil, &pipeline.StageError{Category: pipeline.FailureDatabase, Reason: err.Error()}
	}
	if ws == nil {
		return pipelineParams{}, nil, &pipeline.StageError{Category: pipeline.FailureWorktree,
			Reason: "no workspace for task (ready stage did not run)"}
	}
	return params, ws, nil
}

// runAgent executes one supervisor run with a per-task cancellable context so
// API cancellation and the emergency stop can interrupt in-flight agent work.
func (s *PipelineService) runAgent(ctx context.Context, taskID string, ws *workspace.Workspace, params pipelineParams, prompt string) (supervisor.RunResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	s.registerRunCancel(taskID, cancel)
	defer func() {
		s.unregisterRunCancel(taskID)
		cancel()
	}()
	res, err := s.sup.Run(runCtx, supervisor.RunRequest{
		WorkspaceID: ws.ID,
		Engine:      params.Engine,
		Model:       params.Model,
		Prompt:      prompt,
		Timeout:     time.Duration(params.TimeoutNanos),
	}, ws.Path)
	if err != nil {
		return supervisor.RunResult{}, &pipeline.StageError{
			Category: pipeline.FailureAgentUnavailable, Reason: err.Error()}
	}
	// Usage events (tokens/cost) must not be dropped on the pipeline path
	// (review finding M2): persist them exactly like the runapp path does.
	s.recordUsage(ctx, taskID, params, res.Events)
	return res, nil
}

// recordUsage extracts usage events from an agent run's event stream and
// persists them via the UsageSink, mirroring runapp.Service.recordUsage
// (KF-10). Best-effort and never fatal — usage recording is observability,
// not a correctness gate.
func (s *PipelineService) recordUsage(ctx context.Context, taskID string, params pipelineParams, events []protocol.NormalizedEvent) {
	if s.usage == nil {
		return
	}
	now := time.Now().UTC()
	for _, ev := range events {
		if ev.Type != protocol.EventUsageUpdated || ev.Usage == nil {
			continue
		}
		ue := runapp.UsageEvent{
			TaskID:            taskID,
			ProjectID:         params.ProjectID,
			Provider:          params.Engine,
			Model:             params.Model,
			Kind:              "coding",
			InputTokens:       ev.Usage.InputTokens,
			CachedInputTokens: ev.Usage.CacheReadTokens,
			OutputTokens:      ev.Usage.OutputTokens,
			CostUSD:           ev.Usage.Cost,
			OccurredAt:        now,
		}
		if err := s.usage.RecordUsage(ctx, ue); err != nil {
			s.logger.Warn("pipeline: usage record failed", "task", taskID, "err", err)
		}
	}
}

// agentOutcomeError maps a terminal agent run to a StageError, or nil on
// success. Cancellation cancels the pipeline run durably (Store.Cancel) —
// unless the emergency stop caused it, in which case the stage fails as
// `interrupted` and the driver PARKS the run (it stays active at the current
// stage and resumes on estop-off/restart) so the history distinguishes an
// estop from a user cancel without losing the run.
func (s *PipelineService) agentOutcomeError(ctx context.Context, taskID string, res supervisor.RunResult) error {
	if res.Cancelled {
		return s.interruptedOrCancelledError(taskID, "cancelled")
	}
	if res.Failed {
		cat := categoryForFailureClass(res.Outcome.Failure)
		reason := "agent run failed"
		if res.Outcome.Failure != nil && res.Outcome.Failure.Reason != "" {
			reason = res.Outcome.Failure.Reason
		}
		return &pipeline.StageError{Category: cat, Reason: reason}
	}
	return nil
}

// interruptedOrCancelledError implements the cancel-vs-estop decision shared
// by agent-run cancellation and verification interruption (SIGINT): with the
// emergency stop on, the stage fails as `interrupted` (the driver parks the
// run; it resumes on estop-off/restart); otherwise the run is cancelled
// durably (Store.Cancel) and a plain error ends the drive — the driver's
// failStage observes ErrRunTerminal and Drive returns nil with the cancelled
// outcome durable.
func (s *PipelineService) interruptedOrCancelledError(taskID, reason string) error {
	if on, estopReason, err := s.store.EmergencyStop(context.Background()); err == nil && on {
		if estopReason == "" {
			estopReason = "emergency stop engaged"
		}
		return &pipeline.StageError{Category: pipeline.FailureInterrupted, Reason: estopReason}
	}
	if err := s.store.Cancel(context.Background(), taskID, reason); err != nil {
		s.logger.Warn("pipeline: cancel after interruption failed", "task", taskID, "err", err)
	}
	return errors.New("pipeline: run cancelled")
}

// categoryForFailureClass maps the adapter failure taxonomy (spec §32) onto
// the pipeline failure categories.
func categoryForFailureClass(f *protocol.FailurePayload) pipeline.FailureCategory {
	if f == nil {
		return pipeline.FailureAgentUnavailable
	}
	switch f.Class {
	case protocol.FailureProviderAuth:
		return pipeline.FailureAgentAuthUnavailable
	case protocol.FailureProviderQuota, protocol.FailureBudgetExceeded:
		return pipeline.FailureQuotaExceeded
	case protocol.FailureProviderRateLimit, protocol.FailureProviderCapacity:
		return pipeline.FailureRateLimited
	case protocol.FailureTimeout:
		return pipeline.FailureProviderTimeout
	case protocol.FailureMalformedOutput:
		return pipeline.FailureInvalidAgentOutput
	case protocol.FailureScopeViolation, protocol.FailurePolicyViolation:
		return pipeline.FailurePolicyRejection
	default:
		return pipeline.FailureAgentUnavailable
	}
}

// runAgentText runs one agent prompt and returns the concatenated assistant
// text (message.delta deltas), used as review.RunFunc.
func (s *PipelineService) runAgentText(ctx context.Context, taskID string, ws *workspace.Workspace, params pipelineParams, prompt string) (string, error) {
	res, err := s.runAgent(ctx, taskID, ws, params, prompt)
	if err != nil {
		return "", err
	}
	if stageErr := s.agentOutcomeError(ctx, taskID, res); stageErr != nil {
		return "", stageErr
	}
	var b strings.Builder
	for _, ev := range res.Events {
		if ev.Type == protocol.EventMessageDelta && ev.Message != nil {
			b.WriteString(ev.Message.Delta)
		}
	}
	return b.String(), nil
}

// buildExecutePrompt renders the execute-stage prompt from the task and (when
// available) the compiled specification's objective + acceptance criteria.
func buildExecutePrompt(ctx context.Context, specs *task.SpecificationStore, taskID string, params pipelineParams) string {
	var b strings.Builder
	b.WriteString(params.Description)
	if spec, err := specs.GetLatest(ctx, taskID); err == nil && spec.Objective != "" {
		b.WriteString("\n\nObjective: ")
		b.WriteString(spec.Objective)
		if len(spec.AcceptanceCriteria) > 0 {
			b.WriteString("\n\nAcceptance criteria:")
			for _, ac := range spec.AcceptanceCriteria {
				b.WriteString("\n- ")
				b.WriteString(ac.Statement)
			}
		}
	}
	b.WriteString("\n\nWork only inside this workspace. Do not touch any other checkout.")
	return b.String()
}

// evidenceJSON persists a stage's evidence payload in the content-addressed
// artifact store and returns its ref ("artifact:<hash>"). Evidence problems
// never fail a stage — a plain inline ref is the fallback.
func (s *PipelineService) evidenceJSON(ctx context.Context, taskID, kind string, payload any) string {
	b, err := json.Marshal(payload)
	if err != nil {
		return kind + ":unserializable"
	}
	hash, _, err := s.artifacts.Write(b)
	if err != nil {
		s.logger.Warn("pipeline: evidence write failed", "task", taskID, "kind", kind, "err", err)
		return kind + ":unpersisted"
	}
	return "artifact:" + hash
}

func (s *PipelineService) auditStage(ctx context.Context, rc *pipeline.RunContext, status, evidence, category string) {
	payload := map[string]any{
		"stage": string(rc.Stage), "attempt": rc.Attempt, "status": status,
	}
	if evidence != "" {
		payload["evidence_ref"] = evidence
	}
	if category != "" {
		payload["failure_category"] = category
	}
	s.audit(ctx, "pipeline.stage", rc.TaskID, payload)
}
