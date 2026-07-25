// Package fake is the §33.1 fake coding agent. It is a deterministic, in-process
// and executable agent used by orchestration, conformance and CLI tests so that
// no real (paid) AI API is ever called (rule §36.5, §33.1).
//
// The same [Scenario] scripts drive three surfaces, so behaviour is identical
// across them:
//
//   - the in-process [Adapter] (used by orchestration tests);
//   - the declarative command-mode binary cmd/fake-coding-agent (--mode command);
//   - the native JSON-RPC plugin-mode binary cmd/fake-coding-agent (--mode jsonrpc).
//
// Supported scenarios (superset of spec §33.1):
//
//	success, quota-before-edits, quota-after-edits, rate-limit, auth-failure,
//	malformed-json, timeout, crash, partial-output, resume, cancellation,
//	scope-violation, usage-events.
package fake

import "time"

// Scenario names a deterministic fake-agent behaviour (spec §33.1). The set is a
// strict superset of the spec list; extras are required by the conformance
// suite and the task's explicit scenario list.
type Scenario string

const (
	ScenarioSuccess          Scenario = "success"
	ScenarioQuotaBeforeEdits Scenario = "quota-before-edits"
	ScenarioQuotaAfterEdits  Scenario = "quota-after-edits"
	ScenarioRateLimit        Scenario = "rate-limit"
	ScenarioAuthFailure      Scenario = "auth-failure"
	ScenarioMalformedJSON    Scenario = "malformed-json"
	ScenarioTimeout          Scenario = "timeout"
	ScenarioCrash            Scenario = "crash"
	ScenarioPartialOutput    Scenario = "partial-output"
	ScenarioResume           Scenario = "resume"
	ScenarioCancellation     Scenario = "cancellation"
	ScenarioScopeViolation   Scenario = "scope-violation"
	ScenarioUsageEvents      Scenario = "usage-events"
	// ScenarioNoChange emits run.started + run.completed with no file writes.
	// Used by the minimal-run black-box tests to drive the
	// `completed-no-changes` outcome (KF-01 regression).
	ScenarioNoChange Scenario = "no-change"
	// ScenarioWriteCommit writes RESULT.md and commits it inside the
	// workspace, then emits run.completed. Used by the minimal-run black-box
	// tests to drive the `completed-with-commit` outcome (the happy path).
	ScenarioWriteCommit Scenario = "write-commit"
	// ScenarioWriteNoCommit writes a file but does not commit it. Used by the
	// minimal-run tests to drive `completed-with-uncommitted-changes`.
	// (Alias of ScenarioSuccess; explicit name keeps the test intent clear.)
	ScenarioWriteNoCommit Scenario = "write-no-commit"
)

// AllScenarios is the full, ordered scenario catalogue (spec §33.1 + task list).
var AllScenarios = []Scenario{
	ScenarioSuccess,
	ScenarioQuotaBeforeEdits,
	ScenarioQuotaAfterEdits,
	ScenarioRateLimit,
	ScenarioAuthFailure,
	ScenarioMalformedJSON,
	ScenarioTimeout,
	ScenarioCrash,
	ScenarioPartialOutput,
	ScenarioResume,
	ScenarioCancellation,
	ScenarioScopeViolation,
	ScenarioUsageEvents,
	ScenarioNoChange,
	ScenarioWriteCommit,
	ScenarioWriteNoCommit,
}

// IsValidScenario reports whether s is a known scenario.
func IsValidScenario(s Scenario) bool {
	for _, x := range AllScenarios {
		if x == s {
			return true
		}
	}
	return false
}

// step is one action in a scripted scenario replay. A scenario is an ordered
// list of steps plus a terminal outcome.
type step struct {
	// event is emitted to the sink (in-process) / JSONL/JSON-RPC (executable).
	event *scriptEvent
	// writePath/writeContent, when set, write a file inside the workspace
	// (simulates an edit).
	writePath    string
	writeContent string
	// emitRaw writes an arbitrary line verbatim to the executable stdout (used
	// by malformed-json). Ignored by the in-process adapter.
	emitRaw string
	// hang, if true, blocks forever (until cancelled/killed) — timeout scenario.
	hang bool
	// exitBeforeTerminal exits the executable immediately without a terminal
	// event (crash / partial-output). The exitCode/outcome are used.
	exitBeforeTerminal bool
	// gitAdd, when true, runs `git add -A` inside the workspace (in-process
	// only). Used by the write-commit scenario to produce a real commit.
	gitAdd bool
	// gitCommit, when non-empty, runs `git commit -m <gitCommit>` inside the
	// workspace (in-process only). Used by the write-commit scenario.
	gitCommit string
}

// scriptEvent is a fully-resolved event to emit. Kind selects the payload.
type scriptEvent struct {
	kind    string // "typed" or a literal type override
	failure *fakeFailure
	usage   *fakeUsage
	file    *fakeFileChange
	text    string // for message delta
}

type fakeFailure struct {
	class    string
	reason   string
	exitCode int
}

type fakeUsage struct {
	in, out, cacheRead, cacheWrite int64
	cost                           float64
	confidence                     string
}

type fakeFileChange struct {
	path    string
	action  string
	inScope bool
}

// outcome is the terminal disposition of a scenario.
type outcome struct {
	// exitCode for the executable mode.
	exitCode int
	// stderr written by the executable.
	stderr string
	// class is the failure class for run.failed (empty for success/cancel).
	class string
	// terminal is the terminal event type: run.completed / run.failed /
	// run.cancelled, or "" when exitBeforeTerminal.
	terminal string
	// sessionID returned in run.started (for resume).
	sessionID string
}

// script is a resolved scenario: steps + outcome.
type script struct {
	steps   []step
	outcome outcome
}

// resolveScenario turns a Scenario name + runtime params into a concrete script.
// sessionDir/sessionID support the resume scenario; workspace is used for file
// writes.
func resolveScenario(s Scenario, req runParams) script {
	base := outcome{sessionID: req.sessionID}
	if base.sessionID == "" {
		base.sessionID = "fake-session-1"
	}
	switch s {
	case ScenarioSuccess:
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{event: &scriptEvent{kind: "message.delta", text: "Hello from fake agent"}},
				{writePath: "src/hello.txt", writeContent: "hello\n", event: fileEvent("src/hello.txt", "created", true)},
				{event: usageEvent(120, 80, 0, 0, 0.0001, "PROVIDER_REPORTED")},
			},
			outcome: outcome{terminal: "run.completed", exitCode: 0, sessionID: base.sessionID},
		}
	case ScenarioQuotaBeforeEdits:
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{event: usageEvent(0, 0, 0, 0, 0, "UNKNOWN")},
			},
			outcome: outcome{
				terminal: "run.failed", exitCode: 2,
				class: "PROVIDER_QUOTA", stderr: "error: quota exhausted before any edits\n",
			},
		}
	case ScenarioQuotaAfterEdits:
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{writePath: "src/edit.txt", writeContent: "edit\n", event: fileEvent("src/edit.txt", "modified", true)},
				{event: usageEvent(200, 100, 0, 0, 0.0002, "PROVIDER_REPORTED")},
			},
			outcome: outcome{
				terminal: "run.failed", exitCode: 2,
				class: "PROVIDER_QUOTA", stderr: "error: quota exhausted after edits\n",
			},
		}
	case ScenarioRateLimit:
		return script{
			steps:   []step{{event: &scriptEvent{kind: "run.started"}}},
			outcome: outcome{terminal: "run.failed", exitCode: 2, class: "PROVIDER_RATE_LIMIT", stderr: "HTTP 429 too many requests\n"},
		}
	case ScenarioAuthFailure:
		return script{
			steps:   []step{{event: &scriptEvent{kind: "run.started"}}},
			outcome: outcome{terminal: "run.failed", exitCode: 2, class: "PROVIDER_AUTH", stderr: "401 unauthorized: invalid api key\n"},
		}
	case ScenarioMalformedJSON:
		// Emit a malformed line, then complete normally — tests that malformed
		// output is classified/saved but does NOT break the run.
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{emitRaw: "{not valid json"},
				{event: &scriptEvent{kind: "message.delta", text: "still working"}},
			},
			outcome: outcome{terminal: "run.completed", exitCode: 0, sessionID: base.sessionID},
		}
	case ScenarioTimeout:
		// Hangs forever after run.started; only cancellation/timeout ends it.
		return script{
			steps:   []step{{event: &scriptEvent{kind: "run.started"}, hang: true}},
			outcome: outcome{terminal: "", exitCode: 124, stderr: ""},
		}
	case ScenarioCrash:
		// Emits run.started then run.failed(ENGINE_CRASH). This is consistent
		// across the in-process adapter, the JSONL executable and the JSON-RPC
		// plugin so the conformance suite can verify crash classification on
		// every surface. (Abrupt no-terminal exits are exercised separately by
		// the partial-output scenario + the declarative adapter's synthesis.)
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
			},
			outcome: outcome{terminal: "run.failed", exitCode: 134, class: "ENGINE_CRASH", stderr: "fake agent panicked (simulated crash)\n"},
		}
	case ScenarioPartialOutput:
		// Emits run.started + a delta, then exits mid-stream with no terminal event.
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{event: &scriptEvent{kind: "message.delta", text: "partial..."}, exitBeforeTerminal: true},
			},
			outcome: outcome{exitCode: 1, stderr: ""},
		}
	case ScenarioResume:
		// run.resumed then completes. Used with a sessionID derived from the
		// request; verifies successful resume.
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.resumed"}},
				{event: &scriptEvent{kind: "message.delta", text: "resumed and finishing"}},
				{writePath: "src/resumed.txt", writeContent: "done\n", event: fileEvent("src/resumed.txt", "modified", true)},
			},
			outcome: outcome{terminal: "run.completed", exitCode: 0, sessionID: base.sessionID},
		}
	case ScenarioCancellation:
		// run.started then hang until cancelled; emits run.cancelled on cancel.
		return script{
			steps:   []step{{event: &scriptEvent{kind: "run.started"}, hang: true}},
			outcome: outcome{terminal: "run.cancelled", exitCode: 137, sessionID: base.sessionID},
		}
	case ScenarioScopeViolation:
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{writePath: "OUTSIDE_SCOPE/secret.txt", writeContent: "nope\n", event: fileEvent("OUTSIDE_SCOPE/secret.txt", "created", false)},
			},
			outcome: outcome{terminal: "run.failed", exitCode: 2, class: "SCOPE_VIOLATION", stderr: "scope violation: wrote outside allowed paths\n"},
		}
	case ScenarioUsageEvents:
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{event: usageEvent(100, 50, 0, 0, 0.0001, "PROVIDER_REPORTED")},
				{event: usageEvent(150, 90, 40, 10, 0.0002, "PROVIDER_REPORTED")},
				{event: usageEvent(160, 120, 80, 10, 0.0003, "PROVIDER_REPORTED")},
			},
			outcome: outcome{terminal: "run.completed", exitCode: 0, sessionID: base.sessionID},
		}
	case ScenarioNoChange:
		// run.started → run.completed, no file writes. Drives the
		// `completed-no-changes` outcome (KF-01 regression).
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{event: &scriptEvent{kind: "message.delta", text: "nothing to do"}},
			},
			outcome: outcome{terminal: "run.completed", exitCode: 0, sessionID: base.sessionID},
		}
	case ScenarioWriteCommit:
		// Write RESULT.md, git add + commit it, then run.completed. Drives
		// the `completed-with-commit` outcome (the happy path). The commit
		// runs inside the workspace path; the workspace manager's git runner
		// is allowlisted for `add`/`commit` (git.go).
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{writePath: "RESULT.md", writeContent: "hello\n", event: fileEvent("RESULT.md", "created", true)},
				{event: &scriptEvent{kind: "message.delta", text: "done"}},
				{gitAdd: true},
				{gitCommit: "agent work"},
			},
			outcome: outcome{terminal: "run.completed", exitCode: 0, sessionID: base.sessionID},
		}
	case ScenarioWriteNoCommit:
		// Write a file but do not commit. Drives
		// `completed-with-uncommitted-changes`. Alias of ScenarioSuccess with
		// a clearer name for the minimal-run tests.
		return script{
			steps: []step{
				{event: &scriptEvent{kind: "run.started"}},
				{writePath: "uncommitted.txt", writeContent: "dirty\n", event: fileEvent("uncommitted.txt", "created", true)},
			},
			outcome: outcome{terminal: "run.completed", exitCode: 0, sessionID: base.sessionID},
		}
	default:
		return script{outcome: outcome{terminal: "run.failed", exitCode: 1, class: "INTERNAL_ERROR", stderr: "unknown scenario\n"}}
	}
}

func fileEvent(path, action string, inScope bool) *scriptEvent {
	return &scriptEvent{kind: "file.changed", file: &fakeFileChange{path: path, action: action, inScope: inScope}}
}

func usageEvent(in, out, cr, cw int64, cost float64, conf string) *scriptEvent {
	return &scriptEvent{kind: "usage.updated", usage: &fakeUsage{in: in, out: out, cacheRead: cr, cacheWrite: cw, cost: cost, confidence: conf}}
}

// runParams carries runtime context shared by the adapter and the executable.
type runParams struct {
	workspace string
	engine    string
	model     string
	runID     string
	sessionID string
	// scenario selects the behaviour for the executable (the in-process adapter
	// carries it in AdapterOptions instead).
	scenario Scenario
	// sessionDir holds continuation-pack files for the resume scenario.
	sessionDir string
	// startIsResume selects run.resumed vs run.started for the first event.
	startIsResume bool
}

// hangGrace is how long the timeout/cancellation scenarios wait in the
// in-process adapter before yielding (kept tiny for tests; the executable hangs
// indefinitely and relies on the caller to cancel/kill).
const hangGrace = 50 * time.Millisecond
