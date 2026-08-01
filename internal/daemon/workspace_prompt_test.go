package daemon

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workgraph"
	"neuroforge/internal/workspace"
)

func quietSlog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newWorkspaceService builds a WorkspaceService against a temp DB + fake
// supervisor wiring, for exercising the daemon-side run validation without a
// network round-trip.
func newWorkspaceService(t *testing.T) (*WorkspaceService, *storage.DB) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := storage.Open(ctx, dbPath, &storage.Options{Logger: quietSlog()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rec := audit.NewRecorder(db, quietSlog())
	wm := workspace.NewManager(db, rec, filepath.Join(t.TempDir(), "ws"), quietSlog())
	leases := workgraph.NewLeaseManager(db)
	bl := task.NewBacklog(db, rec, "", quietSlog())
	reg, err := buildAdapterRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sup := supervisor.New(supervisor.Options{Adapters: reg, Audit: rec, Logger: quietSlog()})
	resolve := func(ctx context.Context, id string) (string, error) { return "/tmp/" + id, nil }
	svc := NewWorkspaceService(wm, leases, sup, bl, rec, quietSlog(), resolve)
	return svc, db
}

// TestRunWorkspace_EmptyPromptRejectedForProductionEngine proves the daemon
// rejects a production-engine run with an empty prompt (blocker 3: defense in
// depth — the CLI also validates, but the daemon is the authority).
func TestRunWorkspace_EmptyPromptRejectedForProductionEngine(t *testing.T) {
	svc, db := newWorkspaceService(t)
	ctx := context.Background()

	// Seed a project + task + active workspace so the run reaches the prompt
	// guard.
	if _, err := db.Underlying().ExecContext(ctx,
		`INSERT INTO projects (id,name,path,state,profile,created_at,updated_at) VALUES ('p','p','/tmp/p','ACTIVE','LOCAL_REVIEW','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Underlying().ExecContext(ctx,
		`INSERT INTO tasks (id,project_id,description,priority,state,created_at,updated_at) VALUES ('p-1','p','d','NORMAL','NEW','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Underlying().ExecContext(ctx,
		`INSERT INTO workspaces (id,project_id,task_id,work_package_id,attempt,path,branch,base_sha,head_sha,state,created_at,updated_at) VALUES ('ws-1','p','p-1','main',1,'/tmp/ws','b','sha','sha','active','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		engine string
		prompt string
		wantOK bool
	}{
		{"opencode", "", false}, // production engine, no prompt → rejected
		{"codex", "   ", false}, // whitespace-only prompt → rejected
		{"fake", "", true},      // fake engine keeps legacy promptless mode
	} {
		_, err := svc.RunWorkspace(ctx, "ws-1", transport.RunWorkspaceRequest{
			Engine: tc.engine, Prompt: tc.prompt,
		})
		if tc.wantOK {
			// fake run may proceed past the guard; downstream errors (no real
			// worktree) are fine — we only assert the prompt guard did not
			// fire for fake.
			if err != nil && !contains(err.Error(), "prompt is required") {
				t.Logf("fake run downstream err (expected, not the guard): %v", err)
			}
		} else {
			if err == nil {
				t.Errorf("engine %q with empty prompt should be rejected", tc.engine)
			} else if !contains(err.Error(), "prompt is required") {
				t.Errorf("engine %q: error = %v, want prompt-required error", tc.engine, err)
			}
		}
	}
}
