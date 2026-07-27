package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// fakeSpecAPI is a test double for transport.SpecAPI. It records the calls so
// contract tests can assert on what reached the daemon-side interface (the
// transport layer must not silently invent fields or drop them).
type fakeSpecAPI struct {
	lastCompileReq CompileSpecRequest
	lastGetVersion int
	lastLockReq    LockSpecRequest
	compileErr     error
	getErr         error
	listErr        error
	lockErr        error
	compileResult  CompileSpecResultDTO
	getResult      SpecificationDTO
	listResult     []int
	lockResult     SpecificationDTO
}

func (f *fakeSpecAPI) CompileSpec(ctx context.Context, req CompileSpecRequest) (CompileSpecResultDTO, error) {
	f.lastCompileReq = req
	if f.compileErr != nil {
		return CompileSpecResultDTO{}, f.compileErr
	}
	return f.compileResult, nil
}

func (f *fakeSpecAPI) GetSpecification(ctx context.Context, taskID string, version int) (SpecificationDTO, error) {
	f.lastGetVersion = version
	if f.getErr != nil {
		return SpecificationDTO{}, f.getErr
	}
	return f.getResult, nil
}

func (f *fakeSpecAPI) ListSpecificationVersions(ctx context.Context, taskID string) ([]int, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResult, nil
}

func (f *fakeSpecAPI) LockSpecification(ctx context.Context, req LockSpecRequest) (SpecificationDTO, error) {
	f.lastLockReq = req
	if f.lockErr != nil {
		return SpecificationDTO{}, f.lockErr
	}
	return f.lockResult, nil
}

func startSpecTestServer(t *testing.T, spec SpecAPI) (*Server, string, string) {
	t.Helper()
	bus := NewBus()
	srv, err := NewServer(Config{
		Addr:    "127.0.0.1:0",
		Token:   "test-token-that-is-long-enough-32+chars",
		SpecAPI: spec,
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
	})
	return srv, "http://" + addr.String(), "test-token-that-is-long-enough-32+chars"
}

func sampleSpecDTO() SpecificationDTO {
	return SpecificationDTO{
		TaskID:    "p-1",
		Version:   1,
		Objective: "Implement the foo bar.",
		AcceptanceCriteria: []AcceptanceCriterionDTO{
			{ID: "AC-1", Statement: "Foo returns 200."},
			{ID: "AC-2", Statement: "Bar is persisted."},
		},
		Risk:       "R2",
		Complexity: "C1",
		Locked:     false,
		CreatedAt:  "2026-07-27T00:00:00Z",
	}
}

// TestSpecAPI_Compile_HappyPath proves POST /tasks/{id}/specification/compile
// routes through to SpecAPI.CompileSpec, returns 200 with the result DTO, and
// propagates the URL path id into req.TaskID (the body cannot override it).
func TestSpecAPI_Compile_HappyPath(t *testing.T) {
	fake := &fakeSpecAPI{
		compileResult: CompileSpecResultDTO{
			Specification: sampleSpecDTO(),
			Confidence:    "HIGH",
			Created:       true,
			RiskReasons:   []string{"public API surface"},
		},
	}
	_, baseURL, token := startSpecTestServer(t, fake)
	cli := NewClient(baseURL, token)

	out, err := cli.CompileSpec(context.Background(), "p-1", CompileSpecRequest{LockedBy: "alice"})
	if err != nil {
		t.Fatalf("CompileSpec: %v", err)
	}
	if out.Created != true {
		t.Errorf("Created=%v, want true", out.Created)
	}
	if out.Confidence != "HIGH" {
		t.Errorf("Confidence=%q, want HIGH", out.Confidence)
	}
	if out.Specification.TaskID != "p-1" {
		t.Errorf("TaskID=%q, want p-1", out.Specification.TaskID)
	}
	if out.Specification.Version != 1 {
		t.Errorf("Version=%d, want 1", out.Specification.Version)
	}
	if len(out.Specification.AcceptanceCriteria) != 2 {
		t.Errorf("AC count=%d, want 2", len(out.Specification.AcceptanceCriteria))
	}
	if out.Specification.AcceptanceCriteria[0].ID != "AC-1" {
		t.Errorf("first AC ID=%q, want AC-1", out.Specification.AcceptanceCriteria[0].ID)
	}
	// URL path id wins over any body TaskID.
	if fake.lastCompileReq.TaskID != "p-1" {
		t.Errorf("daemon received TaskID=%q, want p-1", fake.lastCompileReq.TaskID)
	}
	if fake.lastCompileReq.LockedBy != "alice" {
		t.Errorf("daemon received LockedBy=%q, want alice", fake.lastCompileReq.LockedBy)
	}
}

// TestSpecAPI_Compile_AllowsEmptyBody verifies the compile endpoint tolerates
// an empty body (Content-Length: 0). This matters because the CLI's text-mode
// compile-and-save call has nothing to send besides the path id.
func TestSpecAPI_Compile_AllowsEmptyBody(t *testing.T) {
	fake := &fakeSpecAPI{compileResult: CompileSpecResultDTO{Specification: sampleSpecDTO()}}
	_, baseURL, token := startSpecTestServer(t, fake)
	cli := NewClient(baseURL, token)

	out, err := cli.CompileSpec(context.Background(), "p-1", CompileSpecRequest{})
	if err != nil {
		t.Fatalf("CompileSpec with empty body: %v", err)
	}
	if out.Specification.TaskID != "p-1" {
		t.Errorf("TaskID=%q, want p-1", out.Specification.TaskID)
	}
}

// TestSpecAPI_Get_LatestAndVersion proves GET /tasks/{id}/specification returns
// the latest version by default and the requested version when ?version=N is
// present. The version query param is parsed server-side and forwarded.
func TestSpecAPI_Get_LatestAndVersion(t *testing.T) {
	fake := &fakeSpecAPI{getResult: sampleSpecDTO()}
	_, baseURL, token := startSpecTestServer(t, fake)
	cli := NewClient(baseURL, token)

	// Latest (no version query).
	if _, err := cli.GetSpecification(context.Background(), "p-1", 0); err != nil {
		t.Fatalf("GetSpecification latest: %v", err)
	}
	if fake.lastGetVersion != 0 {
		t.Errorf("latest forwarded version=%d, want 0", fake.lastGetVersion)
	}

	// Specific version.
	if _, err := cli.GetSpecification(context.Background(), "p-1", 3); err != nil {
		t.Fatalf("GetSpecification v3: %v", err)
	}
	if fake.lastGetVersion != 3 {
		t.Errorf("v3 forwarded version=%d, want 3", fake.lastGetVersion)
	}
}

// TestSpecAPI_Get_InvalidVersionParam verifies a non-integer ?version yields
// HTTP 400, not a downstream "specification not found" — the bad input must be
// rejected at the transport edge.
func TestSpecAPI_Get_InvalidVersionParam(t *testing.T) {
	_, baseURL, token := startSpecTestServer(t, &fakeSpecAPI{})
	req, _ := http.NewRequest("GET", baseURL+"/tasks/p-1/specification?version=abc", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for non-integer version", resp.StatusCode)
	}
}

// TestSpecAPI_Get_NotFoundMaps404 verifies that a "specification not found"
// error from the daemon surfaces as HTTP 404 in the wire response. getJSON
// returns a plain error string (not *APIError — only POST helpers go through
// checkResponse), so we assert on the embedded status text.
func TestSpecAPI_Get_NotFoundMaps404(t *testing.T) {
	fake := &fakeSpecAPI{getErr: errors.New("specification not found")}
	_, baseURL, token := startSpecTestServer(t, fake)
	cli := NewClient(baseURL, token)

	_, err := cli.GetSpecification(context.Background(), "p-1", 0)
	if err == nil {
		t.Fatal("expected error for not-found spec")
	}
	if !strings.Contains(err.Error(), "status 404") {
		t.Errorf("err=%q, want it to contain 'status 404'", err.Error())
	}
	if !strings.Contains(err.Error(), "specification not found") {
		t.Errorf("err=%q, want it to contain 'specification not found'", err.Error())
	}
}

// TestSpecAPI_Lock_HappyPath proves POST /tasks/{id}/specification/lock routes
// to SpecAPI.LockSpecification with the body's Version + LockedBy preserved
// and the URL path id overriding any body TaskID.
func TestSpecAPI_Lock_HappyPath(t *testing.T) {
	locked := sampleSpecDTO()
	locked.Locked = true
	locked.LockedBy = "alice"
	locked.LockedAt = "2026-07-27T00:00:01Z"
	fake := &fakeSpecAPI{lockResult: locked}
	_, baseURL, token := startSpecTestServer(t, fake)
	cli := NewClient(baseURL, token)

	out, err := cli.LockSpecification(context.Background(), "p-1", LockSpecRequest{Version: 1, LockedBy: "alice"})
	if err != nil {
		t.Fatalf("LockSpecification: %v", err)
	}
	if !out.Locked {
		t.Errorf("Locked=%v, want true", out.Locked)
	}
	if out.LockedBy != "alice" {
		t.Errorf("LockedBy=%q, want alice", out.LockedBy)
	}
	if fake.lastLockReq.TaskID != "p-1" {
		t.Errorf("daemon received TaskID=%q, want p-1", fake.lastLockReq.TaskID)
	}
	if fake.lastLockReq.Version != 1 {
		t.Errorf("daemon received Version=%d, want 1", fake.lastLockReq.Version)
	}
	if fake.lastLockReq.LockedBy != "alice" {
		t.Errorf("daemon received LockedBy=%q, want alice", fake.lastLockReq.LockedBy)
	}
}

// TestSpecAPI_Lock_NotFoundMaps404 verifies that a lock attempt on a
// non-existent version surfaces as 404, not 500.
func TestSpecAPI_Lock_NotFoundMaps404(t *testing.T) {
	fake := &fakeSpecAPI{lockErr: errors.New("specification not found")}
	_, baseURL, token := startSpecTestServer(t, fake)
	cli := NewClient(baseURL, token)

	_, err := cli.LockSpecification(context.Background(), "p-1", LockSpecRequest{Version: 99})
	if err == nil {
		t.Fatal("expected error for lock on missing spec")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", apiErr.Code)
	}
}

// TestSpecAPI_ListVersions_EmptyIsArrayNotNil proves the versions endpoint
// coalesces a nil slice to [] (JSON array), matching the existing empty-list
// contract used across the API.
func TestSpecAPI_ListVersions_EmptyIsArrayNotNil(t *testing.T) {
	_, baseURL, token := startSpecTestServer(t, &fakeSpecAPI{listResult: nil})
	cli := NewClient(baseURL, token)

	out, err := cli.ListSpecificationVersions(context.Background(), "p-1")
	if err != nil {
		t.Fatalf("ListSpecificationVersions: %v", err)
	}
	if out == nil {
		t.Fatal("expected empty slice, got nil (would marshal as null)")
	}
	if len(out) != 0 {
		t.Errorf("len=%d, want 0", len(out))
	}
}

// TestSpecAPI_ListVersions_NonEmpty proves the wire form is a JSON array of
// integers (the body the daemon actually sends).
func TestSpecAPI_ListVersions_NonEmpty(t *testing.T) {
	_, baseURL, token := startSpecTestServer(t, &fakeSpecAPI{listResult: []int{1, 2, 3}})
	cli := NewClient(baseURL, token)

	out, err := cli.ListSpecificationVersions(context.Background(), "p-1")
	if err != nil {
		t.Fatalf("ListSpecificationVersions: %v", err)
	}
	if len(out) != 3 || out[0] != 1 || out[2] != 3 {
		t.Errorf("got %v, want [1 2 3]", out)
	}
}

// TestSpecAPI_RequiresToken proves every spec endpoint requires the bearer
// token (auth contract uniformity). A missing token yields 401, not 503 or
// 200.
func TestSpecAPI_RequiresToken(t *testing.T) {
	_, baseURL, _ := startSpecTestServer(t, &fakeSpecAPI{})
	for _, path := range []string{
		"/tasks/p-1/specification",
		"/tasks/p-1/specification/versions",
	} {
		resp, err := http.Get(baseURL + path)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status=%d, want 401", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	for _, path := range []string{
		"/tasks/p-1/specification/compile",
		"/tasks/p-1/specification/lock",
	} {
		req, _ := http.NewRequest("POST", baseURL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status=%d, want 401", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestSpecAPI_NilAdapterYields503 proves a server with SpecAPI unset returns
// HTTP 503 on every spec endpoint (the documented "API not configured"
// contract; mirrors ProjectAPI/TaskAPI/SchedulerAPI behaviour).
func TestSpecAPI_NilAdapterYields503(t *testing.T) {
	bus := NewBus()
	srv, err := NewServer(Config{
		Addr:  "127.0.0.1:0",
		Token: "test-token-that-is-long-enough-32+chars",
		// SpecAPI deliberately nil
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
	t.Cleanup(cancel)
	baseURL := "http://" + addr.String()
	token := "test-token-that-is-long-enough-32+chars"

	for _, path := range []string{
		"/tasks/p-1/specification",
		"/tasks/p-1/specification/versions",
	} {
		req, _ := http.NewRequest("GET", baseURL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status=%d, want 503", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestSpecAPI_DTO_JSONShape pins the JSON shape of SpecificationDTO so future
// changes that would silently rename fields (and break the CLI/TUI parsers)
// are caught at the transport-contract level.
func TestSpecAPI_DTO_JSONShape(t *testing.T) {
	dto := sampleSpecDTO()
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"task_id":"p-1"`,
		`"version":1`,
		`"objective":"Implement the foo bar."`,
		`"acceptance_criteria":[{"id":"AC-1","statement":"Foo returns 200."}`,
		`"risk":"R2"`,
		`"complexity":"C1"`,
		`"locked":false`,
		`"created_at":"2026-07-27T00:00:00Z"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("DTO JSON missing %q\nfull: %s", want, s)
		}
	}
}

// TestSpecAPI_LockedErrorMaps409 verifies the writeAPIError "is locked" case
// added by M14-03 maps ErrSpecificationLocked to HTTP 409 Conflict (not 500).
// This is the transport-level guard for the locked-update rejection path.
func TestSpecAPI_LockedErrorMaps409(t *testing.T) {
	fake := &fakeSpecAPI{lockErr: errors.New("specification is locked")}
	_, baseURL, token := startSpecTestServer(t, fake)
	cli := NewClient(baseURL, token)

	_, err := cli.LockSpecification(context.Background(), "p-1", LockSpecRequest{Version: 1})
	if err == nil {
		t.Fatal("expected error for locked conflict")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != http.StatusConflict {
		t.Errorf("status=%d, want 409 Conflict for locked spec", apiErr.Code)
	}
}
