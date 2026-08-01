package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/pipeline"
	"neuroforge/internal/storage"
)

// nextOnHappyPath maps each stage to its successor on the passing path.
var nextOnHappyPath = map[pipeline.Stage]pipeline.Stage{
	pipeline.StageCompile: pipeline.StagePlan,
	pipeline.StagePlan:    pipeline.StageReady,
	pipeline.StageReady:   pipeline.StageExecute,
	pipeline.StageExecute: pipeline.StageVerify,
	pipeline.StageVerify:  pipeline.StageReview,
	pipeline.StageReview:  pipeline.StageFinalize,
}

// setupDB opens a fresh migrated SQLite DB in t.TempDir() with one seeded
// project, and returns the DB, a Store and the DB path (for reopen tests).
func setupDB(t *testing.T) (*storage.DB, *pipeline.Store, string) {
	t.Helper()
	path := t.TempDir() + "/state.db"
	db := openMigrated(t, path)
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.CreateProject(context.Background(), storage.Project{
		ID: "proj", Name: "T", Path: t.TempDir(), State: "IDLE",
		Profile: "LOCAL_REVIEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return db, pipeline.NewStore(db, nil), path
}

func openMigrated(t *testing.T, path string) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

// reopenStore simulates a process restart: close the current handle (if
// given) and open a fresh one against the same file.
func reopenStore(t *testing.T, db *storage.DB, path string) (*storage.DB, *pipeline.Store) {
	t.Helper()
	if db != nil {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	fresh := openMigrated(t, path)
	t.Cleanup(func() { fresh.Close() })
	return fresh, pipeline.NewStore(fresh, nil)
}

func seedTask(t *testing.T, db *storage.DB, id string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.CreateTask(context.Background(), storage.Task{
		ID: id, ProjectID: "proj", Title: "demo", Description: "d",
		Priority: "NORMAL", State: "NEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// driveTo advances the run along the happy path until it sits in target.
func driveTo(t *testing.T, st *pipeline.Store, taskID string, target pipeline.Stage) {
	t.Helper()
	ctx := context.Background()
	for {
		run, err := st.CurrentRun(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if run.CurrentStage == target {
			return
		}
		next, ok := nextOnHappyPath[run.CurrentStage]
		if !ok {
			t.Fatalf("no happy-path successor for stage %s", run.CurrentStage)
		}
		if _, err := st.Transition(ctx, taskID, next, "", ""); err != nil {
			t.Fatalf("driveTo(%s): transition %s -> %s: %v", target, run.CurrentStage, next, err)
		}
	}
}

func countRecords(t *testing.T, db *storage.DB, taskID string, stage pipeline.Stage, status pipeline.RecordStatus) int {
	t.Helper()
	var n int
	err := db.Underlying().QueryRowContext(context.Background(), `
SELECT COUNT(1) FROM pipeline_stage_records WHERE task_id = ? AND stage = ? AND status = ?`,
		taskID, string(stage), string(status)).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// enteredFinished reports whether the "entered" record for the run's current
// stage attempt has been closed (finished_at set).
func enteredFinished(t *testing.T, db *storage.DB, taskID string, stage pipeline.Stage, attempt int) bool {
	t.Helper()
	var finishedAt *string
	err := db.Underlying().QueryRowContext(context.Background(), `
SELECT finished_at FROM pipeline_stage_records WHERE task_id = ? AND stage = ? AND attempt = ? AND status = 'entered'`,
		taskID, string(stage), attempt).Scan(&finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	return finishedAt != nil && *finishedAt != ""
}

// ---- CreateRun ----

func TestCreateRun_Idempotent(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()

	run, err := st.CreateRun(ctx, "T-1", "proj", 5)
	if err != nil {
		t.Fatal(err)
	}
	if run.CurrentStage != pipeline.StageCompile || run.State != pipeline.RunActive {
		t.Fatalf("initial run = %s/%s, want compile/active", run.CurrentStage, run.State)
	}
	if run.StageAttempt != 1 || run.RepairAttempt != 0 || run.MaxRepairAttempts != 5 {
		t.Fatalf("counters = stage %d repair %d max %d, want 1/0/5",
			run.StageAttempt, run.RepairAttempt, run.MaxRepairAttempts)
	}

	// Re-create with a different max: existing row wins, nothing changes.
	again, err := st.CreateRun(ctx, "T-1", "proj", 99)
	if err != nil {
		t.Fatal(err)
	}
	if again.MaxRepairAttempts != 5 || !again.CreatedAt.Equal(run.CreatedAt) {
		t.Fatalf("re-create mutated run: %+v vs %+v", again, run)
	}
	if n := countRecords(t, db, "T-1", pipeline.StageCompile, pipeline.RecordEntered); n != 1 {
		t.Fatalf("compile entered records = %d, want 1", n)
	}

	// Default max applies when non-positive.
	seedTask(t, db, "T-2")
	def, err := st.CreateRun(ctx, "T-2", "proj", 0)
	if err != nil {
		t.Fatal(err)
	}
	if def.MaxRepairAttempts != pipeline.DefaultMaxRepairAttempts {
		t.Fatalf("default max = %d, want %d", def.MaxRepairAttempts, pipeline.DefaultMaxRepairAttempts)
	}
}

func TestCurrentRun_NotFound(t *testing.T) {
	_, st, _ := setupDB(t)
	_, err := st.CurrentRun(context.Background(), "nope")
	if !errors.Is(err, pipeline.ErrRunNotFound) {
		t.Fatalf("err = %v, want ErrRunNotFound", err)
	}
	if _, err := st.Transition(context.Background(), "nope", pipeline.StagePlan, "", ""); !errors.Is(err, pipeline.ErrRunNotFound) {
		t.Fatalf("transition err = %v, want ErrRunNotFound", err)
	}
}

// ---- Transitions ----

func TestTransition_HappyPathToCompleted(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}

	stages := []pipeline.Stage{
		pipeline.StageCompile, pipeline.StagePlan, pipeline.StageReady,
		pipeline.StageExecute, pipeline.StageVerify, pipeline.StageReview,
		pipeline.StageFinalize,
	}
	for i, stage := range stages {
		if err := st.CompleteStage(ctx, "T-1", stage, "evidence://"+string(stage)); err != nil {
			t.Fatalf("CompleteStage(%s): %v", stage, err)
		}
		if i+1 < len(stages) {
			rec, err := st.Transition(ctx, "T-1", stages[i+1], "", "")
			if err != nil {
				t.Fatalf("Transition(%s -> %s): %v", stage, stages[i+1], err)
			}
			if rec.Stage != stages[i+1] || rec.Status != pipeline.RecordEntered || rec.Attempt != 1 {
				t.Fatalf("entered record = %+v, want stage %s entered attempt 1", rec, stages[i+1])
			}
		}
	}

	for _, stage := range stages {
		if n := countRecords(t, db, "T-1", stage, pipeline.RecordCompleted); n != 1 {
			t.Fatalf("completed records for %s = %d, want 1", stage, n)
		}
		if !enteredFinished(t, db, "T-1", stage, 1) {
			t.Fatalf("entered record for %s not closed", stage)
		}
	}

	if err := st.SetRunState(ctx, "T-1", pipeline.RunStateChange{
		To: pipeline.RunCompleted, ResultRef: "forge/result/T-1",
	}); err != nil {
		t.Fatal(err)
	}
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCompleted || run.ResultRef != "forge/result/T-1" {
		t.Fatalf("final run = %s ref %q, want completed forge/result/T-1", run.State, run.ResultRef)
	}
	active, err := st.ListActiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active runs = %d, want 0", len(active))
	}
}

func TestTransition_Illegal(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}

	var inv *pipeline.ErrInvalidTransition
	if _, err := st.Transition(ctx, "T-1", pipeline.StageExecute, "", ""); !errors.As(err, &inv) {
		t.Fatalf("compile->execute err = %v, want ErrInvalidTransition", err)
	}
	if _, err := st.Transition(ctx, "T-1", pipeline.StageRepair, "", ""); !errors.As(err, &inv) {
		t.Fatalf("compile->repair err = %v, want ErrInvalidTransition", err)
	}
	// Unknown stage string.
	if _, err := st.Transition(ctx, "T-1", pipeline.Stage("design"), "", ""); err == nil {
		t.Fatal("transition to unknown stage succeeded, want error")
	}
	// Run state unchanged after rejected transitions.
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.CurrentStage != pipeline.StageCompile || run.State != pipeline.RunActive {
		t.Fatalf("run mutated by illegal transition: %+v", run)
	}
}

func TestTransition_IdempotentReentryAfterCrash(t *testing.T) {
	db, st, path := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}
	rec, err := st.Transition(ctx, "T-1", pipeline.StagePlan, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a crash: close and reopen the DB, then re-drive the same
	// transition the driver believes it may not have completed.
	db, st = reopenStore(t, db, path)

	again, err := st.Transition(ctx, "T-1", pipeline.StagePlan, "", "")
	if err != nil {
		t.Fatalf("re-entry after crash: %v", err)
	}
	if again.ID != rec.ID {
		t.Fatalf("re-entry returned record %d, want existing %d", again.ID, rec.ID)
	}
	if n := countRecords(t, db, "T-1", pipeline.StagePlan, pipeline.RecordEntered); n != 1 {
		t.Fatalf("plan entered records = %d, want 1", n)
	}
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.CurrentStage != pipeline.StagePlan || run.State != pipeline.RunActive {
		t.Fatalf("run = %s/%s, want plan/active", run.CurrentStage, run.State)
	}

	// The driver can keep going after recovery.
	if _, err := st.Transition(ctx, "T-1", pipeline.StageReady, "", ""); err != nil {
		t.Fatalf("transition after recovery: %v", err)
	}
}

func TestTransition_ResumeFromWaitState(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}
	driveTo(t, st, "T-1", pipeline.StageExecute)

	if err := st.SetRunState(ctx, "T-1", pipeline.RunStateChange{To: pipeline.RunWaitingQuota, Reason: "quota"}); err != nil {
		t.Fatal(err)
	}
	// Only ready is legal out of a wait state.
	var inv *pipeline.ErrInvalidTransition
	if _, err := st.Transition(ctx, "T-1", pipeline.StageVerify, "", ""); !errors.As(err, &inv) {
		t.Fatalf("waiting_quota: execute->verify err = %v, want ErrInvalidTransition", err)
	}
	if _, err := st.Transition(ctx, "T-1", pipeline.StageReady, "quota replenished", ""); err != nil {
		t.Fatalf("waiting_quota: execute->ready: %v", err)
	}
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.CurrentStage != pipeline.StageReady || run.State != pipeline.RunActive {
		t.Fatalf("resumed run = %s/%s, want ready/active", run.CurrentStage, run.State)
	}
}

// ---- ListActiveRuns / restart recovery ----

func TestListActiveRuns_AfterRestart(t *testing.T) {
	db, st, path := setupDB(t)
	ctx := context.Background()
	for _, id := range []string{"T-1", "T-2", "T-3", "T-4"} {
		seedTask(t, db, id)
		if _, err := st.CreateRun(ctx, id, "proj", 3); err != nil {
			t.Fatal(err)
		}
	}
	// T-1: active at compile. T-2: waiting_quota at execute. T-3: cancelled.
	// T-4: failed.
	driveTo(t, st, "T-2", pipeline.StageExecute)
	if err := st.SetRunState(ctx, "T-2", pipeline.RunStateChange{To: pipeline.RunWaitingQuota}); err != nil {
		t.Fatal(err)
	}
	if err := st.Cancel(ctx, "T-3", "user"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRunState(ctx, "T-4", pipeline.RunStateChange{To: pipeline.RunFailed, Reason: "boom"}); err != nil {
		t.Fatal(err)
	}

	db, st = reopenStore(t, db, path)

	runs, err := st.ListActiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]pipeline.RunState{}
	for _, r := range runs {
		got[r.TaskID] = r.State
	}
	if len(got) != 2 || got["T-1"] != pipeline.RunActive || got["T-2"] != pipeline.RunWaitingQuota {
		t.Fatalf("active runs = %v, want T-1/active + T-2/waiting_quota", got)
	}
}

// ---- MarkInterrupted ----

func TestMarkInterrupted(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}
	driveTo(t, st, "T-1", pipeline.StageExecute)

	if err := st.MarkInterrupted(ctx, "T-1", "daemon killed"); err != nil {
		t.Fatal(err)
	}
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	// State NOT advanced.
	if run.CurrentStage != pipeline.StageExecute || run.State != pipeline.RunActive || run.StageAttempt != 1 {
		t.Fatalf("run advanced by MarkInterrupted: %+v", run)
	}
	if !enteredFinished(t, db, "T-1", pipeline.StageExecute, 1) {
		t.Fatal("in-flight entered record not closed")
	}
	if n := countRecords(t, db, "T-1", pipeline.StageExecute, pipeline.RecordFailed); n != 1 {
		t.Fatalf("failed records = %d, want 1", n)
	}
	var cat string
	if err := db.Underlying().QueryRowContext(ctx, `
SELECT failure_category FROM pipeline_stage_records
WHERE task_id = 'T-1' AND stage = 'execute' AND status = 'failed'`).Scan(&cat); err != nil {
		t.Fatal(err)
	}
	if cat != string(pipeline.FailureInterrupted) {
		t.Fatalf("category = %q, want interrupted", cat)
	}

	// Idempotent: second call changes nothing.
	if err := st.MarkInterrupted(ctx, "T-1", "daemon killed again"); err != nil {
		t.Fatal(err)
	}
	if n := countRecords(t, db, "T-1", pipeline.StageExecute, pipeline.RecordFailed); n != 1 {
		t.Fatalf("failed records after 2nd call = %d, want 1", n)
	}

	// Re-drive protocol: bump attempt, re-enter the same stage.
	if _, err := st.IncrementStageAttempt(ctx, "T-1"); err != nil {
		t.Fatal(err)
	}
	rec, err := st.Transition(ctx, "T-1", pipeline.StageExecute, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Attempt != 2 {
		t.Fatalf("re-driven attempt = %d, want 2", rec.Attempt)
	}
	if enteredFinished(t, db, "T-1", pipeline.StageExecute, 2) {
		t.Fatal("fresh entered record must be open")
	}
}

func TestMarkInterrupted_TerminalRunNoop(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}
	if err := st.Cancel(ctx, "T-1", "user"); err != nil {
		t.Fatal(err)
	}
	before := countRecords(t, db, "T-1", pipeline.StageCompile, pipeline.RecordFailed)
	if err := st.MarkInterrupted(ctx, "T-1", ""); err != nil {
		t.Fatal(err)
	}
	if after := countRecords(t, db, "T-1", pipeline.StageCompile, pipeline.RecordFailed); after != before {
		t.Fatalf("records changed on terminal run: %d -> %d", before, after)
	}
}

// ---- Repair loop and exhaustion ----

func TestRepairLoop_Exhaustion(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 2); err != nil {
		t.Fatal(err)
	}
	driveTo(t, st, "T-1", pipeline.StageVerify)

	// Two full repair cycles (max_repair_attempts = 2).
	for cycle := 1; cycle <= 2; cycle++ {
		if err := st.FailStage(ctx, "T-1", pipeline.StageVerify, pipeline.FailureTest, "tests red", ""); err != nil {
			t.Fatalf("cycle %d FailStage: %v", cycle, err)
		}
		if _, err := st.Transition(ctx, "T-1", pipeline.StageRepair, "", ""); err != nil {
			t.Fatalf("cycle %d verify->repair: %v", cycle, err)
		}
		ok, n, err := st.IncrementRepairAttempt(ctx, "T-1")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || n != cycle {
			t.Fatalf("cycle %d IncrementRepairAttempt = (%v, %d), want (true, %d)", cycle, ok, n, cycle)
		}
		if _, err := st.IncrementStageAttempt(ctx, "T-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Transition(ctx, "T-1", pipeline.StageExecute, "repair context", ""); err != nil {
			t.Fatalf("cycle %d repair->execute: %v", cycle, err)
		}
		if _, err := st.Transition(ctx, "T-1", pipeline.StageVerify, "", ""); err != nil {
			t.Fatalf("cycle %d execute->verify: %v", cycle, err)
		}
	}

	// Third failure exhausts the repair budget.
	if err := st.FailStage(ctx, "T-1", pipeline.StageVerify, pipeline.FailureTest, "still red", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Transition(ctx, "T-1", pipeline.StageRepair, "", ""); err != nil {
		t.Fatal(err)
	}
	ok, n, err := st.IncrementRepairAttempt(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if ok || n != 2 {
		t.Fatalf("IncrementRepairAttempt past max = (%v, %d), want (false, 2)", ok, n)
	}
	if err := st.SetRunState(ctx, "T-1", pipeline.RunStateChange{
		To: pipeline.RunRepairExhausted, Reason: "repair budget exhausted",
	}); err != nil {
		t.Fatal(err)
	}
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunRepairExhausted || run.RepairAttempt != 2 {
		t.Fatalf("run = %s repairs %d, want repair_exhausted/2", run.State, run.RepairAttempt)
	}
	if run.FailureCategory != pipeline.FailureTest || run.FailureReason != "repair budget exhausted" {
		t.Fatalf("failure = %s/%q, want test_failure/budget message", run.FailureCategory, run.FailureReason)
	}
	active, err := st.ListActiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("repair_exhausted run still listed active: %v", active)
	}
	// Terminal: no further repair increments.
	if _, _, err := st.IncrementRepairAttempt(ctx, "T-1"); !errors.Is(err, pipeline.ErrRunTerminal) {
		t.Fatalf("increment on terminal run err = %v, want ErrRunTerminal", err)
	}
	// History is intact: two verify failures recorded.
	if got := countRecords(t, db, "T-1", pipeline.StageVerify, pipeline.RecordFailed); got != 3 {
		t.Fatalf("verify failed records = %d, want 3 (one per cycle + final)", got)
	}
}

// ---- Cancellation ----

func TestCancel_FromNonTerminalStates(t *testing.T) {
	for i, tc := range []struct {
		name    string
		prepare func(t *testing.T, st *pipeline.Store, taskID string)
	}{
		{"active", func(t *testing.T, st *pipeline.Store, taskID string) {}},
		{"waiting_quota", func(t *testing.T, st *pipeline.Store, taskID string) {
			driveTo(t, st, taskID, pipeline.StageExecute)
			if err := st.SetRunState(context.Background(), taskID, pipeline.RunStateChange{To: pipeline.RunWaitingQuota}); err != nil {
				t.Fatal(err)
			}
		}},
		{"blocked", func(t *testing.T, st *pipeline.Store, taskID string) {
			if err := st.SetRunState(context.Background(), taskID, pipeline.RunStateChange{To: pipeline.RunBlocked}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, st, _ := setupDB(t)
			taskID := fmt.Sprintf("T-%d", i+1)
			seedTask(t, db, taskID)
			ctx := context.Background()
			if _, err := st.CreateRun(ctx, taskID, "proj", 3); err != nil {
				t.Fatal(err)
			}
			tc.prepare(t, st, taskID)

			if err := st.Cancel(ctx, taskID, "user request"); err != nil {
				t.Fatal(err)
			}
			run, err := st.CurrentRun(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if run.State != pipeline.RunCancelled {
				t.Fatalf("state = %s, want cancelled", run.State)
			}
			if run.FailureCategory != pipeline.FailureCancelled || run.FailureReason != "user request" {
				t.Fatalf("failure = %s/%q, want cancelled/user request", run.FailureCategory, run.FailureReason)
			}
			// Durable: survives reopen.
			path := db.Path()
			db, st = reopenStore(t, db, path)
			run, err = st.CurrentRun(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if run.State != pipeline.RunCancelled {
				t.Fatalf("after reopen state = %s, want cancelled", run.State)
			}
			// Idempotent: cancelling again is a no-op.
			if err := st.Cancel(ctx, taskID, "again"); err != nil {
				t.Fatal(err)
			}
			run, err = st.CurrentRun(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if run.FailureReason != "user request" {
				t.Fatalf("second cancel overwrote reason: %q", run.FailureReason)
			}
		})
	}
}

func TestCancel_AfterTerminalIsNoop(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}
	driveTo(t, st, "T-1", pipeline.StageFinalize)
	if err := st.SetRunState(ctx, "T-1", pipeline.RunStateChange{To: pipeline.RunCompleted, ResultRef: "ref"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Cancel(ctx, "T-1", "too late"); err != nil {
		t.Fatal(err)
	}
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCompleted {
		t.Fatalf("cancel moved terminal run to %s", run.State)
	}
}

// ---- FailStage / SetRunState validation ----

func TestFailStage_Validation(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}
	if err := st.FailStage(ctx, "T-1", pipeline.StageCompile, "not_a_category", "", ""); err == nil {
		t.Fatal("unknown category accepted")
	}
	// Failing a stage the run is not in is rejected.
	var inv *pipeline.ErrInvalidTransition
	if err := st.FailStage(ctx, "T-1", pipeline.StageVerify, pipeline.FailureTest, "", ""); !errors.As(err, &inv) {
		t.Fatalf("fail non-current stage err = %v, want ErrInvalidTransition", err)
	}
	// Real failure lands on the run row (last failure wins).
	if err := st.FailStage(ctx, "T-1", pipeline.StageCompile, pipeline.FailureCompile, "syntax error", "ev://1"); err != nil {
		t.Fatal(err)
	}
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.FailureCategory != pipeline.FailureCompile || run.FailureReason != "syntax error" {
		t.Fatalf("failure = %s/%q", run.FailureCategory, run.FailureReason)
	}
	// Idempotent: failing the same attempt again is a no-op.
	if err := st.FailStage(ctx, "T-1", pipeline.StageCompile, pipeline.FailureCompile, "syntax error", "ev://1"); err != nil {
		t.Fatal(err)
	}
	if n := countRecords(t, db, "T-1", pipeline.StageCompile, pipeline.RecordFailed); n != 1 {
		t.Fatalf("failed records = %d, want 1", n)
	}
}

func TestSetRunState_Restrictions(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}
	// waiting_quota only from execute/verify.
	if err := st.SetRunState(ctx, "T-1", pipeline.RunStateChange{To: pipeline.RunWaitingQuota}); err == nil {
		t.Fatal("waiting_quota from compile accepted")
	}
	// completed only from finalize.
	if err := st.SetRunState(ctx, "T-1", pipeline.RunStateChange{To: pipeline.RunCompleted}); err == nil {
		t.Fatal("completed from compile accepted")
	}
	// Unknown state rejected.
	if err := st.SetRunState(ctx, "T-1", pipeline.RunStateChange{To: "bogus"}); err == nil {
		t.Fatal("unknown state accepted")
	}
	// Blocked is reachable from any active stage, and resumable.
	if err := st.SetRunState(ctx, "T-1", pipeline.RunStateChange{To: pipeline.RunBlocked}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRunState(ctx, "T-1", pipeline.RunStateChange{To: pipeline.RunActive}); err != nil {
		t.Fatal(err)
	}
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunActive {
		t.Fatalf("state = %s, want active", run.State)
	}
}

// ---- Emergency stop ----

func TestEmergencyStop_Persistence(t *testing.T) {
	db, st, path := setupDB(t)
	ctx := context.Background()

	on, reason, err := st.EmergencyStop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if on || reason != "" {
		t.Fatalf("fresh DB stop = (%v, %q), want (false, \"\")", on, reason)
	}

	if err := st.SetEmergencyStop(ctx, true, "provider meltdown"); err != nil {
		t.Fatal(err)
	}
	on, reason, err = st.EmergencyStop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !on || reason != "provider meltdown" {
		t.Fatalf("stop = (%v, %q), want (true, \"provider meltdown\")", on, reason)
	}

	// Survives restart.
	db, st = reopenStore(t, db, path)
	on, reason, err = st.EmergencyStop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !on || reason != "provider meltdown" {
		t.Fatalf("after reopen stop = (%v, %q), want (true, \"provider meltdown\")", on, reason)
	}

	if err := st.SetEmergencyStop(ctx, false, ""); err != nil {
		t.Fatal(err)
	}
	on, reason, err = st.EmergencyStop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if on || reason != "" {
		t.Fatalf("cleared stop = (%v, %q), want (false, \"\")", on, reason)
	}
}

// ---- Concurrency ----

func TestConcurrentTransition_SameTarget(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = st.Transition(ctx, "T-1", pipeline.StagePlan, "", "")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.CurrentStage != pipeline.StagePlan {
		t.Fatalf("stage = %s, want plan", run.CurrentStage)
	}
	if got := countRecords(t, db, "T-1", pipeline.StagePlan, pipeline.RecordEntered); got != 1 {
		t.Fatalf("plan entered records = %d, want 1 (double-advance)", got)
	}
}

func TestConcurrentTransition_DivergentTargets(t *testing.T) {
	db, st, _ := setupDB(t)
	seedTask(t, db, "T-1")
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, "T-1", "proj", 3); err != nil {
		t.Fatal(err)
	}
	driveTo(t, st, "T-1", pipeline.StageReview)

	// Targets are deliberately non-chainable: finalize has no outgoing edges
	// and repair -> finalize is illegal, so exactly one group can win (with
	// verify as the fork stage, review -> repair would be a legal chain and
	// both groups could legitimately advance the run in sequence).
	const perGroup = 4
	var wg sync.WaitGroup
	errs := make([]error, 2*perGroup)
	fire := func(i int, to pipeline.Stage) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = st.Transition(ctx, "T-1", to, "", "")
		}()
	}
	for i := 0; i < perGroup; i++ {
		fire(i, pipeline.StageFinalize)
		fire(perGroup+i, pipeline.StageRepair)
	}
	wg.Wait()

	run, err := st.CurrentRun(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	var winner, loser pipeline.Stage
	switch run.CurrentStage {
	case pipeline.StageFinalize:
		winner, loser = pipeline.StageFinalize, pipeline.StageRepair
	case pipeline.StageRepair:
		winner, loser = pipeline.StageRepair, pipeline.StageFinalize
	default:
		t.Fatalf("stage = %s, want finalize or repair", run.CurrentStage)
	}
	if got := countRecords(t, db, "T-1", winner, pipeline.RecordEntered); got != 1 {
		t.Fatalf("winner %s entered records = %d, want 1", winner, got)
	}
	if got := countRecords(t, db, "T-1", loser, pipeline.RecordEntered); got != 0 {
		t.Fatalf("loser %s entered records = %d, want 0", loser, got)
	}
	sawLoser := false
	for _, err := range errs {
		if err == nil {
			continue
		}
		// A loser either reads the pre-commit cursor and fails the
		// conditional UPDATE (ErrConcurrentModification), or reads the
		// post-commit cursor and is rejected by the transition table
		// (ErrInvalidTransition). Both are acceptable "you lost" signals;
		// what matters is that no loser advanced the run.
		var inv *pipeline.ErrInvalidTransition
		if !errors.Is(err, pipeline.ErrConcurrentModification) && !errors.As(err, &inv) {
			t.Fatalf("unexpected error: %v", err)
		}
		sawLoser = true
	}
	if !sawLoser {
		t.Fatal("expected at least one losing goroutine to be rejected")
	}
}
