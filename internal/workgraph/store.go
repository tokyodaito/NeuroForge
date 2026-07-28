// This file implements the durable Work Graph store (spec §18.3, §31,
// milestone M14-05). It wraps [storage.DB]'s work-package substrate and
// enforces the "only a ValidatedWorkGraph may be persisted" invariant
// (M14-04 AC2 carried forward): the Save and SaveIfChanged entry points
// accept *ValidatedWorkGraph, never a raw WorkGraph, so an invalid DAG cannot
// become durable runnable state.
//
// The store owns JSON encoding of the list-shaped fields (accepted_ac_ids,
// allowed_scope, dependencies, attempts); the schema treats them as opaque
// JSON columns. Reads reconstruct the graph in canonical (sorted-by-ID) order
// so the result of Save → Load is byte-stable for an identical input.
//
// All mutations go through [storage.DB.BeginTx] so the store can compose
// atomically with audit events and lease mutations in the same transaction.

package workgraph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// ErrWorkGraphNotFound is returned by Load / Get when a task has no persisted
// work packages. It wraps the storage-level sentinel so callers can use
// errors.Is across both layers.
var ErrWorkGraphNotFound = storage.ErrWorkGraphNotFound

// ErrWorkPackageNotFound wraps the storage-level not-found sentinel.
var ErrWorkPackageNotFound = storage.ErrWorkPackageNotFound

// WorkGraphStore is the domain service that persists a ValidatedWorkGraph and
// surfaces package-level reads/mutations. Every mutation is recorded to the
// audit trail atomically with the storage change (spec §11.4, §29.4).
type WorkGraphStore struct {
	db     *storage.DB
	audit  *audit.Recorder
	logger *slog.Logger
	now    func() time.Time
}

// NewWorkGraphStore creates a store backed by db. The recorder may be nil
// (audit disabled); the logger may be nil (a quiet default is used).
func NewWorkGraphStore(db *storage.DB, rec *audit.Recorder, logger *slog.Logger) *WorkGraphStore {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return &WorkGraphStore{
		db:     db,
		audit:  rec,
		logger: logger,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// discardWriter is a no-op io.Writer used to silence the default logger when
// the caller does not supply one (mirrors task.SpecificationStore's pattern).
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Save persists a ValidatedWorkGraph for v.TaskID(). Idempotent: re-saving
// the same graph replaces every package row, preserves created_at, increments
// graph_version, and prunes packages that no longer exist. Attempts are
// preserved across re-saves (the caller's domain intent: a graph
// re-decomposition must NOT erase execution history).
//
// Returns the re-read WorkGraph (with refreshed graph_version / timestamps)
// so the caller observes the durable state.
func (s *WorkGraphStore) Save(ctx context.Context, v *ValidatedWorkGraph) (WorkGraph, error) {
	if v == nil {
		return WorkGraph{}, fmt.Errorf("workgraph: save: nil validated graph")
	}
	graph := v.Graph()
	if graph.TaskID == "" {
		return WorkGraph{}, fmt.Errorf("workgraph: save: task_id is required")
	}

	// Preserve runtime state across re-saves: load the existing state for any
	// package that already exists, and use it in place of the in-memory
	// package's state when the caller's input carries a "fresh" marker
	// (PackagePending). A re-decomposed graph (Decompose sets every package to
	// PackagePending) thus keeps its persisted execution state, while a caller
	// that explicitly transitions a package to running/succeeded/etc. before
	// Save still wins. Attempts are preserved unconditionally (execution
	// history must survive a graph re-decomposition).
	existingState, existingAttempts, err := s.loadRuntimeStateByTask(ctx, graph.TaskID)
	if err != nil {
		return WorkGraph{}, fmt.Errorf("workgraph: save: load runtime state: %w", err)
	}
	for i := range graph.Packages {
		p := &graph.Packages[i]
		if atts, ok := existingAttempts[p.ID]; ok && len(atts) > 0 {
			if len(p.Attempts) == 0 {
				p.Attempts = atts
			}
		}
		if st, ok := existingState[p.ID]; ok && p.State == PackagePending {
			// Preserve the persisted runtime state for an already-existing
			// package whose in-memory state is the Decompose default. A
			// caller that explicitly passes a non-pending state (e.g.
			// running) still wins.
			p.State = st
		}
	}

	nowTS := s.now().Format(time.RFC3339Nano)
	rows := make([]storage.WorkPackageRow, 0, len(graph.Packages))
	for _, p := range graph.Packages {
		row, err := packageToRow(p, nowTS)
		if err != nil {
			return WorkGraph{}, err
		}
		rows = append(rows, row)
	}

	tx, err := s.db.BeginTx(ctx)
	if err != nil {
		return WorkGraph{}, fmt.Errorf("workgraph: save: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.ReplaceWorkGraph(ctx, graph.TaskID, rows); err != nil {
		return WorkGraph{}, err
	}

	if s.audit != nil {
		if _, err := s.audit.RecordTx(ctx, tx, audit.Event{
			Type:    "workgraph.saved",
			Scope:   audit.ScopeTask,
			ScopeID: graph.TaskID,
			Actor:   audit.ActorUser,
			Payload: audit.Payload(
				"packages", len(rows),
				"task_id", graph.TaskID,
			),
		}); err != nil {
			return WorkGraph{}, fmt.Errorf("workgraph: save: audit: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return WorkGraph{}, fmt.Errorf("workgraph: save: commit: %w", err)
	}

	s.logger.Info("work graph saved",
		"task", graph.TaskID, "packages", len(rows))
	return s.LoadOrDie(ctx, graph.TaskID)
}

// LoadOrDie returns the persisted WorkGraph for taskID, or
// ErrWorkGraphNotFound when no packages exist. The returned graph is NOT
// validated; callers that need a runnable handle must call ValidateWorkGraph.
func (s *WorkGraphStore) LoadOrDie(ctx context.Context, taskID string) (WorkGraph, error) {
	rows, err := s.db.ListWorkPackages(ctx, taskID)
	if err != nil {
		return WorkGraph{}, err
	}
	packages := make([]WorkPackage, 0, len(rows))
	for _, r := range rows {
		p, err := rowToPackage(r)
		if err != nil {
			return WorkGraph{}, fmt.Errorf("workgraph: load package %q: %w", r.PackageID, err)
		}
		packages = append(packages, p)
	}
	return WorkGraph{TaskID: taskID, Packages: packages}, nil
}

// LoadValidated loads the persisted graph and re-validates it. Returns
// ErrWorkGraphNotFound when no packages exist; ErrInvalidWorkGraph when the
// persisted state fails re-validation (which would indicate a storage or
// validation bug — the store only accepts ValidatedWorkGraph so this should
// be unreachable absent a schema/domain drift).
func (s *WorkGraphStore) LoadValidated(ctx context.Context, taskID string) (*ValidatedWorkGraph, error) {
	g, err := s.LoadOrDie(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return ValidateWorkGraph(g)
}

// HasGraph reports whether taskID has any persisted work packages.
func (s *WorkGraphStore) HasGraph(ctx context.Context, taskID string) (bool, error) {
	_, err := s.db.ListWorkPackages(ctx, taskID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrWorkGraphNotFound) {
		return false, nil
	}
	return false, err
}

// GetPackage returns one package by ID.
func (s *WorkGraphStore) GetPackage(ctx context.Context, taskID, packageID string) (WorkPackage, error) {
	r, err := s.db.GetWorkPackage(ctx, taskID, packageID)
	if err != nil {
		return WorkPackage{}, err
	}
	return rowToPackage(r)
}

// TransitionPackage atomically updates a package's lifecycle state and
// records the audit event in the same transaction. Returns
// ErrWorkPackageNotFound when the package row does not exist.
//
// Validity of the transition itself is NOT enforced here (e.g. pending →
// succeeded is allowed); the scheduler / supervisor that owns the lifecycle
// state machine is responsible for transition legality. This keeps the store
// a thin persistence layer and avoids duplicating the state-machine rules in
// the data layer.
func (s *WorkGraphStore) TransitionPackage(ctx context.Context, taskID, packageID string, newState PackageState) error {
	if !newState.IsValid() {
		return fmt.Errorf("workgraph: invalid package state %q", newState)
	}
	nowTS := s.now().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("workgraph: transition: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.UpdateWorkPackageState(ctx, taskID, packageID, string(newState), nowTS); err != nil {
		return err
	}
	if s.audit != nil {
		if _, err := s.audit.RecordTx(ctx, tx, audit.Event{
			Type:    "workgraph.package.transitioned",
			Scope:   audit.ScopeTask,
			ScopeID: taskID,
			Actor:   audit.ActorUser,
			Payload: audit.Payload(
				"package_id", packageID,
				"new_state", string(newState),
			),
		}); err != nil {
			return fmt.Errorf("workgraph: transition: audit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("workgraph: transition: commit: %w", err)
	}
	return nil
}

// AppendAttempt appends an Attempt to a package's history durably. It reads
// the current attempts slice, appends the new attempt with the next index,
// writes the JSON column atomically, and inserts a row into
// work_package_attempts. Returns ErrWorkPackageNotFound when the package row
// does not exist.
func (s *WorkGraphStore) AppendAttempt(ctx context.Context, taskID, packageID string, att Attempt) error {
	now := s.now()
	if att.StartedAt.IsZero() {
		att.StartedAt = now
	}
	pkg, err := s.GetPackage(ctx, taskID, packageID)
	if err != nil {
		return err
	}
	att.Index = len(pkg.Attempts)
	pkg.Attempts = append(pkg.Attempts, att)

	attsJSON, err := encodeAttempts(pkg.Attempts)
	if err != nil {
		return fmt.Errorf("workgraph: append attempt: encode: %w", err)
	}
	nowTS := now.Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("workgraph: append attempt: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.SetWorkPackageAttempts(ctx, taskID, packageID, attsJSON, nowTS); err != nil {
		return err
	}
	if err := tx.AppendAttempt(ctx, storage.WorkPackageAttemptRow{
		TaskID:        taskID,
		PackageID:     packageID,
		AttemptIndex:  att.Index,
		State:         string(att.State),
		StartedAt:     att.StartedAt.Format(time.RFC3339Nano),
		FinishedAt:    att.FinishedAt.Format(time.RFC3339Nano),
		FailureReason: att.FailureReason,
		ExitCode:      att.ExitCode,
		AgentRunID:    att.AgentRunID,
	}); err != nil {
		return err
	}
	if s.audit != nil {
		if _, err := s.audit.RecordTx(ctx, tx, audit.Event{
			Type:    "workgraph.package.attempt_appended",
			Scope:   audit.ScopeTask,
			ScopeID: taskID,
			Actor:   audit.ActorUser,
			Payload: audit.Payload(
				"package_id", packageID,
				"attempt_index", att.Index,
				"state", string(att.State),
			),
		}); err != nil {
			return fmt.Errorf("workgraph: append attempt: audit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("workgraph: append attempt: commit: %w", err)
	}
	return nil
}

// loadRuntimeStateByTask returns maps of package_id → current state and
// package_id → attempts for every package of taskID. Used by Save to preserve
// execution history (state + attempts) across re-saves.
func (s *WorkGraphStore) loadRuntimeStateByTask(ctx context.Context, taskID string) (map[string]PackageState, map[string][]Attempt, error) {
	rows, err := s.db.ListWorkPackages(ctx, taskID)
	if err != nil {
		if errors.Is(err, ErrWorkGraphNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	states := make(map[string]PackageState, len(rows))
	atts := make(map[string][]Attempt, len(rows))
	for _, r := range rows {
		states[r.PackageID] = PackageState(r.State)
		a, err := decodeAttempts(r.Attempts)
		if err != nil {
			return nil, nil, fmt.Errorf("decode attempts for %q: %w", r.PackageID, err)
		}
		if len(a) > 0 {
			atts[r.PackageID] = a
		}
	}
	return states, atts, nil
}

// ---- JSON helpers ----
//
// The store owns the JSON encoding of the list-shaped columns so the schema
// stays stable while the domain types evolve. encoding/json is used directly
// (the Attempt struct already has stable JSON tags from M14-04).

func packageToRow(p WorkPackage, nowTS string) (storage.WorkPackageRow, error) {
	acJSON, err := encodeStringList(p.AcceptedACIDs)
	if err != nil {
		return storage.WorkPackageRow{}, fmt.Errorf("encode accepted_ac_ids for %q: %w", p.ID, err)
	}
	scopeJSON, err := encodeStringList(p.AllowedScope)
	if err != nil {
		return storage.WorkPackageRow{}, fmt.Errorf("encode allowed_scope for %q: %w", p.ID, err)
	}
	depsJSON, err := encodeStringList(p.Dependencies)
	if err != nil {
		return storage.WorkPackageRow{}, fmt.Errorf("encode dependencies for %q: %w", p.ID, err)
	}
	attsJSON, err := encodeAttempts(p.Attempts)
	if err != nil {
		return storage.WorkPackageRow{}, fmt.Errorf("encode attempts for %q: %w", p.ID, err)
	}
	return storage.WorkPackageRow{
		TaskID:        p.TaskID,
		PackageID:     p.ID,
		Stage:         string(p.Stage),
		Title:         p.Title,
		Objective:     p.Objective,
		AcceptedACIDs: acJSON,
		AllowedScope:  scopeJSON,
		Dependencies:  depsJSON,
		State:         string(p.State),
		Attempts:      attsJSON,
		UpdatedAt:     nowTS,
	}, nil
}

func rowToPackage(r storage.WorkPackageRow) (WorkPackage, error) {
	acs, err := decodeStringList(r.AcceptedACIDs)
	if err != nil {
		return WorkPackage{}, fmt.Errorf("decode accepted_ac_ids: %w", err)
	}
	scope, err := decodeStringList(r.AllowedScope)
	if err != nil {
		return WorkPackage{}, fmt.Errorf("decode allowed_scope: %w", err)
	}
	deps, err := decodeStringList(r.Dependencies)
	if err != nil {
		return WorkPackage{}, fmt.Errorf("decode dependencies: %w", err)
	}
	atts, err := decodeAttempts(r.Attempts)
	if err != nil {
		return WorkPackage{}, fmt.Errorf("decode attempts: %w", err)
	}
	return WorkPackage{
		ID:            r.PackageID,
		TaskID:        r.TaskID,
		Stage:         Stage(r.Stage),
		Title:         r.Title,
		Objective:     r.Objective,
		AcceptedACIDs: acs,
		AllowedScope:  scope,
		Dependencies:  deps,
		State:         PackageState(r.State),
		Attempts:      atts,
	}, nil
}

func encodeStringList(in []string) (string, error) {
	if len(in) == 0 {
		return "[]", nil
	}
	b, err := storageJSONMarshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeStringList(s string) ([]string, error) {
	if s == "" || s == "null" {
		return nil, nil
	}
	var out []string
	if err := storageJSONUnmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func encodeAttempts(in []Attempt) (string, error) {
	if len(in) == 0 {
		return "[]", nil
	}
	b, err := storageJSONMarshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeAttempts(s string) ([]Attempt, error) {
	if s == "" || s == "null" {
		return nil, nil
	}
	var out []Attempt
	if err := storageJSONUnmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// storageJSONMarshal / storageJSONUnmarshal are thin wrappers over
// encoding/json kept in this file so the workgraph package's import surface
// stays narrow and a future swap (e.g. to a stable canonical encoder) touches
// one place. They mirror internal/storage's json helpers.
func storageJSONMarshal(v any) ([]byte, error) { return jsonMarshal(v) }
func storageJSONUnmarshal(data []byte, v any) error {
	return jsonUnmarshal(data, v)
}
