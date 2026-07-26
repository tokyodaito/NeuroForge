package cli

import (
	"context"
	"testing"
	"time"

	"neuroforge/internal/daemon"
)

// TestForgeRun_RestartPreservesTerminal verifies BF-03 / invariant I.8 /
// STATE_MACHINE.md §5.2: after a run reaches a terminal state and the daemon is
// restarted (running the startup reconciler), the terminal workspace is left
// alone — the reconciler must NOT revive it to active. It also confirms a
// subsequent run works after the restart.
func TestForgeRun_RestartPreservesTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("restart E2E spawns real daemon processes")
	}
	cases := []struct {
		name   string
		model  string // fake scenario
		wantWS string // expected terminal workspace state
	}{
		{"completed-with-commit", "fake/write-commit", "completed"},
		{"no-changes-failed", "fake/no-change", "failed"},
		{"adapter-failed", "fake/crash", "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunFixture(t)
			dirs := daemon.WithRoot(f.home)

			// 1. Drive the run to a terminal state.
			f.run("--engine", "fake", "--model", tc.model, "do task")

			// 2. Capture the terminal workspace state before restart.
			pid := firstProjectID(t, f.home)
			wsState := latestWorkspaceState(t, f.home, pid)
			if wsState == "" {
				t.Fatalf("no workspace recorded after run")
			}
			if wsState != tc.wantWS {
				t.Fatalf("pre-restart workspace state = %s, want %s", wsState, tc.wantWS)
			}

			// 3. Restart the daemon: stop, then a fresh run triggers autostart,
			//    which runs the startup reconciler over the existing terminal
			//    workspace.
			runForge(f.t, f.bin, f.home, "daemon", "stop")

			// 4. A new run after restart must succeed (new task) and must NOT
			//    revive the prior terminal workspace.
			f.run("--engine", "fake", "--model", "fake/no-change", "after restart")

			// 5. No workspace may be left non-terminal (active/pending): the
			//    reconciler must not revive the prior run, and the new run must
			//    itself have reached a terminal state.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cli, err := daemon.Connect(ctx, dirs)
			if err != nil {
				t.Fatalf("connect after restart: %v", err)
			}
			wss, err := cli.ListWorkspaces(ctx, "", pid)
			if err != nil {
				t.Fatalf("list workspaces: %v", err)
			}
			for _, w := range wss {
				if w.State == "active" || w.State == "pending" {
					t.Errorf("workspace %s is non-terminal (%s) after restart — reconciler revived it or it never settled (BF-03/I.8)",
						w.ID, w.State)
				}
			}
		})
	}
}

// firstProjectID returns the id of the (single) project registered for the
// fixture repo, via the loopback API.
func firstProjectID(t *testing.T, home string) string {
	t.Helper()
	dirs := daemon.WithRoot(home)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli, err := daemon.Connect(ctx, dirs)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ps, err := cli.ListProjects(ctx)
	if err != nil || len(ps) == 0 {
		t.Fatalf("no projects: %v", err)
	}
	return ps[0].ID
}

// latestWorkspaceState returns the state of the most-recent workspace for the
// project, via the loopback API.
func latestWorkspaceState(t *testing.T, home, projectID string) string {
	t.Helper()
	dirs := daemon.WithRoot(home)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli, err := daemon.Connect(ctx, dirs)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	wss, err := cli.ListWorkspaces(ctx, "", projectID)
	if err != nil || len(wss) == 0 {
		return ""
	}
	return wss[len(wss)-1].State
}
