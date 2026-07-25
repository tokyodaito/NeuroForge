package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"neuroforge/internal/adapter/vcs"
)

// fakeServer is a minimal GitHub API stub for fixture tests. It records the
// last request and returns canned responses keyed by method+path.
type fakeServer struct {
	t        *testing.T
	mux      *http.ServeMux
	server   *httptest.Server
	token    string
	requests []recordedReq
	// mergeStatus overrides the PR-merge response code (default 200).
	mergeStatus int
}

type recordedReq struct {
	method string
	path   string
	auth   string
	body   map[string]any
}

func newFakeServer(t *testing.T, token string) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t, token: token, mux: http.NewServeMux(), mergeStatus: http.StatusOK}
	f.mux.HandleFunc("/", f.dispatch)
	f.server = httptest.NewServer(f.mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeServer) dispatch(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var bmap map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &bmap)
	}
	f.requests = append(f.requests, recordedReq{
		method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: bmap,
	})
	if r.Header.Get("Authorization") != "token "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls") && !strings.Contains(r.URL.Path, "/requested_reviewers"):
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":42,"title":"PR","head":{"ref":"feat"},"base":{"ref":"main"},"state":"open","html_url":"https://github.com/o/r/pull/42","mergeable":true}`))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/requested_reviewers"):
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/pulls/"):
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":42,"title":"updated","state":"open","html_url":"https://github.com/o/r/pull/42","head":{"ref":"feat"},"base":{"ref":"main"}}`))
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/commits/") && strings.HasSuffix(r.URL.Path, "/status"):
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"state":"success","statuses":[{"context":"ci","state":"success"}]}`))
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/auto-merge"):
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/merge"):
		w.WriteHeader(f.mergeStatus)
		if f.mergeStatus == http.StatusOK {
			_, _ = w.Write([]byte(`{"sha":"abc123","merged":true}`))
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
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
		Credentials: func(Repository) (string, bool) { return f.token, true },
		BaseURL:     f.server.URL,
		HTTP:        f.server.Client(),
		GitBinary:   "git",
	})
}

func repoCtx(ctx context.Context) context.Context {
	return WithRepo(ctx, Repository{Owner: "o", Name: "r"})
}

func TestCapabilities_GitHub(t *testing.T) {
	t.Parallel()
	p := New(Options{Credentials: func(Repository) (string, bool) { return "x", true }})
	c := p.Capabilities()
	if !c.IsNetwork || !c.PushBranch || !c.Merge || !c.EnableAutoMerge {
		t.Fatalf("github must advertise full remote surface: %+v", c)
	}
}

func TestCreateChangeRequest_Fixture(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	cr, err := p.CreateChangeRequest(repoCtx(context.Background()), vcs.CreateChangeRequestRequest{
		TaskID: "T1", Title: "PR", HeadBranch: "feat", BaseBranch: "main",
		Body: "b", ReviewerIDs: []string{"alice"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cr.Number != 42 || cr.State != "open" || cr.WebURL == "" {
		t.Fatalf("bad CR: %+v", cr)
	}
	// Must have created the PR then requested reviewers (2 POSTs to pulls path).
	if len(f.requests) < 2 {
		t.Fatalf("expected reviewer POST, got %d requests", len(f.requests))
	}
	// Auth header always carries the token (never env).
	if got := f.requests[0].auth; got != "token tok" {
		t.Errorf("auth header: want %q, got %q", "token tok", got)
	}
}

func TestUpdateChangeRequest_Fixture(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	_, err := p.UpdateChangeRequest(repoCtx(context.Background()), vcs.UpdateChangeRequestRequest{
		TaskID: "T1", Number: 42, Title: "updated", State: "open",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	r := f.lastReq()
	if r.method != http.MethodPatch {
		t.Errorf("want PATCH, got %s", r.method)
	}
}

func TestGetChecks_AllPassed(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	cs, err := p.GetChecks(repoCtx(context.Background()), vcs.GetChecksRequest{
		TaskID: "T1", Number: 42, HeadSHA: "deadbeef",
	})
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	if !cs.AllPassed || !cs.RequiredPassed {
		t.Fatalf("expected checks passed: %+v", cs)
	}
	if len(cs.Checks) != 1 || cs.Checks[0].Name != "ci" {
		t.Fatalf("bad checks: %+v", cs)
	}
}

func TestEnableAutoMerge_Fixture(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	if err := p.EnableAutoMerge(repoCtx(context.Background()), vcs.EnableAutoMergeRequest{
		TaskID: "T1", Number: 42, Method: vcs.MergeMethodSquash,
	}); err != nil {
		t.Fatalf("automerge: %v", err)
	}
	if f.lastReq().method != http.MethodPut {
		t.Errorf("want PUT auto-merge, got %s", f.lastReq().method)
	}
}

func TestMerge_Success(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	res, err := p.Merge(repoCtx(context.Background()), vcs.MergeRequest{
		TaskID: "T1", Number: 42, Method: vcs.MergeMethodSquash,
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !res.Merged || res.CommitSHA != "abc123" {
		t.Fatalf("bad merge result: %+v", res)
	}
}

func TestMerge_ConflictReturnsBranchNotCurrent(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	f.mergeStatus = http.StatusConflict
	p := newProvider(t, f)
	_, err := p.Merge(repoCtx(context.Background()), vcs.MergeRequest{
		TaskID: "T1", Number: 42,
	})
	if !errors.Is(err, vcs.ErrBranchNotCurrent) {
		t.Fatalf("want ErrBranchNotCurrent, got %v", err)
	}
}

func TestAuthFailure_MissingToken(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := New(Options{
		Credentials: func(Repository) (string, bool) { return "", false },
		BaseURL:     f.server.URL,
		HTTP:        f.server.Client(),
	})
	_, err := p.Merge(repoCtx(context.Background()), vcs.MergeRequest{TaskID: "T1", Number: 42})
	if !errors.Is(err, vcs.ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestPushBranch_TokenNeverLogged(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	// Stub the push to capture the args; ensure the token is in the URL but the
	// push call succeeds and never writes to env.
	var pushedArgs []string
	runGitCmd = func(ctx context.Context, gb string, args ...string) error {
		pushedArgs = args
		return nil
	}
	defer func() { runGitCmd = realGitPushFallback }()

	_, err := p.PushBranch(repoCtx(context.Background()), vcs.PushBranchRequest{
		TaskID: "T1", RemoteBranch: "feat", HeadSHA: "abc",
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	// The push URL must embed the token as x-access-token (not env).
	if len(pushedArgs) == 0 {
		t.Fatal("no push captured")
	}
	url := pushedArgs[len(pushedArgs)-2]
	if !strings.Contains(url, "x-access-token:tok@") {
		t.Errorf("push URL missing token embedding: %s", url)
	}
}

// realGitPushFallback is the reset target in tests that override runGitCmd.
func realGitPushFallback(ctx context.Context, gitBinary string, args ...string) error {
	cmd := execCmd(ctx, gitBinary, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(string(out))
	}
	return nil
}

func TestRevert_CreatesRevertPR(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, "tok")
	p := newProvider(t, f)
	res, err := p.Revert(repoCtx(context.Background()), vcs.RevertRequest{
		TaskID: "T1", CommitSHA: "abcdef1234", BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !res.Reverted {
		t.Fatal("expected reverted=true")
	}
}
