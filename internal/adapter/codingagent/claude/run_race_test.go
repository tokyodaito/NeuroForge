package claude

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// TestRunConcurrentStartCancelCompletion is a regression test for the data race
// on Adapter.runs that was caused by lazy-initialising the shared active-runs
// registry outside a.mu in startRun (run.go:225-226).
//
// It drives three concurrent operations against a single shared adapter — the
// original race surface — so the -race detector reliably catches regressions:
//
//   - many parallel Start calls insert into the shared registry at once,
//   - a subset of runs is cancelled mid-flight, exercising Cancel's registry
//     read + runState teardown concurrently with sibling Starts,
//   - the remaining runs reach a terminal event via natural stream completion.
//
// A mixed spawner dispatches even-indexed spawns to the success fixture
// (natural completion) and odd-indexed spawns to the hanging cancellation
// fixture, so both terminal paths are exercised on one adapter. No real Claude
// CLI is invoked (rule §36.5: no real paid calls).
func TestRunConcurrentStartCancelCompletion(t *testing.T) {
	successSpec := fixtureForScenario(fake.ScenarioSuccess)
	hangSpec := fixtureForScenario(fake.ScenarioCancellation)
	var spawnIdx atomic.Int32
	mixedSpawn := spawner(func(argv []string, dir string, env []string, stdin io.Reader, stderr io.Writer) (process, error) {
		if spawnIdx.Add(1)%2 == 0 {
			return replaySpawner(successSpec)(argv, dir, env, stdin, stderr)
		}
		return replaySpawner(hangSpec)(argv, dir, env, stdin, stderr)
	})
	a, err := New(Options{
		BinaryPath:   "claude",
		Spawn:        mixedSpawn,
		ArtifactsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const runs = 16
	var wg sync.WaitGroup
	wg.Add(runs)
	for i := 0; i < runs; i++ {
		go func(i int) {
			defer wg.Done()
			sink := &codingagent.SliceSink{}
			handle, err := a.Start(context.Background(), protocol.AgentRunRequest{
				RunID:     fmt.Sprintf("race-%d", i),
				Engine:    a.ID(),
				Model:     "sonnet",
				Workspace: tempDir(),
			}, sink)
			if err != nil {
				t.Errorf("Start %d: %v", i, err)
				return
			}
			// Allow a brief window for natural completion; if the run is still
			// live (hanging fixture), cancel it. Either way it must terminate.
			term := make(chan struct{})
			go func() {
				_ = waitTerminal(sink, 3*time.Second)
				close(term)
			}()
			select {
			case <-term:
			case <-time.After(120 * time.Millisecond):
				if err := a.Cancel(context.Background(), handle); err != nil {
					t.Errorf("Cancel %d: %v", i, err)
				}
				<-term
			}
			evs := sink.Events()
			if len(evs) == 0 || !evs[len(evs)-1].Type.IsTerminal() {
				t.Errorf("run %d did not terminate: %v", i, typesOf(evs))
			}
		}(i)
	}
	wg.Wait()

	// After every run finishes, supervise's defer must have untracked each one;
	// the shared registry must drain to empty. Poll briefly since the defer
	// runs asynchronously relative to the terminal event.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		n := len(a.runs)
		a.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.runs) != 0 {
		t.Errorf("active-runs registry not drained: %d entries remain", len(a.runs))
	}
}
