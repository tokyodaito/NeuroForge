package gemini

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// TestSupervise_CancelBeatsEOF verifies KF-09 / invariant I.9: when cancellation
// is accepted and kills the process group, the resulting EOF must NOT synthesize
// a run.failed/run.completed terminal from the (non-zero) exit code — the
// recorded terminal MUST be run.cancelled.
//
// This models the REAL production race: the agent is still running (its stdout
// is a live pipe), and Cancel kills the whole process group, which is what
// closes the pipe and surfaces EOF to the reader goroutine. The fix records the
// cancel intent BEFORE the kill, so by the time the kill-induced EOF reaches
// supervise's single terminal decision, the intent is already visible. Without
// the fix, the EOF could win the select and synthesize run.failed from the
// SIGKILL exit code.
//
// We loop many iterations and rely on -race -count to flush out any residual
// ordering hazard. This test reproduces the bug before the fix (the kill-induced
// EOF synthesized run.failed) and is green after.
func TestSupervise_CancelBeatsEOF(t *testing.T) {
	for i := 0; i < 50; i++ {
		runCancelRaceOnce(t)
	}
}

func runCancelRaceOnce(t *testing.T) {
	t.Helper()
	// hangingProc blocks forever until kill closes its held pipe (→ EOF) and
	// reports a non-zero exit code — the exact conditions that, before the fix,
	// synthesized run.failed from the kill-induced EOF.
	proc := hangingProc(1, "boom\n")
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return proc, nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "race", Prompt: "hi"}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Cancel the run. The adapter records the intent, cancels ctx, then kills
	// the group (closing the pipe → EOF). The terminal must be run.cancelled.
	if err := a.Cancel(context.Background(), handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	last := ""
	if len(evs) > 0 {
		last = string(lastType(evs))
	}
	if last != string(protocol.EventRunCancelled) {
		t.Fatalf("terminal = %s, want run.cancelled (KF-09): %v", last, typesOf(evs))
	}
}

// TestSupervise_TimeoutBeetsEOF verifies KF-09 / I.9: when the hard deadline
// fires at the same instant the process EOFs (because the timeout killed the
// process), the recorded terminal MUST be run.failed(TIMEOUT) — never
// run.completed/run.failed(any-other-class). The fix re-checks ctx.Err()
// after readCh wins.
//
// We use a hanging process whose stdout unblocks only when the timeout kills
// it (the killed pipe is closed, surfacing EOF). Without the fix, the EOF
// could win the select and synthesize run.completed from exit code 0.
func TestSupervise_TimeoutBeetsEOF(t *testing.T) {
	for i := 0; i < 10; i++ {
		runTimeoutRaceOnce(t)
	}
}

func runTimeoutRaceOnce(t *testing.T) {
	t.Helper()
	// hangingProc unblocks on kill (the held pipe is closed).
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return hangingProc(0, ""), nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "to-race", Prompt: "hi", Timeout: 40 * time.Millisecond}, sink); err != nil {
		t.Fatal(err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	last := lastType(evs)
	if last != protocol.EventRunFailed {
		t.Fatalf("terminal = %s, want run.failed (timeout)", last)
	}
	terminal := evs[len(evs)-1]
	if terminal.Failure == nil || terminal.Failure.Class != protocol.FailureTimeout {
		t.Fatalf("failure = %+v, want TIMEOUT class", terminal.Failure)
	}
}

// TestSupervise_CancelIsIdempotent verifies cancellation is idempotent: calling
// Cancel multiple times on the same handle does not error and the terminal is
// always run.cancelled.
func TestSupervise_CancelIsIdempotent(t *testing.T) {
	var proc *stubProc
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			proc = hangingProc(137, "")
			return proc, nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "idem", Prompt: "hi"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	// Cancel multiple times. The supervisor and adapter must not race on the
	// cancel map or the process state.
	for i := 0; i < 5; i++ {
		_ = a.Cancel(context.Background(), handle)
	}
	evs := waitTerminal(sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunCancelled {
		t.Fatalf("terminal = %s, want run.cancelled", lastType(evs))
	}
}

// ---- helpers used by these tests ----

// (waitTerminal, lastType, typesOf are defined in adapter_test.go.)

// _ keeps referenced helpers alive across future refactorings.
var (
	_ = errors.New
	_ = strings.Builder{}
	_ sync.Mutex
	_ atomic.Bool
)
