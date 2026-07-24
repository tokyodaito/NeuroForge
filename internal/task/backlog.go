package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// Priority is a task priority level.
type Priority string

const (
	PriorityLow    Priority = "LOW"
	PriorityNormal Priority = "NORMAL"
	PriorityHigh   Priority = "HIGH"
	PriorityUrgent Priority = "URGENT"
)

// AttachmentRole classifies an attachment (spec §9.4).
type AttachmentRole string

const (
	RoleDesignReference AttachmentRole = "DESIGN_REFERENCE"
	RoleBugScreenshot   AttachmentRole = "BUG_SCREENSHOT"
	RoleRequirements    AttachmentRole = "REQUIREMENTS"
	RoleLog             AttachmentRole = "LOG"
	RoleAPISpec         AttachmentRole = "API_SPECIFICATION"
	RoleExample         AttachmentRole = "EXAMPLE"
	RoleGeneralContext  AttachmentRole = "GENERAL_CONTEXT"
)

// Task is the domain-level task entity.
type Task struct {
	ID          string
	ProjectID   string
	Title       string
	Description string
	Priority    Priority
	State       State
	Attachments []Attachment
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Attachment is a content-addressed file attached to a task.
type Attachment struct {
	Hash     string
	Filename string
	MimeType string
	Size     int64
	Role     AttachmentRole
}

// AddRequest is the input for creating a new task.
type AddRequest struct {
	ProjectID   string            // required
	Description string            // required (can be the only user field)
	Title       string            // optional
	Priority    Priority          // optional, defaults to NORMAL
	Attachments []AttachmentInput // optional
}

// AttachmentInput is an attachment to be stored.
type AttachmentInput struct {
	Path     string         // local filesystem path to read from
	Filename string         // original filename (defaults to filepath.Base(Path))
	Role     AttachmentRole // optional, defaults to GENERAL_CONTEXT
}

// ErrEmptyDescription is returned when neither a description nor an attachment
// is provided (spec §9.2: project + non-empty description or attachment required).
var ErrEmptyDescription = errors.New("task: description or attachment is required")

// ErrNotFound wraps the storage-level not-found error.
var ErrNotFound = storage.ErrTaskNotFound

// Backlog is the task backlog service. It creates tasks, manages task state,
// stores attachments content-addressed, and records mutations to audit.
type Backlog struct {
	db           *storage.DB
	audit        *audit.Recorder
	logger       *slog.Logger
	artifactsDir string
	now          func() time.Time
	seq          atomic.Int64
}

// NewBacklog creates a Backlog backed by db. Attachments are stored
// content-addressed under artifactsDir (spec §9.5).
func NewBacklog(db *storage.DB, rec *audit.Recorder, artifactsDir string, logger *slog.Logger) *Backlog {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return &Backlog{
		db:           db,
		audit:        rec,
		logger:       logger,
		artifactsDir: artifactsDir,
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// Add creates a new task from free-form text. The description can be the only
// user-provided field (spec §9.1, AC-3). Attachments are stored
// content-addressed (§9.5). The task is created in NEW state.
func (b *Backlog) Add(ctx context.Context, req AddRequest) (Task, error) {
	if req.ProjectID == "" {
		return Task{}, fmt.Errorf("task: project id is required")
	}

	// Store attachments first; if description is empty but an attachment exists,
	// that's valid (§9.2).
	var attachments []Attachment
	for _, att := range req.Attachments {
		stored, err := b.storeAttachment(ctx, att, req.ProjectID)
		if err != nil {
			return Task{}, fmt.Errorf("task: store attachment %q: %w", att.Filename, err)
		}
		attachments = append(attachments, stored)
	}

	if strings.TrimSpace(req.Description) == "" && len(attachments) == 0 {
		return Task{}, ErrEmptyDescription
	}

	priority := req.Priority
	if priority == "" {
		priority = PriorityNormal
	}

	id := b.nextID(req.ProjectID)
	now := b.now()

	t := Task{
		ID:          id,
		ProjectID:   req.ProjectID,
		Title:       req.Title,
		Description: req.Description,
		Priority:    priority,
		State:       StateNew,
		Attachments: attachments,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := b.db.CreateTask(ctx, storage.Task{
		ID:          t.ID,
		ProjectID:   t.ProjectID,
		Title:       t.Title,
		Description: t.Description,
		Priority:    string(t.Priority),
		State:       string(t.State),
		CreatedAt:   now.Format(time.RFC3339Nano),
		UpdatedAt:   now.Format(time.RFC3339Nano),
	}); err != nil {
		return Task{}, fmt.Errorf("task: persist: %w", err)
	}

	for _, a := range attachments {
		if err := b.db.CreateAttachment(ctx, storage.TaskAttachment{
			TaskID:    t.ID,
			Hash:      a.Hash,
			Filename:  a.Filename,
			MimeType:  a.MimeType,
			Size:      a.Size,
			Role:      string(a.Role),
			CreatedAt: now.Format(time.RFC3339Nano),
		}); err != nil {
			return Task{}, fmt.Errorf("task: persist attachment: %w", err)
		}
	}

	attSummary := make([]map[string]any, len(attachments))
	for i, a := range attachments {
		attSummary[i] = map[string]any{
			"hash": a.Hash, "filename": a.Filename, "mime": a.MimeType,
		}
	}

	b.auditTask(ctx, t.ID, "task.created", audit.Payload(
		"project", t.ProjectID,
		"title", t.Title,
		"priority", string(t.Priority),
		"attachments", attSummary,
	))

	b.logger.Info("task created", "id", t.ID, "project", t.ProjectID,
		"attachments", len(attachments))
	return t, nil
}

// Get returns a task by id, including its attachments.
func (b *Backlog) Get(ctx context.Context, id string) (Task, error) {
	st, err := b.db.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	t := fromStorage(st)
	atts, err := b.db.ListAttachments(ctx, id)
	if err != nil {
		return Task{}, fmt.Errorf("task: list attachments: %w", err)
	}
	for _, a := range atts {
		t.Attachments = append(t.Attachments, Attachment{
			Hash:     a.Hash,
			Filename: a.Filename,
			MimeType: a.MimeType,
			Size:     a.Size,
			Role:     AttachmentRole(a.Role),
		})
	}
	return t, nil
}

// ListByProject returns all tasks for a project.
func (b *Backlog) ListByProject(ctx context.Context, projectID string) ([]Task, error) {
	rows, err := b.db.ListTasksByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Task, len(rows))
	for i, st := range rows {
		out[i] = fromStorage(st)
	}
	return out, nil
}

// ListAll returns all tasks across all projects.
func (b *Backlog) ListAll(ctx context.Context) ([]Task, error) {
	rows, err := b.db.ListAllTasks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Task, len(rows))
	for i, st := range rows {
		out[i] = fromStorage(st)
	}
	return out, nil
}

// Transition applies a lifecycle action to a task. Invalid transitions are
// rejected. The new state is persisted and audited (spec §29.4, §11.4).
func (b *Backlog) Transition(ctx context.Context, id string, action Action) (Task, error) {
	st, err := b.db.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	current := State(st.State)

	newState, err := CanTransition(current, action)
	if err != nil {
		return Task{}, err
	}

	now := b.now().Format(time.RFC3339Nano)
	if err := b.db.UpdateTaskState(ctx, id, string(newState), now); err != nil {
		return Task{}, err
	}

	b.auditTask(ctx, id, "task.state_changed", audit.Payload(
		"action", string(action),
		"from", string(current),
		"to", string(newState),
	))

	b.logger.Info("task state changed", "id", id,
		"action", action, "from", current, "to", newState)

	t := fromStorage(st)
	t.State = newState
	return t, nil
}

// Pause transitions a task to PAUSED.
func (b *Backlog) Pause(ctx context.Context, id string) (Task, error) {
	return b.Transition(ctx, id, ActionPause)
}

// Cancel transitions a task to CANCELLED.
func (b *Backlog) Cancel(ctx context.Context, id string) (Task, error) {
	return b.Transition(ctx, id, ActionCancel)
}

// ---- attachment storage ----

// storeAttachment reads the file at input.Path, computes its SHA-256 hash, and
// stores it content-addressed under artifactsDir/<hash>. If the artifact
// already exists (same content), it is not duplicated. Metadata is returned for
// persistence in the task_attachments table (spec §9.5).
func (b *Backlog) storeAttachment(ctx context.Context, input AttachmentInput, projectID string) (Attachment, error) {
	if input.Path == "" {
		return Attachment{}, fmt.Errorf("attachment path is required")
	}

	f, err := os.Open(input.Path)
	if err != nil {
		return Attachment{}, fmt.Errorf("open attachment: %w", err)
	}
	defer f.Close()

	hash, size, err := hashAndCount(f)
	if err != nil {
		return Attachment{}, fmt.Errorf("hash attachment: %w", err)
	}

	filename := input.Filename
	if filename == "" {
		filename = filepath.Base(input.Path)
	}

	if b.artifactsDir != "" {
		dest := filepath.Join(b.artifactsDir, hash)
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			if err := copyArtifact(dest, input.Path); err != nil {
				return Attachment{}, fmt.Errorf("store artifact: %w", err)
			}
		}
	}

	role := input.Role
	if role == "" {
		role = RoleGeneralContext
	}

	return Attachment{
		Hash:     hash,
		Filename: filename,
		MimeType: mimeTypeFor(filename),
		Size:     size,
		Role:     role,
	}, nil
}

// copyArtifact copies src to dst. It is used to place attachments in the
// content-addressed store.
func copyArtifact(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// mimeTypeFor guesses the MIME type from a filename.
func mimeTypeFor(filename string) string {
	ext := filepath.Ext(filename)
	if ext == "" {
		return "application/octet-stream"
	}
	mt := mime.TypeByExtension(ext)
	if mt == "" {
		return "application/octet-stream"
	}
	return mt
}

// nextID generates a task id. Format: <projectID>-<seq>.
func (b *Backlog) nextID(projectID string) string {
	n := b.seq.Add(1)
	return fmt.Sprintf("%s-%d", projectID, n)
}

func (b *Backlog) auditTask(ctx context.Context, id, eventType string, payload map[string]any) {
	if b.audit == nil {
		return
	}
	if _, err := b.audit.Record(ctx, audit.Event{
		Type:    eventType,
		Scope:   audit.ScopeTask,
		ScopeID: id,
		Actor:   audit.ActorUser,
		Payload: payload,
	}); err != nil {
		b.logger.Warn("audit record failed", "type", eventType, "err", err)
	}
}

func fromStorage(st storage.Task) Task {
	created, _ := time.Parse(time.RFC3339Nano, st.CreatedAt)
	updated, _ := time.Parse(time.RFC3339Nano, st.UpdatedAt)
	return Task{
		ID:          st.ID,
		ProjectID:   st.ProjectID,
		Title:       st.Title,
		Description: st.Description,
		Priority:    Priority(st.Priority),
		State:       State(st.State),
		CreatedAt:   created,
		UpdatedAt:   updated,
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
