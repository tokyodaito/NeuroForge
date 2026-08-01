package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"neuroforge/internal/pipeline"
	"neuroforge/internal/storage"
)

// ---- scripted fake handlers ----

type simpleOutcome struct {
	evidence string
	err      error
}

type execOutcome struct {
	evidence string
	changed  int
	err      error
}

type verifyOutcome struct {
	passed   bool
	evidence string
	category pipeline.FailureCategory
	err      error
}

type reviewOutcome struct {
	approved bool
	evidence string
	err      error
}

// scripted serves handler outcomes from per-stage queues; when a queue is
// empty a passing default is produced. An optional hook runs inside every
// handler invocation (used to set the emergency stop or cancel mid-run).
type scripted struct {
	mu      sync.Mutex
	calls   []pipeline.Stage
	hook    func(stage pipeline.Stage, rc *pipeline.RunContext)
	simpleQ map[pipeline.Stage][]simpleOutcome
	execQ   []execOutcome
	verifyQ []verifyOutcome
	reviewQ []reviewOutcome
	repairQ []simpleOutcome
}

func newScripted() *scripted {
	return &scripted{simpleQ: map[pipeline.Stage][]simpleOutcome{}}
}

func (s *scripted) handlers() pipeline.Handlers {
	return pipeline.Handlers{
		Compile:  s.simple(pipeline.StageCompile),
		Plan:     s.simple(pipeline.StagePlan),
		Ready:    s.simple(pipeline.StageReady),
		Finalize: s.simple(pipeline.StageFinalize),
		Execute:  s.execute,
		Verify:   s.verify,
		Review:   s.review,
		Repair:   s.repair,
	}
}

func (s *scripted) simple(stage pipeline.Stage) pipeline.SimpleHandler {
	return func(ctx context.Context, rc *pipeline.RunContext) (string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, stage)
		if s.hook != nil {
			s.hook(stage, rc)
		}
		if q := s.simpleQ[stage]; len(q) > 0 {
			out := q[0]
			s.simpleQ[stage] = q[1:]
			return out.evidence, out.err
		}
		return fmt.Sprintf("ev/%s/%d", stage, len(s.calls)), nil
	}
}

func (s *scripted) execute(ctx context.Context, rc *pipeline.RunContext) (string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, pipeline.StageExecute)
	if s.hook != nil {
		s.hook(pipeline.StageExecute, rc)
	}
	if len(s.execQ) > 0 {
		out := s.execQ[0]
		s.execQ = s.execQ[1:]
		return out.evidence, out.changed, out.err
	}
	return "ev/execute", 1, nil
}

func (s *scripted) verify(ctx context.Context, rc *pipeline.RunContext) (bool, string, pipeline.FailureCategory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, pipeline.StageVerify)
	if s.hook != nil {
		s.hook(pipeline.StageVerify, rc)
	}
	if len(s.verifyQ) > 0 {
		out := s.verifyQ[0]
		s.verifyQ = s.verifyQ[1:]
		return out.passed, out.evidence, out.category, out.err
	}
	return true, "ev/verify", "", nil
}

func (s *scripted) review(ctx context.Context, rc *pipeline.RunContext) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, pipeline.StageReview)
	if s.hook != nil {
		s.hook(pipeline.StageReview, rc)
	}
	if len(s.reviewQ) > 0 {
		out := s.reviewQ[0]
		s.reviewQ = s.reviewQ[1:]
		return out.approved, out.evidence, out.err
	}
	return true, "ev/review", nil
}

func (s *scripted) repair(ctx context.Context, rc *pipeline.RunContext) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, pipeline.StageRepair)
	if s.hook != nil {
		s.hook(pipeline.StageRepair, rc)
	}
	if len(s.repairQ) > 0 {
		out := s.repairQ[0]
		s.repairQ = s.repairQ[1:]
		return out.evidence, out.err
	}
	return "ev/repair", nil
}

func (s *scripted) count(stage pipeline.Stage) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c == stage {
			n++
		}
	}
	return n
}

func (s *scripted) totalCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// ---- helpers ----

// setupRun seeds a DB with a project + task and creates the pipeline run.
func setupRun(t *testing.T, taskID string, maxRepair int) (*storage.DB, *pipeline.Store) {
	t.Helper()
	db, store, _ := setupDB(t)
	seedTask(t, db, taskID)
	if _, err := store.CreateRun(context.Background(), taskID, "proj", maxRepair); err != nil {
		t.Fatal(err)
	}
	return db, store
}

func newDriver(t *testing.T, store *pipeline.Store, h pipeline.Handlers) *pipeline.Driver {
	t.Helper()
	d, err := pipeline.NewDriver(store, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// recordSequence returns the stage history as "stage/attempt/status" strings
// ordered by row id.
func recordSequence(t *testing.T, db *storage.DB, taskID string) []string {
	t.Helper()
	rows, err := db.Underlying().QueryContext(context.Background(), `
SELECT stage, attempt, status FROM pipeline_stage_records WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var stage, status string
		var attempt int
		if err := rows.Scan(&stage, &attempt, &status); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s/%d/%s", stage, attempt, status))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSequence(t *testing.T, db *storage.DB, taskID string, want []string) {
	t.Helper()
	got := recordSequence(t, db, taskID)
	if len(got) != len(want) {
		t.Fatalf("record sequence length = %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record %d = %s, want %s\ngot:  %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}

// happyPrefix is the record sequence for compile→plan→ready→execute all
// succeeding at attempt 1.
var happyPrefix = []string{
	"compile/1/entered", "compile/1/completed",
	"plan/1/entered", "plan/1/completed",
	"ready/1/entered", "ready/1/completed",
	"execute/1/entered", "execute/1/completed",
}

// ---- tests ----

func TestNewDriverRejectsNil(t *testing.T) {
	db, store, _ := setupDB(t)
	_ = db
	if _, err := pipeline.NewDriver(nil, newScripted().handlers(), nil); err == nil {
		t.Error("NewDriver(nil store) succeeded, want error")
	}
	full := newScripted().handlers()
	cases := map[string]func(h *pipeline.Handlers){
		"Compile":  func(h *pipeline.Handlers) { h.Compile = nil },
		"Plan":     func(h *pipeline.Handlers) { h.Plan = nil },
		"Ready":    func(h *pipeline.Handlers) { h.Ready = nil },
		"Finalize": func(h *pipeline.Handlers) { h.Finalize = nil },
		"Execute":  func(h *pipeline.Handlers) { h.Execute = nil },
		"Verify":   func(h *pipeline.Handlers) { h.Verify = nil },
		"Review":   func(h *pipeline.Handlers) { h.Review = nil },
		"Repair":   func(h *pipeline.Handlers) { h.Repair = nil },
	}
	for name, nilOut := range cases {
		h := full
		nilOut(&h)
		if _, err := pipeline.NewDriver(store, h, nil); err == nil {
			t.Errorf("NewDriver with nil %s handler succeeded, want error", name)
		}
	}
}

func TestDriveHappyPath(t *testing.T) {
	db, store := setupRun(t, "T-happy", 2)
	sc := newScripted()
	d := newDriver(t, store, sc.handlers())

	if err := d.Drive(context.Background(), "T-happy"); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	run, err := store.CurrentRun(context.Background(), "T-happy")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCompleted {
		t.Fatalf("run state = %s, want completed", run.State)
	}
	if run.ResultRef == "" {
		t.Error("result_ref is empty, want finalize evidence ref")
	}
	if run.StageAttempt != 1 || run.RepairAttempt != 0 {
		t.Errorf("attempts = stage:%d repair:%d, want 1/0", run.StageAttempt, run.RepairAttempt)
	}

	want := append(append([]string{}, happyPrefix...),
		"verify/1/entered", "verify/1/completed",
		"review/1/entered", "review/1/completed",
		"finalize/1/entered", "finalize/1/completed",
	)
	assertSequence(t, db, "T-happy", want)

	// Idempotent re-drive: no new handler calls, no new records.
	calls := sc.totalCalls()
	if err := d.Drive(context.Background(), "T-happy"); err != nil {
		t.Fatalf("re-Drive: %v", err)
	}
	if sc.totalCalls() != calls {
		t.Errorf("re-Drive invoked handlers (%d -> %d calls)", calls, sc.totalCalls())
	}
	assertSequence(t, db, "T-happy", want)
}

func TestDriveVerifyFailureThenRepair(t *testing.T) {
	db, store := setupRun(t, "T-vfail", 3)
	sc := newScripted()
	sc.verifyQ = []verifyOutcome{
		{passed: false, evidence: "ev/verify-fail", category: pipeline.FailureTest},
	}
	d := newDriver(t, store, sc.handlers())

	if err := d.Drive(context.Background(), "T-vfail"); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	run, err := store.CurrentRun(context.Background(), "T-vfail")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCompleted {
		t.Fatalf("run state = %s, want completed", run.State)
	}
	if run.RepairAttempt != 1 || run.StageAttempt != 2 {
		t.Errorf("attempts = stage:%d repair:%d, want 2/1", run.StageAttempt, run.RepairAttempt)
	}

	want := append(append([]string{}, happyPrefix...),
		"verify/1/entered", "verify/1/failed",
		"repair/1/entered", "repair/1/completed",
		"verify/2/entered", "verify/2/completed",
		"review/2/entered", "review/2/completed",
		"finalize/2/entered", "finalize/2/completed",
	)
	assertSequence(t, db, "T-vfail", want)

	// The failed verify record carries the handler's category.
	var cat string
	if err := db.Underlying().QueryRowContext(context.Background(), `
SELECT failure_category FROM pipeline_stage_records
WHERE task_id = 'T-vfail' AND stage = 'verify' AND status = 'failed'`).Scan(&cat); err != nil {
		t.Fatal(err)
	}
	if cat != string(pipeline.FailureTest) {
		t.Errorf("verify failure category = %s, want test_failure", cat)
	}
}

func TestDriveReviewRejectionThenRepair(t *testing.T) {
	db, store := setupRun(t, "T-rfail", 3)
	sc := newScripted()
	sc.reviewQ = []reviewOutcome{
		{approved: false, evidence: "ev/review-reject"},
	}
	d := newDriver(t, store, sc.handlers())

	if err := d.Drive(context.Background(), "T-rfail"); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	run, err := store.CurrentRun(context.Background(), "T-rfail")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCompleted {
		t.Fatalf("run state = %s, want completed", run.State)
	}
	if run.RepairAttempt != 1 || run.StageAttempt != 2 {
		t.Errorf("attempts = stage:%d repair:%d, want 2/1", run.StageAttempt, run.RepairAttempt)
	}

	want := append(append([]string{}, happyPrefix...),
		"verify/1/entered", "verify/1/completed",
		"review/1/entered", "review/1/failed",
		"repair/1/entered", "repair/1/completed",
		"verify/2/entered", "verify/2/completed",
		"review/2/entered", "review/2/completed",
		"finalize/2/entered", "finalize/2/completed",
	)
	assertSequence(t, db, "T-rfail", want)

	var cat string
	if err := db.Underlying().QueryRowContext(context.Background(), `
SELECT failure_category FROM pipeline_stage_records
WHERE task_id = 'T-rfail' AND stage = 'review' AND status = 'failed'`).Scan(&cat); err != nil {
		t.Fatal(err)
	}
	if cat != string(pipeline.FailureReviewRejection) {
		t.Errorf("review failure category = %s, want review_rejection", cat)
	}
}

func TestDriveRepairExhaustion(t *testing.T) {
	db, store := setupRun(t, "T-exhaust", 2)
	sc := newScripted()
	// Verification never passes.
	sc.verifyQ = []verifyOutcome{
		{passed: false, category: pipeline.FailureTest},
		{passed: false, category: pipeline.FailureTest},
		{passed: false, category: pipeline.FailureTest},
		{passed: false, category: pipeline.FailureTest},
	}
	d := newDriver(t, store, sc.handlers())

	if err := d.Drive(context.Background(), "T-exhaust"); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	run, err := store.CurrentRun(context.Background(), "T-exhaust")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunRepairExhausted {
		t.Fatalf("run state = %s, want repair_exhausted", run.State)
	}
	if run.RepairAttempt != 2 {
		t.Errorf("repair attempts = %d, want 2 (max)", run.RepairAttempt)
	}
	if n := sc.count(pipeline.StageRepair); n != 2 {
		t.Errorf("repair handler invoked %d times, want 2", n)
	}
	if n := sc.count(pipeline.StageVerify); n != 3 {
		t.Errorf("verify handler invoked %d times, want 3", n)
	}

	want := append(append([]string{}, happyPrefix...),
		"verify/1/entered", "verify/1/failed",
		"repair/1/entered", "repair/1/completed",
		"verify/2/entered", "verify/2/failed",
		"repair/2/entered", "repair/2/completed",
		"verify/3/entered", "verify/3/failed",
	)
	assertSequence(t, db, "T-exhaust", want)
}

func TestDriveExecuteNoCodeChanges(t *testing.T) {
	db, store := setupRun(t, "T-nocode", 3)
	sc := newScripted()
	sc.execQ = []execOutcome{
		{evidence: "ev/execute-empty", changed: 0},
	}
	d := newDriver(t, store, sc.handlers())

	if err := d.Drive(context.Background(), "T-nocode"); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	run, err := store.CurrentRun(context.Background(), "T-nocode")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCompleted {
		t.Fatalf("run state = %s, want completed", run.State)
	}
	if run.RepairAttempt != 1 {
		t.Errorf("repair attempts = %d, want 1", run.RepairAttempt)
	}
	// Repair re-enters verify directly; execute ran exactly once.
	if n := sc.count(pipeline.StageExecute); n != 1 {
		t.Errorf("execute handler invoked %d times, want 1", n)
	}

	want := []string{
		"compile/1/entered", "compile/1/completed",
		"plan/1/entered", "plan/1/completed",
		"ready/1/entered", "ready/1/completed",
		"execute/1/entered", "execute/1/failed",
		"repair/1/entered", "repair/1/completed",
		"verify/2/entered", "verify/2/completed",
		"review/2/entered", "review/2/completed",
		"finalize/2/entered", "finalize/2/completed",
	}
	assertSequence(t, db, "T-nocode", want)

	var cat string
	if err := db.Underlying().QueryRowContext(context.Background(), `
SELECT failure_category FROM pipeline_stage_records
WHERE task_id = 'T-nocode' AND stage = 'execute' AND status = 'failed'`).Scan(&cat); err != nil {
		t.Fatal(err)
	}
	if cat != string(pipeline.FailureNoCodeChanges) {
		t.Errorf("execute failure category = %s, want no_code_changes", cat)
	}
}

func TestDriveQuotaErrorWaitsAndResumes(t *testing.T) {
	_, store := setupRun(t, "T-quota", 3)
	sc := newScripted()
	sc.execQ = []execOutcome{
		{err: &pipeline.StageError{Category: pipeline.FailureQuotaExceeded, Reason: "provider quota spent"}},
	}
	d := newDriver(t, store, sc.handlers())

	if err := d.Drive(context.Background(), "T-quota"); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	run, err := store.CurrentRun(context.Background(), "T-quota")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunWaitingQuota {
		t.Fatalf("run state = %s, want waiting_quota", run.State)
	}
	if run.FailureCategory != pipeline.FailureQuotaExceeded {
		t.Errorf("failure category = %s, want quota_exceeded", run.FailureCategory)
	}

	// Driving a waiting run is a no-op: no handlers run.
	calls := sc.totalCalls()
	if err := d.Drive(context.Background(), "T-quota"); err != nil {
		t.Fatalf("re-Drive waiting run: %v", err)
	}
	if sc.totalCalls() != calls {
		t.Errorf("Drive on waiting_quota invoked handlers (%d -> %d)", calls, sc.totalCalls())
	}

	// Quota refilled: resume re-dispatch from ready, then the run completes.
	if _, err := store.Transition(context.Background(), "T-quota", pipeline.StageReady, "quota refilled", ""); err != nil {
		t.Fatalf("resume transition: %v", err)
	}
	if err := d.Drive(context.Background(), "T-quota"); err != nil {
		t.Fatalf("Drive after resume: %v", err)
	}
	run, err = store.CurrentRun(context.Background(), "T-quota")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCompleted {
		t.Fatalf("run state after resume = %s, want completed", run.State)
	}
}

func TestDriveHandlerErrorFailsRun(t *testing.T) {
	t.Run("categorised stage error", func(t *testing.T) {
		_, store := setupRun(t, "T-errcat", 3)
		sc := newScripted()
		sc.verifyQ = []verifyOutcome{
			{err: &pipeline.StageError{Category: pipeline.FailureGit, Reason: "git status exploded"}},
		}
		d := newDriver(t, store, sc.handlers())

		if err := d.Drive(context.Background(), "T-errcat"); err != nil {
			t.Fatalf("Drive: %v", err)
		}
		run, err := store.CurrentRun(context.Background(), "T-errcat")
		if err != nil {
			t.Fatal(err)
		}
		if run.State != pipeline.RunFailed {
			t.Fatalf("run state = %s, want failed", run.State)
		}
		if run.FailureCategory != pipeline.FailureGit {
			t.Errorf("failure category = %s, want git_failure", run.FailureCategory)
		}
	})

	t.Run("plain error becomes invariant violation", func(t *testing.T) {
		_, store := setupRun(t, "T-errplain", 3)
		sc := newScripted()
		sc.execQ = []execOutcome{
			{err: errors.New("boom")},
		}
		d := newDriver(t, store, sc.handlers())

		if err := d.Drive(context.Background(), "T-errplain"); err != nil {
			t.Fatalf("Drive: %v", err)
		}
		run, err := store.CurrentRun(context.Background(), "T-errplain")
		if err != nil {
			t.Fatal(err)
		}
		if run.State != pipeline.RunFailed {
			t.Fatalf("run state = %s, want failed", run.State)
		}
		if run.FailureCategory != pipeline.FailureInvariantViolation {
			t.Errorf("failure category = %s, want invariant_violation", run.FailureCategory)
		}
	})
}

func TestDriveEmergencyStop(t *testing.T) {
	_, store := setupRun(t, "T-estop", 3)
	sc := newScripted()
	ctx := context.Background()
	sc.hook = func(stage pipeline.Stage, rc *pipeline.RunContext) {
		if stage == pipeline.StageCompile {
			if err := rc.Store.SetEmergencyStop(ctx, true, "operator halt"); err != nil {
				t.Errorf("set estop: %v", err)
			}
		}
	}
	d := newDriver(t, store, sc.handlers())

	err := d.Drive(ctx, "T-estop")
	if !errors.Is(err, pipeline.ErrEmergencyStopped) {
		t.Fatalf("Drive error = %v, want ErrEmergencyStopped", err)
	}
	run, rerr := store.CurrentRun(ctx, "T-estop")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if run.State != pipeline.RunActive {
		t.Fatalf("run state = %s, want active (resumable)", run.State)
	}
	if run.CurrentStage != pipeline.StagePlan {
		t.Fatalf("current stage = %s, want plan (compile finished, plan not started)", run.CurrentStage)
	}
	if n := sc.count(pipeline.StagePlan); n != 0 {
		t.Errorf("plan handler invoked %d times while stopped, want 0", n)
	}

	// Clearing the stop lets the very same run resume from plan.
	if err := store.SetEmergencyStop(ctx, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.Drive(ctx, "T-estop"); err != nil {
		t.Fatalf("Drive after clearing estop: %v", err)
	}
	run, rerr = store.CurrentRun(ctx, "T-estop")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if run.State != pipeline.RunCompleted {
		t.Fatalf("run state after resume = %s, want completed", run.State)
	}
}

func TestDriveCancelMidRun(t *testing.T) {
	_, store := setupRun(t, "T-cancel", 3)
	sc := newScripted()
	ctx := context.Background()
	sc.hook = func(stage pipeline.Stage, rc *pipeline.RunContext) {
		if stage == pipeline.StagePlan {
			if err := rc.Store.Cancel(ctx, "T-cancel", "user cancelled"); err != nil {
				t.Errorf("cancel: %v", err)
			}
		}
	}
	d := newDriver(t, store, sc.handlers())

	if err := d.Drive(ctx, "T-cancel"); err != nil {
		t.Fatalf("Drive = %v, want nil (outcome is in the store)", err)
	}
	run, err := store.CurrentRun(ctx, "T-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCancelled {
		t.Fatalf("run state = %s, want cancelled", run.State)
	}
	if run.CurrentStage != pipeline.StagePlan {
		t.Errorf("current stage = %s, want plan (no advance after cancel)", run.CurrentStage)
	}
	if n := sc.count(pipeline.StageReady); n != 0 {
		t.Errorf("ready handler invoked %d times after cancel, want 0", n)
	}
}

func TestDriveResumeAfterCrash(t *testing.T) {
	db, store := setupRun(t, "T-crash", 3)
	sc := newScripted()
	ctx := context.Background()

	// First incarnation: execute panics mid-stage (simulated process crash).
	crashed := sc.handlers()
	crashed.Execute = func(ctx context.Context, rc *pipeline.RunContext) (string, int, error) {
		panic("simulated crash")
	}
	d := newDriver(t, store, crashed)

	err := d.Drive(ctx, "T-crash")
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("Drive error = %v, want handler panic error", err)
	}
	run, rerr := store.CurrentRun(ctx, "T-crash")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if run.State != pipeline.RunActive || run.CurrentStage != pipeline.StageExecute {
		t.Fatalf("after crash: state=%s stage=%s, want active/execute", run.State, run.CurrentStage)
	}

	// Reconciler: close the in-flight record, then re-drive with a fixed
	// execute handler.
	if err := store.MarkInterrupted(ctx, "T-crash", "simulated crash"); err != nil {
		t.Fatal(err)
	}
	execCalls := 0
	fixed := sc.handlers()
	fixed.Execute = func(ctx context.Context, rc *pipeline.RunContext) (string, int, error) {
		execCalls++
		return "ev/execute-fixed", 3, nil
	}
	d = newDriver(t, store, fixed)
	if err := d.Drive(ctx, "T-crash"); err != nil {
		t.Fatalf("re-Drive: %v", err)
	}

	run, rerr = store.CurrentRun(ctx, "T-crash")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if run.State != pipeline.RunCompleted {
		t.Fatalf("run state = %s, want completed", run.State)
	}
	// No duplicate side effects: the same stage attempt was re-driven, not a
	// new one, and the repair budget was untouched.
	if run.StageAttempt != 1 || run.RepairAttempt != 0 {
		t.Errorf("attempts = stage:%d repair:%d, want 1/0", run.StageAttempt, run.RepairAttempt)
	}
	if execCalls != 1 {
		t.Errorf("fixed execute handler invoked %d times, want 1", execCalls)
	}
	if n := countRecords(t, db, "T-crash", pipeline.StageExecute, pipeline.RecordEntered); n != 1 {
		t.Errorf("execute entered records = %d, want 1", n)
	}
	if n := countRecords(t, db, "T-crash", pipeline.StageExecute, pipeline.RecordCompleted); n != 1 {
		t.Errorf("execute completed records = %d, want 1", n)
	}
	if n := countRecords(t, db, "T-crash", pipeline.StageExecute, pipeline.RecordFailed); n != 1 {
		t.Errorf("execute failed (interrupted) records = %d, want 1", n)
	}
	var cat string
	if err := db.Underlying().QueryRowContext(ctx, `
SELECT failure_category FROM pipeline_stage_records
WHERE task_id = 'T-crash' AND stage = 'execute' AND status = 'failed'`).Scan(&cat); err != nil {
		t.Fatal(err)
	}
	if cat != string(pipeline.FailureInterrupted) {
		t.Errorf("interrupted record category = %s, want interrupted", cat)
	}
}

func TestDriveConcurrentSameTask(t *testing.T) {
	_, store := setupRun(t, "T-conc", 3)
	sc := newScripted()
	d := newDriver(t, store, sc.handlers())

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = d.Drive(context.Background(), "T-conc")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Drive %d: %v", i, err)
		}
	}
	run, err := store.CurrentRun(context.Background(), "T-conc")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCompleted {
		t.Fatalf("run state = %s, want completed", run.State)
	}
	// Serialised by the per-task mutex: each stage handler ran exactly once.
	for _, stage := range []pipeline.Stage{
		pipeline.StageCompile, pipeline.StagePlan, pipeline.StageReady,
		pipeline.StageExecute, pipeline.StageVerify, pipeline.StageReview,
		pipeline.StageFinalize,
	} {
		if n := sc.count(stage); n != 1 {
			t.Errorf("stage %s handler invoked %d times, want 1", stage, n)
		}
	}
}
