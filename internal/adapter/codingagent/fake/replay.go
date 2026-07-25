package fake

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// emitter abstracts how replayed steps reach the outside world: the in-process
// [Adapter] routes typed events to its [EventSink]; the executable routes them
// to JSONL (command mode) or JSON-RPC notifications (plugin mode).
type emitter interface {
	// emit sends one typed normalized event.
	emit(ctx context.Context, ev protocol.NormalizedEvent) error
	// emitRaw sends an opaque line verbatim (used by the malformed-json
	// scenario to simulate malformed agent output).
	emitRaw(ctx context.Context, line string) error
	// write writes a file inside the workspace (simulates an agent edit).
	write(ctx context.Context, path, content string) error
	// gitAddAll runs `git add -A` inside the workspace (in-process only). Used
	// by the write-commit scenario to produce a real commit. Implementations
	// that have no workspace (e.g. tests without a worktree) return nil.
	gitAddAll(ctx context.Context) error
	// gitCommit runs `git commit -m <msg>` inside the workspace (in-process
	// only). Used by the write-commit scenario.
	gitCommit(ctx context.Context, msg string) error
}

// ErrReplayCancelled is returned by [replay] when the run was cancelled mid-play.
var ErrReplayCancelled = errors.New("fake: replay cancelled")

// emitTerminalOnCancel emits a run.cancelled terminal event on cancellation and
// always returns ErrReplayCancelled. Cancellation is itself the terminal signal
// for any scenario (including the timeout scenario, which has no natural
// terminal): the supervisor learns the run ended via run.cancelled.
func emitTerminalOnCancel(sc script, p runParams, em emitter) error {
	cancelled := sc.outcome
	cancelled.terminal = "run.cancelled"
	_ = em.emit(context.Background(), buildTerminal(cancelled, p, time.Now()))
	return ErrReplayCancelled
}

// replay walks a resolved scenario's steps, driving the emitter. It honours
// hang steps (blocks until ctx is done) and, for cancellation scenarios, emits
// the terminal run.cancelled event. It returns the resolved [outcome] so the
// caller (adapter/executable) can set its exit code/stderr consistently.
func replay(ctx context.Context, sc script, p runParams, em emitter) (outcome, error) {
	now := time.Now()
	for _, st := range sc.steps {
		// Apply the file-write side effect first.
		if st.writePath != "" {
			if err := em.write(ctx, st.writePath, st.writeContent); err != nil {
				return sc.outcome, err
			}
		}
		if st.emitRaw != "" {
			if err := em.emitRaw(ctx, st.emitRaw); err != nil {
				return sc.outcome, err
			}
		}
		if st.event != nil {
			ev := buildEvent(*st.event, p, now)
			if err := em.emit(ctx, ev); err != nil {
				return sc.outcome, err
			}
		}
		if st.exitBeforeTerminal {
			return sc.outcome, nil
		}
		// In-process git side effects (write-commit scenario). The executable
		// ignores these; the in-process adapter performs them against the
		// worktree.
		if st.gitAdd {
			if err := em.gitAddAll(ctx); err != nil {
				return sc.outcome, err
			}
		}
		if st.gitCommit != "" {
			if err := em.gitCommit(ctx, st.gitCommit); err != nil {
				return sc.outcome, err
			}
		}
		if st.hang {
			// Block until cancelled. The cancellation scenario then emits its
			// terminal run.cancelled event; the timeout scenario (terminal="")
			// emits nothing and the caller surfaces a timeout.
			select {
			case <-time.After(hangGrace):
				// Tiny grace keeps the in-process adapter responsive; the
				// executable ignores hangGrace and blocks on the caller.
			case <-ctx.Done():
				return sc.outcome, emitTerminalOnCancel(sc, p, em)
			}
			// Continue hanging in a loop until ctx is done (timeout scenario
			// never terminates on its own).
			<-ctx.Done()
			return sc.outcome, emitTerminalOnCancel(sc, p, em)
		}
	}

	// Emit the terminal event if the scenario defines one.
	if sc.outcome.terminal != "" {
		ev := buildTerminal(sc.outcome, p, now)
		if err := em.emit(ctx, ev); err != nil {
			return sc.outcome, err
		}
	}
	return sc.outcome, nil
}

// buildEvent converts a script step into a normalized event with full metadata.
func buildEvent(se scriptEvent, p runParams, ts time.Time) protocol.NormalizedEvent {
	ev := protocol.NormalizedEvent{
		Type:      protocol.EventType(se.kind),
		Timestamp: ts,
		RunID:     p.runID,
		Engine:    p.engine,
		Model:     p.model,
	}
	switch se.kind {
	case "run.started", "run.resumed":
		// top-level metadata only
	case "message.delta":
		ev.Message = &protocol.MessagePayload{Delta: se.text}
	case "file.changed":
		if se.file != nil {
			ev.FileChange = &protocol.FileChangePayload{
				Path: se.file.path, Action: se.file.action, InScope: se.file.inScope,
			}
		}
	case "usage.updated":
		if se.usage != nil {
			ev.Usage = &protocol.UsagePayload{
				InputTokens: se.usage.in, OutputTokens: se.usage.out,
				CacheReadTokens: se.usage.cacheRead, CacheWriteTokens: se.usage.cacheWrite,
				Cost: se.usage.cost, Currency: "USD", Confidence: protocol.QuotaConfidence(se.usage.confidence),
			}
		}
	}
	return ev
}

// buildTerminal builds the run.completed / run.failed / run.cancelled event.
func buildTerminal(o outcome, p runParams, ts time.Time) protocol.NormalizedEvent {
	ev := protocol.NormalizedEvent{
		Type:      protocol.EventType(o.terminal),
		Timestamp: ts,
		RunID:     p.runID,
		Engine:    p.engine,
		Model:     p.model,
	}
	if o.terminal == "run.failed" && o.class != "" {
		ev.Failure = &protocol.FailurePayload{
			Class:    protocol.FailureClass(o.class),
			Reason:   reasonFor(o),
			ExitCode: o.exitCode,
		}
	}
	return ev
}

func reasonFor(o outcome) string {
	if o.stderr != "" {
		return firstLine(o.stderr)
	}
	return string(o.class)
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// fileWrite performs a workspace-relative file write for the file.changed side
// effects. It is used by both the in-process adapter and the executable.
func fileWrite(workspace, rel, content string) error {
	abs := filepath.Join(workspace, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// gitInWorkspace runs git with the given args inside the workspace. It is the
// in-process counterpart of the executable's git-commit step (used by the
// write-commit scenario so the fake adapter can produce a real commit). All
// subcommands are local git operations — no network (AC-7).
func gitInWorkspace(workspace string, args ...string) error {
	full := append([]string{"-C", workspace}, args...)
	cmd := exec.CommandContext(context.Background(), "git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New("fake: git " + strings.Join(args, " ") + ": " + err.Error() + ": " + string(out))
	}
	return nil
}
