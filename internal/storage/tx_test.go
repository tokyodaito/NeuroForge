package storage

import (
	"context"
	"testing"
)

// sampleProject returns a minimal valid project row for tx tests.
func sampleProject(id string) Project {
	return Project{
		ID: id, Name: id, Path: "/tmp/" + id, State: "DISABLED",
		Profile: "LOCAL_REVIEW", CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}
}

// TestTx_CommitPersistsMutationAndAudit is the regression test for the MAJOR-3
// finding ("only migrations use BeginTx; runtime state changes have none"). It
// proves a compound operation — a project mutation plus the audit event that
// records it — commits as one atomic unit: after Commit both rows are visible.
func TestTx_CommitPersistsMutationAndAudit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	auditBefore, err := db.CountAuditEvents(ctx)
	if err != nil {
		t.Fatalf("count audit before: %v", err)
	}

	tx, err := db.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := tx.CreateProject(ctx, sampleProject("alpha")); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := tx.AppendAuditEvent(ctx, AuditEvent{
		Scope: "project", ScopeID: "alpha", Type: "project.added",
		Actor: "user", Payload: "{}",
	}); err != nil {
		t.Fatalf("AppendAuditEvent: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if _, err := db.GetProject(ctx, "alpha"); err != nil {
		t.Fatalf("project not durable after commit: %v", err)
	}
	got, err := db.CountAuditEvents(ctx)
	if err != nil {
		t.Fatalf("count audit after: %v", err)
	}
	if got != auditBefore+1 {
		t.Fatalf("audit count = %d, want %d (audit not committed with mutation)", got, auditBefore+1)
	}
}

// TestTx_RollbackDiscardsMutationAndAudit is the regression test for the
// MAJOR-1/MAJOR-3 atomicity property: a crash (simulated by Rollback) after the
// mutation but before Commit leaves the durable state and audit trail exactly as
// they were — no partial state, no orphan audit row (spec §11.4, ADR-0003).
func TestTx_RollbackDiscardsMutationAndAudit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	auditBefore, err := db.CountAuditEvents(ctx)
	if err != nil {
		t.Fatalf("count audit before: %v", err)
	}

	tx, err := db.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := tx.CreateProject(ctx, sampleProject("beta")); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := tx.AppendAuditEvent(ctx, AuditEvent{
		Scope: "project", ScopeID: "beta", Type: "project.added",
		Actor: "user", Payload: "{}",
	}); err != nil {
		t.Fatalf("AppendAuditEvent: %v", err)
	}
	// Simulate "crash / error after mutation but before commit".
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if _, err := db.GetProject(ctx, "beta"); err == nil {
		t.Fatal("project must not be durable after rollback")
	}
	projects, err := db.CountProjects(ctx)
	if err != nil {
		t.Fatalf("CountProjects: %v", err)
	}
	if projects != 0 {
		t.Fatalf("projects = %d, want 0 after rollback", projects)
	}
	got, err := db.CountAuditEvents(ctx)
	if err != nil {
		t.Fatalf("count audit after: %v", err)
	}
	if got != auditBefore {
		t.Fatalf("audit count = %d, want %d (audit leaked outside transaction)", got, auditBefore)
	}
}

// TestTx_RollbackAfterErrorDiscardsAllWrites proves that when the second write
// of a compound operation fails (here: a duplicate-path UNIQUE violation), the
// whole transaction can be rolled back leaving nothing behind — the durability
// guarantee the registry and backlog now rely on.
func TestTx_RollbackAfterErrorDiscardsAllWrites(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateProject(ctx, sampleProject("dup")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tx, err := db.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	// First write in the tx would succeed on its own...
	if err := tx.CreateProject(ctx, sampleProject("transient")); err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}
	// ...but a second write collides on the UNIQUE(path) constraint -> error.
	if err := tx.CreateProject(ctx, sampleProject("dup")); err == nil {
		t.Fatal("expected UNIQUE violation on duplicate path, got nil")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Only the seeded row must remain.
	if _, err := db.GetProject(ctx, "transient"); err == nil {
		t.Fatal("transient project must not survive rollback of failed tx")
	}
	count, err := db.CountProjects(ctx)
	if err != nil {
		t.Fatalf("CountProjects: %v", err)
	}
	if count != 1 {
		t.Fatalf("projects = %d, want 1 (only the seed)", count)
	}
}
