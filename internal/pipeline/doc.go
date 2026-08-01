// Package pipeline implements the durable pipeline stage state machine
// (milestone M14-06): the persisted cursor that drives a task through the
// full production pipeline.
//
// The minimal local path models these stages, in order:
//
//	compile → plan → ready → execute → verify → review → finalize
//
// plus repair as a loop stage: verify or review failure — or an execute
// stage that produced no code changes — enters repair, which performs ONE
// bounded repair attempt (agent re-run with repair context) and then
// re-enters verify. A repair policy may instead choose a full
// re-execution (repair → execute). Design / visual / delivery / post-merge
// stages are explicitly OUT OF SCOPE here: they are optional and must not
// block the local path.
//
// # Stage transition map
//
// Stage-to-stage transitions (enforced while the run is active):
//
//	compile → plan
//	plan    → ready
//	ready   → execute
//	execute → verify   (changes produced)
//	execute → repair   (no code changes; repair re-runs the agent)
//	verify  → review   (verification passed)
//	verify  → repair   (verification failed)
//	review  → finalize (review accepted)
//	review  → repair   (changes required)
//	repair  → verify   (repair attempt applied; re-run verification)
//	repair  → execute  (repair policy chooses a full re-execution)
//
// Re-entering the current stage (same stage + same attempt) is always a
// no-op that returns the existing "entered" record — this is the crash-
// recovery path, and is idempotent at the DB level via
// UNIQUE(task_id, stage, attempt, status).
//
// # Run-state transition map
//
// Run states (pipeline_runs.run_state):
//
//	active           — the driver may advance stages
//	waiting_quota    — non-terminal wait; entered from execute/verify on
//	                   quota/rate-limit exhaustion
//	blocked          — non-terminal wait; entered from any active stage on a
//	                   blocking condition (e.g. unresolved lease conflict)
//	completed        — terminal (only reachable from the finalize stage)
//	failed           — terminal
//	cancelled        — terminal (legal from any non-terminal state)
//	repair_exhausted — terminal (repair_attempt reached max_repair_attempts)
//
// Legal run-state transitions:
//
//	active        → waiting_quota | blocked | failed | cancelled |
//	                repair_exhausted | completed
//	waiting_quota → active | failed | cancelled
//	blocked       → active | failed | cancelled
//	(terminal)    → none
//
// Resuming from a wait state is expressed as a stage transition to ready
// (waiting_quota → ready, blocked → ready): the run is re-activated and the
// driver re-dispatches from ready. This is the only stage transition legal
// while the run is in a wait state.
//
// # Durability and crash recovery
//
// All state lives in SQLite (migration v10). Every mutation follows the
// persist-before-effect pattern: the intended next stage is durable (a
// pipeline_stage_records row) before the driver performs any external effect
// for that stage. On startup the reconciler lists non-terminal runs
// (ListActiveRuns), calls MarkInterrupted to close any in-flight stage record
// WITHOUT advancing state, then re-drives from the persisted cursor.
//
// # Emergency stop
//
// SetEmergencyStop persists a kill switch in the control_flags table. The
// driver must call EmergencyStop before starting ANY stage and refuse to
// dispatch while it is on. The check is a single indexed primary-key lookup.
//
// The Store only owns persistence and legality enforcement; it never invokes
// agents, compilers or verifiers — wiring those handlers is the daemon's job.
package pipeline
