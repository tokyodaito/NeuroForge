package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

func quietLogger2() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newReconcileFixture opens a migrated DB + audit recorder in a temp home.
func newReconcileFixture(t *testing.T) (Dirs, *storage.DB, *audit.Recorder) {
	t.Helper()
	dirs := WithRoot(t.TempDir())
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(context.Background(), dirs.StateDB, &storage.Options{Logger: quietLogger2()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return dirs, db, audit.NewRecorder(db, quietLogger2())
}

func txFor(dirs Dirs, db *storage.DB, rec *audit.Recorder) ReconcileTx {
	return ReconcileTx{DB: db, Audit: rec, Dirs: dirs, Logger: quietLogger2()}
}

func TestReconcile_CleanStart_NoOpAndIdempotent(t *testing.T) {
	dirs, db, rec := newReconcileFixture(t)
	tx := txFor(dirs, db, rec)

	dec, err := Reconcile(context.Background(), tx, DefaultReconcilers())
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if len(dec) < 2 {
		t.Fatalf("expected >=2 decisions, got %d", len(dec))
	}
	// Runtime-files should be no-op on a clean dir.
	var rt ReconcileDecision
	for _, d := range dec {
		if d.Reconciler == "runtime-files" {
			rt = d
		}
	}
	if rt.Action != DecisionNoOp {
		t.Errorf("clean start runtime-files action = %s, want no-op", rt.Action)
	}

	// Re-running must be idempotent.
	dec2, err := Reconcile(context.Background(), tx, DefaultReconcilers())
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(dec2) != len(dec) {
		t.Fatalf("not idempotent: %d vs %d decisions", len(dec2), len(dec))
	}
}

func TestReconcile_StalePID_Reclaimed(t *testing.T) {
	dirs, db, rec := newReconcileFixture(t)
	// Write a pid guaranteed not to be alive.
	mustWritePID(t, dirs.PIDFile, 1<<30)
	// Also drop token/addr to simulate a crashed daemon's leftovers.
	mustWriteFile(t, dirs.TokenFile, "x")
	mustWriteFile(t, dirs.AddrFile, "http://127.0.0.1:1")

	tx := txFor(dirs, db, rec)
	dec, err := Reconcile(context.Background(), tx, DefaultReconcilers())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	found := false
	for _, d := range dec {
		if d.Reconciler == "runtime-files" && d.Action == DecisionReclaimed {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reclaimed decision, got %+v", dec)
	}
	// Stale files must be gone.
	for _, p := range []string{dirs.PIDFile, dirs.TokenFile, dirs.AddrFile} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("stale file %s should be removed", p)
		}
	}
}

func TestReconcile_CorruptedPID_RepairedWithoutDataLoss(t *testing.T) {
	dirs, db, rec := newReconcileFixture(t)
	// Seed durable data so we can assert it survives.
	_, _ = rec.Record(context.Background(), audit.Event{Type: "seed"})
	mustWriteFile(t, dirs.PIDFile, "not-a-number\n")

	tx := txFor(dirs, db, rec)
	dec, err := Reconcile(context.Background(), tx, DefaultReconcilers())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var repaired bool
	for _, d := range dec {
		if d.Reconciler == "runtime-files" && d.Action == DecisionRepaired {
			repaired = true
		}
	}
	if !repaired {
		t.Fatalf("expected repaired decision, got %+v", dec)
	}
	// Corrupted pid file removed.
	if _, err := os.Stat(dirs.PIDFile); !os.IsNotExist(err) {
		t.Error("corrupted pid file should be removed")
	}
	// Durable data intact (no silent data loss).
	n, err := db.CountAuditEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("audit data lost: %d rows", n)
	}
}

func TestReconcile_LivePID_AbortsAsConflict(t *testing.T) {
	dirs, db, rec := newReconcileFixture(t)
	// The test process itself is alive — writing our own pid simulates a live
	// daemon owning this home.
	mustWritePID(t, dirs.PIDFile, os.Getpid())

	tx := txFor(dirs, db, rec)
	dec, err := Reconcile(context.Background(), tx, DefaultReconcilers())
	if !errors.Is(err, ErrConcurrentDaemon) {
		t.Fatalf("err = %v, want ErrConcurrentDaemon", err)
	}
	var conflict bool
	for _, d := range dec {
		if d.Action == DecisionConflict {
			conflict = true
		}
	}
	if !conflict {
		t.Fatalf("expected a conflict decision, got %+v", dec)
	}
	// The live owner's files must NOT have been removed.
	if _, err := os.Stat(dirs.PIDFile); err != nil {
		t.Errorf("live owner pid file must not be touched: %v", err)
	}
}

func TestReconcile_DecisionsAreAudited(t *testing.T) {
	dirs, db, rec := newReconcileFixture(t)
	tx := txFor(dirs, db, rec)

	if _, err := Reconcile(context.Background(), tx, DefaultReconcilers()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListAuditEvents(context.Background(), storage.AuditFilter{Type: "reconcile.decision"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Errorf("expected >=2 reconcile.decision audit rows, got %d", len(rows))
	}
	complete, err := db.ListAuditEvents(context.Background(), storage.AuditFilter{Type: "reconcile.complete"})
	if err != nil {
		t.Fatal(err)
	}
	if len(complete) != 1 {
		t.Errorf("expected 1 reconcile.complete row, got %d", len(complete))
	}
}

// TestReconcile_ExtensionPoint verifies a caller-supplied reconciler runs and is
// audited — this is the M2/M3 hook for agent-attempt/work-package recovery.
func TestReconcile_ExtensionPoint(t *testing.T) {
	dirs, db, rec := newReconcileFixture(t)
	tx := txFor(dirs, db, rec)

	custom := stubReconciler{name: "future-attempts", out: []ReconcileDecision{{
		Reconciler: "future-attempts", Entity: "attempt", Action: DecisionMarkedStale,
		Detail: "placeholder — real attempt recovery arrives in M2/M3",
	}}}

	dec, err := Reconcile(context.Background(), tx, []Reconciler{&custom})
	if err != nil {
		t.Fatal(err)
	}
	if !custom.called {
		t.Fatal("custom reconciler was not invoked")
	}
	if len(dec) != 1 || dec[0].Entity != "attempt" {
		t.Fatalf("expected the custom decision, got %+v", dec)
	}
	rows, _ := db.ListAuditEvents(context.Background(), storage.AuditFilter{Type: "reconcile.decision"})
	if len(rows) != 1 {
		t.Errorf("custom decision should be audited once, got %d", len(rows))
	}
}

// TestRun_AbortsWhenConcurrentDaemonOwnsHome exercises the Run-level guard:
// starting a second Run in an already-owned home fails fast instead of
// producing a duplicate daemon.
func TestRun_AbortsWhenConcurrentDaemonOwnsHome(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	// Pretend a live daemon owns this home (test process is alive).
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	mustWritePID(t, dirs.PIDFile, os.Getpid())

	err := Run(context.Background(), RunConfig{Dirs: dirs, Logger: quietLogger2()})
	if !errors.Is(err, ErrConcurrentDaemon) {
		t.Fatalf("Run err = %v, want ErrConcurrentDaemon", err)
	}
}

type stubReconciler struct {
	name   string
	out    []ReconcileDecision
	called bool
}

func (s *stubReconciler) Name() string { return s.name }
func (s *stubReconciler) Reconcile(ctx context.Context, tx ReconcileTx) ([]ReconcileDecision, error) {
	s.called = true
	return s.out, nil
}

func mustWritePID(t *testing.T, path string, pid int) {
	t.Helper()
	mustWriteFile(t, path, strconv.Itoa(pid)+"\n")
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
