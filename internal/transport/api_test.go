package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// fakeProjectAPI is a test double for transport.ProjectAPI.
type fakeProjectAPI struct {
	projects []ProjectDTO
}

func (f *fakeProjectAPI) ListProjects(ctx context.Context) ([]ProjectDTO, error) {
	return f.projects, nil
}
func (f *fakeProjectAPI) AddProject(ctx context.Context, req AddProjectRequest) (ProjectDTO, error) {
	p := ProjectDTO{ID: "new-1", Name: req.Name, Path: req.Path, State: "DISABLED"}
	f.projects = append(f.projects, p)
	return p, nil
}
func (f *fakeProjectAPI) GetProject(ctx context.Context, id string) (ProjectDTO, error) {
	for _, p := range f.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return ProjectDTO{}, &APIError{Code: 404, Msg: "project not found"}
}
func (f *fakeProjectAPI) RemoveProject(ctx context.Context, id string) error {
	return nil
}
func (f *fakeProjectAPI) StartProject(ctx context.Context, id string) (ProjectDTO, error) {
	return f.GetProject(ctx, id)
}
func (f *fakeProjectAPI) PauseProject(ctx context.Context, id string) (ProjectDTO, error) {
	return f.GetProject(ctx, id)
}
func (f *fakeProjectAPI) StopProject(ctx context.Context, id string) (ProjectDTO, error) {
	return f.GetProject(ctx, id)
}

// fakeTaskAPI is a test double for transport.TaskAPI.
type fakeTaskAPI struct {
	tasks []TaskDTO
}

func (f *fakeTaskAPI) ListTasks(ctx context.Context, projectID string) ([]TaskDTO, error) {
	if projectID == "" {
		return f.tasks, nil
	}
	var out []TaskDTO
	for _, t := range f.tasks {
		if t.ProjectID == projectID {
			out = append(out, t)
		}
	}
	return out, nil
}
func (f *fakeTaskAPI) AddTask(ctx context.Context, req AddTaskRequest) (TaskDTO, error) {
	t := TaskDTO{ID: "task-1", ProjectID: req.ProjectID, Description: req.Description, State: "NEW"}
	f.tasks = append(f.tasks, t)
	return t, nil
}
func (f *fakeTaskAPI) GetTask(ctx context.Context, id string) (TaskDTO, error) {
	for _, t := range f.tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return TaskDTO{}, &APIError{Code: 404, Msg: "task not found"}
}
func (f *fakeTaskAPI) PauseTask(ctx context.Context, id string) (TaskDTO, error) {
	return f.GetTask(ctx, id)
}
func (f *fakeTaskAPI) CancelTask(ctx context.Context, id string) (TaskDTO, error) {
	return f.GetTask(ctx, id)
}

func startAPITestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	bus := NewBus()
	srv, err := NewServer(Config{
		Addr:       "127.0.0.1:0",
		Token:      "test-token-that-is-long-enough-32+chars",
		ProjectAPI: &fakeProjectAPI{},
		TaskAPI:    &fakeTaskAPI{},
	}, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		time.Sleep(50 * time.Millisecond)
	})
	return srv, "http://" + addr.String(), "test-token-that-is-long-enough-32+chars"
}

func TestAPI_ProjectEndpoints(t *testing.T) {
	_, baseURL, token := startAPITestServer(t)
	cli := NewClient(baseURL, token)
	ctx := context.Background()

	// Add a project.
	p, err := cli.AddProject(ctx, AddProjectRequest{Path: "/tmp/repo", Name: "Test"})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if p.ID != "new-1" {
		t.Errorf("ID=%s, want new-1", p.ID)
	}

	// List projects.
	projects, err := cli.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects=%d, want 1", len(projects))
	}

	// Get project.
	p, err = cli.GetProject(ctx, "new-1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.Name != "Test" {
		t.Errorf("Name=%s, want Test", p.Name)
	}

	// Get nonexistent -> error.
	_, err = cli.GetProject(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent project")
	}

	// Start project.
	_, err = cli.StartProject(ctx, "new-1")
	if err != nil {
		t.Fatalf("StartProject: %v", err)
	}

	// Remove project.
	if err := cli.RemoveProject(ctx, "new-1"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
}

func TestAPI_TaskEndpoints(t *testing.T) {
	_, baseURL, token := startAPITestServer(t)
	cli := NewClient(baseURL, token)
	ctx := context.Background()

	// Add a task.
	task, err := cli.AddTask(ctx, AddTaskRequest{
		ProjectID:   "proj-1",
		Description: "test task",
		Priority:    "HIGH",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if task.ID != "task-1" {
		t.Errorf("ID=%s, want task-1", task.ID)
	}

	// List tasks.
	tasks, err := cli.ListTasks(ctx, "proj-1")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks=%d, want 1", len(tasks))
	}

	// Get task.
	task, err = cli.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	// Pause task.
	_, err = cli.PauseTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	// Cancel task.
	_, err = cli.CancelTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
}

func TestAPI_Unauthenticated(t *testing.T) {
	_, baseURL, _ := startAPITestServer(t)
	// No token -> 401.
	resp, err := http.Get(baseURL + "/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-token status=%d, want 401", resp.StatusCode)
	}
}

func TestAPI_EmptyListReturnsArray(t *testing.T) {
	_, baseURL, token := startAPITestServer(t)
	cli := NewClient(baseURL, token)
	ctx := context.Background()

	// Empty list should return [] not null.
	projects, err := cli.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	// Verify it's an empty slice, not nil.
	if projects == nil {
		t.Error("expected empty slice, got nil")
	}
}

func TestAPI_POSTWithBody(t *testing.T) {
	_, baseURL, token := startAPITestServer(t)

	body, _ := json.Marshal(AddTaskRequest{
		ProjectID:   "p1",
		Description: "via HTTP",
	})
	req, _ := http.NewRequest("POST", baseURL+"/tasks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status=%d, want 201", resp.StatusCode)
	}
	var task TaskDTO
	json.NewDecoder(resp.Body).Decode(&task)
	if task.Description != "via HTTP" {
		t.Errorf("description=%s, want 'via HTTP'", task.Description)
	}
}
