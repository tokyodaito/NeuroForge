package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/policy"
	"neuroforge/internal/storage"
)

// Project is the domain-level project entity.
type Project struct {
	ID        string
	Name      string
	Path      string
	Remote    string
	State     State
	Profile   policy.Profile
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AddRequest is the input for registering a new project.
type AddRequest struct {
	Path    string         // required: filesystem path to the Git repository
	Name    string         // optional: display name (defaults to directory basename)
	Profile policy.Profile // optional: defaults to LOCAL_REVIEW
}

// ErrAlreadyRegistered is returned when a project path is already registered.
var ErrAlreadyRegistered = errors.New("project: repository already registered")

// ErrNotFound is returned when a project id does not exist.
var ErrNotFound = storage.ErrProjectNotFound

// Registry is the project registry service. It owns project lifecycle state,
// validates inputs, persists to storage, and records every mutation to audit.
type Registry struct {
	db     *storage.DB
	audit  *audit.Recorder
	logger *slog.Logger
	now    func() time.Time
}

// NewRegistry creates a Registry backed by db. The audit recorder records every
// state mutation. If logger is nil, logging is suppressed.
func NewRegistry(db *storage.DB, rec *audit.Recorder, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return &Registry{
		db:     db,
		audit:  rec,
		logger: logger,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// Add registers a new project. It validates that path is a Git repository,
// generates a unique id from the directory name, and persists the project in
// DISABLED state. The project's files are never modified (spec §17.1).
func (r *Registry) Add(ctx context.Context, req AddRequest) (Project, error) {
	abs, err := filepath.Abs(req.Path)
	if err != nil {
		return Project{}, fmt.Errorf("project: resolve path: %w", err)
	}

	// Validate it's a Git repository.
	info, err := ValidateGitRepo(ctx, abs)
	if err != nil {
		return Project{}, err
	}

	// Check for duplicate path.
	if existing, err := r.db.GetProjectByPath(ctx, abs); err == nil && existing.ID != "" {
		return Project{}, fmt.Errorf("%w: %s (id=%s)", ErrAlreadyRegistered, abs, existing.ID)
	}

	name := req.Name
	if name == "" {
		name = filepath.Base(abs)
	}

	profile := req.Profile
	if profile == "" {
		profile = policy.ProfileLocalReview
	}
	if !profile.IsValid() {
		return Project{}, fmt.Errorf("project: invalid profile %q", profile)
	}

	id := slugify(filepath.Base(abs))
	if id == "" {
		id = "project"
	}
	// Ensure uniqueness: append -2, -3, etc. if the id is taken.
	id = r.uniqueID(ctx, id)

	now := r.now()
	p := Project{
		ID:        id,
		Name:      name,
		Path:      abs,
		Remote:    info.Remote,
		State:     StateDisabled,
		Profile:   profile,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Persist the project and its audit event inside one SQLite transaction so
	// the registration is durable (and audited) atomically before any external
	// action (spec §11.4, ADR-0003).
	tx, err := r.db.BeginTx(ctx)
	if err != nil {
		return Project{}, fmt.Errorf("project: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.CreateProject(ctx, storage.Project{
		ID:        p.ID,
		Name:      p.Name,
		Path:      p.Path,
		Remote:    p.Remote,
		State:     string(p.State),
		Profile:   string(p.Profile),
		CreatedAt: now.Format(time.RFC3339Nano),
		UpdatedAt: now.Format(time.RFC3339Nano),
	}); err != nil {
		return Project{}, fmt.Errorf("project: persist: %w", err)
	}

	if err := r.auditProjectTx(ctx, tx, p.ID, "project.added", audit.Payload(
		"path", p.Path, "remote", p.Remote, "profile", string(p.Profile))); err != nil {
		return Project{}, err
	}

	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("project: commit: %w", err)
	}

	r.logger.Info("project registered", "id", p.ID, "path", p.Path)
	return p, nil
}

// Get returns a project by id.
func (r *Registry) Get(ctx context.Context, id string) (Project, error) {
	sp, err := r.db.GetProject(ctx, id)
	if err != nil {
		return Project{}, err
	}
	return fromStorage(sp), nil
}

// List returns all registered projects.
func (r *Registry) List(ctx context.Context) ([]Project, error) {
	rows, err := r.db.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Project, len(rows))
	for i, sp := range rows {
		out[i] = fromStorage(sp)
	}
	return out, nil
}

// Remove unregisters a project by id. It does NOT delete the project's files
// (spec §8: "удалить регистрацию проекта без удаления файлов"). Associated
// tasks are cascade-deleted at the storage level.
func (r *Registry) Remove(ctx context.Context, id string) error {
	p, err := r.db.GetProject(ctx, id)
	if err != nil {
		return err
	}
	// Delete the registration and record the audit event atomically (spec
	// §11.4, ADR-0003).
	tx, err := r.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("project: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.DeleteProject(ctx, id); err != nil {
		return err
	}
	if err := r.auditProjectTx(ctx, tx, id, "project.removed", audit.Payload("path", p.Path)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("project: commit: %w", err)
	}
	r.logger.Info("project removed", "id", id)
	return nil
}

// Transition applies a lifecycle action to a project, validating the state
// machine transition. The new state is persisted BEFORE any effect is taken
// (spec §11.4), and the transition is recorded in audit.
func (r *Registry) Transition(ctx context.Context, id string, action Action) (Project, error) {
	sp, err := r.db.GetProject(ctx, id)
	if err != nil {
		return Project{}, err
	}
	current := State(sp.State)

	newState, err := CanTransition(current, action)
	if err != nil {
		return Project{}, err
	}

	now := r.now().Format(time.RFC3339Nano)
	// Persist the new state and the audit event atomically (spec §11.4,
	// ADR-0003): the intended next state is durable before any external action.
	tx, err := r.db.BeginTx(ctx)
	if err != nil {
		return Project{}, fmt.Errorf("project: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.UpdateProjectState(ctx, id, string(newState), now); err != nil {
		return Project{}, err
	}

	if err := r.auditProjectTx(ctx, tx, id, "project.state_changed", audit.Payload(
		"action", string(action),
		"from", string(current),
		"to", string(newState),
	)); err != nil {
		return Project{}, err
	}

	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("project: commit: %w", err)
	}

	r.logger.Info("project state changed", "id", id,
		"action", action, "from", current, "to", newState)

	p := fromStorage(sp)
	p.State = newState
	p.UpdatedAt = r.now()
	return p, nil
}

// Start transitions a project from DISABLED to IDLE.
func (r *Registry) Start(ctx context.Context, id string) (Project, error) {
	return r.Transition(ctx, id, ActionStart)
}

// Pause transitions a project to PAUSED.
func (r *Registry) Pause(ctx context.Context, id string) (Project, error) {
	return r.Transition(ctx, id, ActionPause)
}

// Stop transitions a project to DISABLED.
func (r *Registry) Stop(ctx context.Context, id string) (Project, error) {
	return r.Transition(ctx, id, ActionStop)
}

// ---- helpers ----

func (r *Registry) uniqueID(ctx context.Context, base string) string {
	id := base
	suffix := 2
	for {
		if _, err := r.db.GetProject(ctx, id); err != nil {
			break
		}
		id = fmt.Sprintf("%s-%d", base, suffix)
		suffix++
	}
	return id
}

// auditProjectTx records one project-scoped audit event into tx. Because it
// shares the caller's transaction, an audit failure aborts the whole mutation
// (spec §29.4: audit is mandatory; §11.4: state + audit are atomic).
func (r *Registry) auditProjectTx(ctx context.Context, a audit.AuditAppender, id, eventType string, payload map[string]any) error {
	if r.audit == nil {
		return nil
	}
	if _, err := r.audit.RecordTx(ctx, a, audit.Event{
		Type:    eventType,
		Scope:   audit.ScopeProject,
		ScopeID: id,
		Actor:   audit.ActorUser,
		Payload: payload,
	}); err != nil {
		return fmt.Errorf("project: audit %s: %w", eventType, err)
	}
	return nil
}

func fromStorage(sp storage.Project) Project {
	created, _ := time.Parse(time.RFC3339Nano, sp.CreatedAt)
	updated, _ := time.Parse(time.RFC3339Nano, sp.UpdatedAt)
	return Project{
		ID:        sp.ID,
		Name:      sp.Name,
		Path:      sp.Path,
		Remote:    sp.Remote,
		State:     State(sp.State),
		Profile:   policy.Profile(sp.Profile),
		CreatedAt: created,
		UpdatedAt: updated,
	}
}

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a name to a lowercase kebab-case slug suitable for use as
// a project id.
func slugify(name string) string {
	s := strings.ToLower(name)
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
