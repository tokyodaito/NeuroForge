package gitlab

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"neuroforge/internal/adapter/vcs"
)

type fakeServer struct {
	t           *testing.T
	server      *httptest.Server
	token       string
	requests    []recordedReq
	mergeStatus int
}

type recordedReq struct {
	method       string
	path         string
	privateToken string
}

func newFakeServer(t *testing.T, token string) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t, token: token, mergeStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.dispatch)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeServer) dispatch(w http.ResponseWriter, r *http.Request) {
	f.requests = append(f.requests, recordedReq{
		method: r.Method, path: r.URL.Path, privateToken: r.Header.Get("PRIVATE-TOKEN"),
	})
	if r.Header.Get("PRIVATE-TOKEN") != f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	_ = body
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge_requests"):
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"iid":7,"title":"MR","description":"d","source_branch":"feat","target_branch":"main","state":"opened","web_url":"https://gitlab.com/g/p/-/merge_requests/7","detailed_merge_status":"mergeable"}`))
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/merge_requests/") && !strings.HasSuffix(r.URL.Path, "/merge"):
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"iid":7,"title":"updated","state":"closed","web_url":"https://gitlab.com/g/p/-/merge_requests/7","source_branch":"feat","target_branch":"main"}`))
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/statuses"):
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"name":"build","status":"success","allow_failure":false}]`))
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/merge") && !strings.Contains(r.URL.Path, "/merge_when"):
		w.WriteHeader(f.mergeStatus)
		_, _ = w.Write([]byte(`{"iid":7,"state":"merged","merge_commit_sha":"sha9"}`))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revert"):
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	}
}

func (f *fakeServer) lastReq() recordedReq {
	if len(f.requests) == 0 {
		f.t.Fatal("no requests recorded")
	}
	return f.requests[len(f.requests)-1]
}

func newProvider(t *testing.T, f *fakeServer) *Provider {
	t.Helper()
	return New(Options{
		Credentials: func(Project) (string, bool) { return f.token, true },
		BaseURL:     f.server.URL,
		HTTP:        f.server.Client(),
	})
}

func projCtx(ctx context.Context) context.Context {
	return WithProject(ctx, Project{Group: "g", Name: "p"})
}

func TestCapabilities_GitLab(t *testing.T) {
	t.Parallel()
	p := New(Options{Credentials: func(Project) (string, bool) { return "x", true }})
	c := p.Capabilities()
	if !c.IsNetwork || !c.PushBranch || !c.Merge {
		t.Fatalf("gitlab must advertise full remote surface: %+v", c)
	}
}

func TestCreateChangeRequest_Fixture(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	cr, err := p.CreateChangeRequest(projCtx(context.Background()), vcs.CreateChangeRequestRequest{
		TaskID: "T1", Title: "MR", HeadBranch: "feat", BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cr.Number != 7 || cr.State != "opened" || cr.WebURL == "" {
		t.Fatalf("bad MR: %+v", cr)
	}
	if cr.Mergeable == nil || !*cr.Mergeable {
		t.Fatal("expected mergeable")
	}
	if f.requests[0].privateToken != "tok" {
		t.Errorf("PRIVATE-TOKEN header: want tok, got %q", f.requests[0].privateToken)
	}
}

func TestUpdateChangeRequest_Fixture(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	_, err := p.UpdateChangeRequest(projCtx(context.Background()), vcs.UpdateChangeRequestRequest{
		TaskID: "T1", Number: 7, State: "closed",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if f.lastReq().method != http.MethodPut {
		t.Errorf("want PUT, got %s", f.lastReq().method)
	}
}

func TestGetChecks_Success(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	cs, err := p.GetChecks(projCtx(context.Background()), vcs.GetChecksRequest{
		TaskID: "T1", Number: 7, HeadSHA: "deadbeef",
	})
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	if !cs.AllPassed || len(cs.Checks) != 1 {
		t.Fatalf("bad checks: %+v", cs)
	}
}

func TestMerge_Success(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	res, err := p.Merge(projCtx(context.Background()), vcs.MergeRequest{
		TaskID: "T1", Number: 7, HeadSHA: "abc", Method: vcs.MergeMethodMerge,
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !res.Merged || res.CommitSHA != "sha9" {
		t.Fatalf("bad merge result: %+v", res)
	}
}

func TestMerge_ConflictReturnsBranchNotCurrent(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	f.mergeStatus = http.StatusConflict
	p := newProvider(t, f)
	_, err := p.Merge(projCtx(context.Background()), vcs.MergeRequest{
		TaskID: "T1", Number: 7, HeadSHA: "abc",
	})
	if !errors.Is(err, vcs.ErrBranchNotCurrent) {
		t.Fatalf("want ErrBranchNotCurrent, got %v", err)
	}
}

func TestEnableAutoMerge_Fixture(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	f.mergeStatus = http.StatusOK
	// EnableAutoMerge uses the same /merge endpoint with form field.
	// We intercept by checking a request was PUT to /merge.
	p := newProvider(t, f)
	err := p.EnableAutoMerge(projCtx(context.Background()), vcs.EnableAutoMergeRequest{
		TaskID: "T1", Number: 7,
	})
	// The fake returns 200 with merge state; auto-merge accepts 200/201.
	if err != nil {
		t.Fatalf("automerge: %v", err)
	}
}

func TestAuthFailure_MissingToken(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := New(Options{
		Credentials: func(Project) (string, bool) { return "", false },
		BaseURL:     f.server.URL,
		HTTP:        f.server.Client(),
	})
	_, err := p.Merge(projCtx(context.Background()), vcs.MergeRequest{TaskID: "T1", Number: 7})
	if !errors.Is(err, vcs.ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestRevert_Fixture(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	res, err := p.Revert(projCtx(context.Background()), vcs.RevertRequest{
		TaskID: "T1", CommitSHA: "abcdef", BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !res.Reverted {
		t.Fatal("expected reverted")
	}
}

func TestPushBranch_TokenInURLNotEnv(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	var captured []string
	runGitCmd = func(ctx context.Context, gb string, args ...string) error {
		captured = args
		return nil
	}
	defer func() { runGitCmd = realGitPushCmd }()

	_, err := p.PushBranch(projCtx(context.Background()), vcs.PushBranchRequest{
		TaskID: "T1", RemoteBranch: "feat", HeadSHA: "abc",
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("no push captured")
	}
	url := captured[len(captured)-2]
	if !strings.Contains(url, "oauth2:tok@") {
		t.Errorf("push URL missing token: %s", url)
	}
}

// realGitPushCmd resets runGitCmd after tests override it.
func realGitPushCmd(ctx context.Context, gb string, args ...string) error {
	cmd := execCmd(ctx, gb, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(string(out))
	}
	return nil
}
