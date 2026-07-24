package storage

import (
	"context"
	"fmt"
)

// Project is the data-only row mirroring the projects table. Domain logic lives
// in internal/project; this struct is the storage-level representation.
type Project struct {
	ID        string
	Name      string
	Path      string
	Remote    string
	State     string
	Profile   string
	CreatedAt string
	UpdatedAt string
}

// CreateProject inserts a new project row. The caller is responsible for
// generating the id and validating the path.
func (d *DB) CreateProject(ctx context.Context, p Project) error {
	return createProject(ctx, d.db, p)
}

// CreateProject inserts a new project row as part of tx.
func (t *Tx) CreateProject(ctx context.Context, p Project) error {
	return createProject(ctx, t.tx, p)
}

func createProject(ctx context.Context, e executor, p Project) error {
	_, err := e.ExecContext(ctx, `
INSERT INTO projects (id, name, path, remote, state, profile, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Path, p.Remote, p.State, p.Profile, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("storage: create project: %w", err)
	}
	return nil
}

// GetProject returns a single project by id. Returns ErrProjectNotFound if the
// row does not exist.
func (d *DB) GetProject(ctx context.Context, id string) (Project, error) {
	var p Project
	err := d.db.QueryRowContext(ctx, `
SELECT id, name, path, remote, state, profile, created_at, updated_at
FROM projects WHERE id = ?`, id).Scan(
		&p.ID, &p.Name, &p.Path, &p.Remote, &p.State, &p.Profile,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("storage: get project %q: %w", id, err)
	}
	return p, nil
}

// GetProjectByPath returns a single project by its filesystem path.
func (d *DB) GetProjectByPath(ctx context.Context, path string) (Project, error) {
	var p Project
	err := d.db.QueryRowContext(ctx, `
SELECT id, name, path, remote, state, profile, created_at, updated_at
FROM projects WHERE path = ?`, path).Scan(
		&p.ID, &p.Name, &p.Path, &p.Remote, &p.State, &p.Profile,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("storage: get project by path %q: %w", path, err)
	}
	return p, nil
}

// ListProjects returns all registered projects ordered by name.
func (d *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, name, path, remote, state, profile, created_at, updated_at
FROM projects ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("storage: list projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &p.Remote, &p.State,
			&p.Profile, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan project: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProjectState updates the state and updated_at timestamp of a project.
func (d *DB) UpdateProjectState(ctx context.Context, id, state, updatedAt string) error {
	return updateProjectState(ctx, d.db, id, state, updatedAt)
}

// UpdateProjectState updates the state and updated_at timestamp of a project as
// part of tx.
func (t *Tx) UpdateProjectState(ctx context.Context, id, state, updatedAt string) error {
	return updateProjectState(ctx, t.tx, id, state, updatedAt)
}

func updateProjectState(ctx context.Context, e executor, id, state, updatedAt string) error {
	res, err := e.ExecContext(ctx,
		`UPDATE projects SET state = ?, updated_at = ? WHERE id = ?`,
		state, updatedAt, id)
	if err != nil {
		return fmt.Errorf("storage: update project state: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// DeleteProject removes a project registration by id. This does NOT touch the
// project's files on disk (spec §8: "удалить регистрацию проекта без удаления
// файлов"). The ON DELETE CASCADE on tasks ensures tasks are removed too.
func (d *DB) DeleteProject(ctx context.Context, id string) error {
	return deleteProject(ctx, d.db, id)
}

// DeleteProject removes a project registration by id as part of tx.
func (t *Tx) DeleteProject(ctx context.Context, id string) error {
	return deleteProject(ctx, t.tx, id)
}

func deleteProject(ctx context.Context, e executor, id string) error {
	res, err := e.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete project: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// CountProjects returns the number of registered projects.
func (d *DB) CountProjects(ctx context.Context) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count projects: %w", err)
	}
	return n, nil
}

// ErrProjectNotFound is returned when a project row is expected but absent.
var ErrProjectNotFound = fmt.Errorf("project not found")
