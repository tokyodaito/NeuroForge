// Package gitlab is the GitLab Merge Request change-request provider (spec
// §17.6, AC-14).
//
// STATUS: implemented for milestone M11.
//
// The provider speaks the GitLab REST API v4 over net/http. Real network calls
// are OPT-IN (rule §33): fixture tests drive an httptest.Server fake, and the
// real-network tests live in network_test.go behind the `network` build tag.
//
// Credentials are resolved via an injected [CredentialResolver] (the daemon's
// secret store owns them — NEVER read from process env, AC-28). The provider
// holds no credentials at rest.
//
// Security: Git network operations run ONLY when the Merge Governor Authority
// has authorised the call; LOCAL_REVIEW never reaches this provider (AC-7).
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"neuroforge/internal/adapter/vcs"
)

// EngineID is the stable provider identifier.
const EngineID vcs.ProviderID = vcs.ProviderGitLab

// DefaultBaseURL is the public GitLab API root. Overridable for self-hosted.
const DefaultBaseURL = "https://gitlab.com/api/v4"

// CredentialResolver returns the GitLab token for a project, or false if none
// is configured. Never reads env directly (AC-28).
type CredentialResolver func(project Project) (token string, ok bool)

// Project identifies a GitLab project. Path is "group/project" URL-encoded.
type Project struct {
	Group string
	Name  string
}

// String renders the URL-encoded "group/project".
func (p Project) String() string {
	return url.PathEscape(p.Group + "/" + p.Name)
}

// Raw returns the un-encoded "group/project".
func (p Project) Raw() string { return p.Group + "/" + p.Name }

// Options configures the provider.
type Options struct {
	Credentials CredentialResolver
	BaseURL     string
	HTTP        *http.Client
	GitBinary   string
}

// Provider implements [vcs.ChangeRequestProvider] for GitLab.
type Provider struct {
	opts Options
	http *http.Client
}

// New returns a GitLab provider.
func New(opts Options) *Provider {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	hc := opts.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	if opts.GitBinary == "" {
		opts.GitBinary = "git"
	}
	return &Provider{opts: opts, http: hc}
}

// ID returns the provider id.
func (p *Provider) ID() vcs.ProviderID { return EngineID }

// Capabilities: GitLab supports the full remote surface.
func (p *Provider) Capabilities() vcs.Capabilities {
	return vcs.Capabilities{
		PushBranch:          true,
		CreateChangeRequest: true,
		UpdateChangeRequest: true,
		GetChecks:           true,
		EnableAutoMerge:     true,
		Merge:               true,
		Revert:              true,
		IsNetwork:           true,
	}
}

// PushBranch pushes a local branch to the GitLab remote.
func (p *Provider) PushBranch(ctx context.Context, req vcs.PushBranchRequest) (vcs.PushResult, error) {
	proj, err := projectFromContext(ctx)
	if err != nil {
		return vcs.PushResult{}, err
	}
	tok, ok := p.opts.Credentials(proj)
	if !ok {
		return vcs.PushResult{}, fmt.Errorf("%w: no token for %s", vcs.ErrAuthFailed, proj.Raw())
	}
	if err := gitPush(ctx, p.opts.GitBinary, proj, tok, req.RemoteBranch); err != nil {
		return vcs.PushResult{}, err
	}
	return vcs.PushResult{RemoteBranch: req.RemoteBranch, HeadSHA: req.HeadSHA, PushedAt: time.Now().UTC().Format(time.RFC3339)}, nil
}

// CreateChangeRequest opens a GitLab MR.
func (p *Provider) CreateChangeRequest(ctx context.Context, req vcs.CreateChangeRequestRequest) (vcs.ChangeRequest, error) {
	proj, err := projectFromContext(ctx)
	if err != nil {
		return vcs.ChangeRequest{}, err
	}
	form := url.Values{}
	form.Set("title", req.Title)
	form.Set("source_branch", req.HeadBranch)
	form.Set("target_branch", req.BaseBranch)
	form.Set("description", req.Body)
	if req.Draft {
		form.Set("draft", "true")
	}
	status, resp, err := p.doForm(ctx, http.MethodPost, proj, "merge_requests", form)
	if err != nil {
		return vcs.ChangeRequest{}, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return vcs.ChangeRequest{}, decodeError(status, resp, "create MR")
	}
	var mr struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		State        string `json:"state"`
		WebURL       string `json:"web_url"`
		MergeStatus  string `json:"detailed_merge_status"`
	}
	if err := json.Unmarshal(resp, &mr); err != nil {
		return vcs.ChangeRequest{}, fmt.Errorf("gitlab: decode MR: %w", err)
	}
	mergeable := mr.MergeStatus == "mergeable" || mr.MergeStatus == "can_be_merged"
	return vcs.ChangeRequest{
		Provider: EngineID, Number: mr.IID, Title: mr.Title, Body: mr.Description,
		HeadBranch: mr.SourceBranch, BaseBranch: mr.TargetBranch, State: mr.State,
		WebURL: mr.WebURL, Mergeable: &mergeable,
	}, nil
}

// UpdateChangeRequest amends an MR.
func (p *Provider) UpdateChangeRequest(ctx context.Context, req vcs.UpdateChangeRequestRequest) (vcs.ChangeRequest, error) {
	proj, err := projectFromContext(ctx)
	if err != nil {
		return vcs.ChangeRequest{}, err
	}
	form := url.Values{}
	if req.Title != "" {
		form.Set("title", req.Title)
	}
	if req.Body != "" {
		form.Set("description", req.Body)
	}
	if req.State != "" {
		// GitLab uses state_event=close/reopen.
		if req.State == "closed" {
			form.Set("state_event", "close")
		} else if req.State == "open" {
			form.Set("state_event", "reopen")
		}
	}
	status, resp, err := p.doForm(ctx, http.MethodPut, proj,
		fmt.Sprintf("merge_requests/%d", req.Number), form)
	if err != nil {
		return vcs.ChangeRequest{}, err
	}
	if status != http.StatusOK {
		return vcs.ChangeRequest{}, decodeError(status, resp, "update MR")
	}
	var mr struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		State        string `json:"state"`
		WebURL       string `json:"web_url"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
	}
	if err := json.Unmarshal(resp, &mr); err != nil {
		return vcs.ChangeRequest{}, fmt.Errorf("gitlab: decode updated MR: %w", err)
	}
	return vcs.ChangeRequest{
		Provider: EngineID, Number: mr.IID, Title: mr.Title,
		State: mr.State, WebURL: mr.WebURL, HeadBranch: mr.SourceBranch, BaseBranch: mr.TargetBranch,
	}, nil
}

// GetChecks reads the latest pipeline status for a head SHA.
func (p *Provider) GetChecks(ctx context.Context, req vcs.GetChecksRequest) (vcs.CheckStatus, error) {
	proj, err := projectFromContext(ctx)
	if err != nil {
		return vcs.CheckStatus{}, err
	}
	status, resp, err := p.do(ctx, http.MethodGet, proj,
		fmt.Sprintf("projects/%s/repository/commits/%s/statuses", url.PathEscape(proj.Raw()), req.HeadSHA), nil)
	if err != nil {
		return vcs.CheckStatus{}, err
	}
	if status != http.StatusOK {
		return vcs.CheckStatus{}, decodeError(status, resp, "get statuses")
	}
	var statuses []struct {
		Name         string `json:"name"`
		Status       string `json:"status"` // pending/running/success/failed/canceled
		AllowFailure bool   `json:"allow_failure"`
	}
	if err := json.Unmarshal(resp, &statuses); err != nil {
		return vcs.CheckStatus{}, fmt.Errorf("gitlab: decode statuses: %w", err)
	}
	out := vcs.CheckStatus{}
	for _, s := range statuses {
		conclusion := s.Status
		if conclusion == "success" {
			conclusion = "success"
		}
		out.Checks = append(out.Checks, vcs.CheckRun{
			Name: s.Name, Status: "completed", Conclusion: conclusion, Mandatory: !s.AllowFailure,
		})
		if s.Status == "pending" || s.Status == "running" {
			out.Pending = true
		}
		if (s.Status == "failed" || s.Status == "canceled") && !s.AllowFailure {
			out.AllPassed = false
			out.RequiredPassed = false
		}
	}
	if !out.Pending && len(statuses) > 0 {
		allOK := true
		for _, s := range statuses {
			if s.Status != "success" && !s.AllowFailure {
				allOK = false
			}
		}
		out.AllPassed = allOK
		out.RequiredPassed = allOK
	}
	return out, nil
}

// EnableAutoMerge sets merge_when_pipeline_succeeds on the MR.
func (p *Provider) EnableAutoMerge(ctx context.Context, req vcs.EnableAutoMergeRequest) error {
	proj, err := projectFromContext(ctx)
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("merge_when_pipeline_succeeds", "true")
	status, resp, err := p.doForm(ctx, http.MethodPut, proj,
		fmt.Sprintf("merge_requests/%d/merge", req.Number), form)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return decodeError(status, resp, "enable auto-merge")
	}
	return nil
}

// Merge accepts (merges) a GitLab MR.
func (p *Provider) Merge(ctx context.Context, req vcs.MergeRequest) (vcs.MergeResult, error) {
	proj, err := projectFromContext(ctx)
	if err != nil {
		return vcs.MergeResult{}, err
	}
	form := url.Values{}
	form.Set("should_remove_source_branch", "false")
	form.Set("sha", req.HeadSHA)
	status, resp, err := p.doForm(ctx, http.MethodPut, proj,
		fmt.Sprintf("merge_requests/%d/merge", req.Number), form)
	if err != nil {
		return vcs.MergeResult{}, err
	}
	if status != http.StatusOK {
		if status == http.StatusConflict || status == http.StatusUnprocessableEntity {
			return vcs.MergeResult{}, fmt.Errorf("%w: http %d", vcs.ErrBranchNotCurrent, status)
		}
		return vcs.MergeResult{}, decodeError(status, resp, "merge MR")
	}
	var mr struct {
		MergeCommitSHA string `json:"merge_commit_sha"`
		State          string `json:"state"`
	}
	_ = json.Unmarshal(resp, &mr)
	return vcs.MergeResult{Merged: mr.State == "merged", Method: req.Method, CommitSHA: mr.MergeCommitSHA, BaseBranch: req.BaseBranch}, nil
}

// Revert creates a revert MR. GitLab has a native revert endpoint.
func (p *Provider) Revert(ctx context.Context, req vcs.RevertRequest) (vcs.RevertResult, error) {
	proj, err := projectFromContext(ctx)
	if err != nil {
		return vcs.RevertResult{}, err
	}
	form := url.Values{}
	form.Set("sha", req.CommitSHA)
	status, resp, err := p.doForm(ctx, http.MethodPost, proj,
		fmt.Sprintf("repository/commits/%s/revert", req.CommitSHA), form)
	if err != nil {
		return vcs.RevertResult{}, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return vcs.RevertResult{}, decodeError(status, resp, "revert")
	}
	return vcs.RevertResult{Reverted: true, RevertSHA: req.CommitSHA}, nil
}

// --- HTTP plumbing ---

func (p *Provider) do(ctx context.Context, method string, proj Project, path string, reqBody any) (int, []byte, error) {
	tok, ok := p.opts.Credentials(proj)
	if !ok {
		return 0, nil, fmt.Errorf("%w: no token for %s", vcs.ErrAuthFailed, proj.Raw())
	}
	u := strings.TrimRight(p.opts.BaseURL, "/") + "/" + path
	var reader io.Reader
	if reqBody != nil {
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return 0, nil, fmt.Errorf("gitlab: marshal: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	return p.send(ctx, method, u, tok, "application/json", reader)
}

func (p *Provider) doForm(ctx context.Context, method string, proj Project, path string, form url.Values) (int, []byte, error) {
	tok, ok := p.opts.Credentials(proj)
	if !ok {
		return 0, nil, fmt.Errorf("%w: no token for %s", vcs.ErrAuthFailed, proj.Raw())
	}
	u := strings.TrimRight(p.opts.BaseURL, "/") + "/projects/" + proj.String() + "/" + path
	return p.send(ctx, method, u, tok, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
}

func (p *Provider) send(ctx context.Context, method, u, tok, contentType string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, nil, fmt.Errorf("gitlab: build request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", tok)
	req.Header.Set("Content-Type", contentType)
	resp, err := p.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, b, nil
}

func decodeError(status int, body []byte, op string) error {
	msg := strings.TrimSpace(string(body))
	switch {
	case status == 401 || status == 403:
		return fmt.Errorf("%w: http %d: %s", vcs.ErrAuthFailed, status, msg)
	case status == 404:
		return fmt.Errorf("gitlab: %s: not found (http 404): %s", op, msg)
	case status == 409, status == 422:
		return fmt.Errorf("%w: http %d", vcs.ErrBranchNotCurrent, status)
	default:
		return fmt.Errorf("gitlab: %s: http %d: %s", op, status, msg)
	}
}

// --- project context (tests inject the {group,name}) ---

type projCtxKey struct{}

// WithProject returns a context carrying the target project.
func WithProject(ctx context.Context, proj Project) context.Context {
	return context.WithValue(ctx, projCtxKey{}, proj)
}

func projectFromContext(ctx context.Context) (Project, error) {
	if p, ok := ctx.Value(projCtxKey{}).(Project); ok {
		return p, nil
	}
	return Project{}, errors.New("gitlab: no project in context (set via WithProject)")
}

// gitPush performs the actual `git push`. Overridable for tests.
var gitPush = realGitPush

func remoteURLFor(proj Project, token string) string {
	return fmt.Sprintf("https://oauth2:%s@gitlab.com/%s.git", token, proj.Raw())
}

func realGitPush(ctx context.Context, gitBinary string, proj Project, token, remoteBranch string) error {
	return runGitCmd(ctx, gitBinary, "push", remoteURLFor(proj, token), "HEAD:refs/heads/"+remoteBranch)
}

var runGitCmd = func(ctx context.Context, gitBinary string, args ...string) error {
	cmd := execCmd(ctx, gitBinary, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w [%s]", args[0], err, out)
	}
	return nil
}
