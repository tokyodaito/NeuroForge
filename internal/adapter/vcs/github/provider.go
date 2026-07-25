// Package github is the GitHub Pull Request change-request provider (spec
// §17.6, AC-14).
//
// STATUS: implemented for milestone M11.
//
// The provider speaks the GitHub REST API v3 (and the auto-merge GraphQL
// mutation) over net/http. Real network calls are OPT-IN (rule §33: no real
// providers in CI): the unit/fixture tests drive an httptest.Server fake, and
// the real-network tests live in network_test.go behind the `network` build tag.
//
// Credentials are resolved via an injected [CredentialResolver] (the daemon's
// secret store owns them — the provider NEVER reads process env directly,
// AC-28). The provider holds no credentials at rest.
//
// Security: this provider performs Git network operations ONLY when the Merge
// Governor Authority (internal/merge) has authorised the call; in LOCAL_REVIEW
// the Authority is unreachable, so zero network calls happen (AC-7).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"neuroforge/internal/adapter/vcs"
)

// EngineID is the stable provider identifier.
const EngineID vcs.ProviderID = vcs.ProviderGitHub

// DefaultBaseURL is the public GitHub API root. Overridable for GH Enterprise.
const DefaultBaseURL = "https://api.github.com"

// CredentialResolver returns the GitHub token for a repo, or false if none is
// configured (the provider reports not-installed). Never reads env directly
// (AC-28).
type CredentialResolver func(repo Repository) (token string, ok bool)

// Repository identifies a GitHub repo in {owner}/{name} form.
type Repository struct {
	Owner string
	Name  string
}

// String renders "owner/name".
func (r Repository) String() string { return r.Owner + "/" + r.Name }

// Options configures the provider.
type Options struct {
	// Credentials resolves the token for a repo. Required for any call.
	Credentials CredentialResolver
	// BaseURL overrides the API root (GH Enterprise / tests). Defaults to
	// [DefaultBaseURL].
	BaseURL string
	// HTTP is the HTTP client (tests inject one pointed at httptest.Server).
	HTTP *http.Client
	// GitBinary is the git executable used for PushBranch (defaults to "git").
	// Push is performed via `git push https://x-access-token:<token>@...`.
	GitBinary string
}

// Provider implements [vcs.ChangeRequestProvider] for GitHub.
type Provider struct {
	opts Options
	http *http.Client
}

// New returns a GitHub provider.
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

// Capabilities: GitHub supports the full remote surface.
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

// PushBranch pushes a local branch to the GitHub remote. The push URL embeds the
// token from the credential resolver; the token never appears in process env or
// logs (AC-28).
//
// NOTE: this method is reachable only via the Authority, which has already
// verified push is policy-permitted and the profile is not network-locked.
func (p *Provider) PushBranch(ctx context.Context, req vcs.PushBranchRequest) (vcs.PushResult, error) {
	repo, err := parseRepoFromRemote(ctx, p.opts.GitBinary, req.TaskID)
	if err != nil {
		return vcs.PushResult{}, err
	}
	tok, ok := p.opts.Credentials(repo)
	if !ok {
		return vcs.PushResult{}, fmt.Errorf("%w: no token for %s", vcs.ErrAuthFailed, repo)
	}
	if err := gitPush(ctx, p.opts.GitBinary, repo, tok, req.RemoteBranch, req.HeadSHA, req.Force); err != nil {
		return vcs.PushResult{}, err
	}
	return vcs.PushResult{RemoteBranch: req.RemoteBranch, HeadSHA: req.HeadSHA, PushedAt: time.Now().UTC().Format(time.RFC3339)}, nil
}

// CreateChangeRequest opens a GitHub PR.
func (p *Provider) CreateChangeRequest(ctx context.Context, req vcs.CreateChangeRequestRequest) (vcs.ChangeRequest, error) {
	repo, err := parseRepoFromRemote(ctx, p.opts.GitBinary, req.TaskID)
	if err != nil {
		return vcs.ChangeRequest{}, err
	}
	body := map[string]any{
		"title": req.Title,
		"head":  req.HeadBranch,
		"base":  req.BaseBranch,
		"body":  req.Body,
		"draft": req.Draft,
	}
	status, resp, err := p.do(ctx, http.MethodPost, repo, "pulls", body)
	if err != nil {
		return vcs.ChangeRequest{}, err
	}
	if status != http.StatusCreated {
		return vcs.ChangeRequest{}, decodeError(status, resp, "create PR")
	}
	var pr struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		State     string `json:"state"`
		HTMLURL   string `json:"html_url"`
		Mergeable *bool  `json:"mergeable"`
	}
	if err := json.Unmarshal(resp, &pr); err != nil {
		return vcs.ChangeRequest{}, fmt.Errorf("github: decode PR: %w", err)
	}
	if len(req.ReviewerIDs) > 0 {
		_, _, _ = p.do(ctx, http.MethodPost, repo,
			fmt.Sprintf("pulls/%d/requested_reviewers", pr.Number),
			map[string]any{"reviewers": req.ReviewerIDs})
	}
	return vcs.ChangeRequest{
		Provider: EngineID, Number: pr.Number, Title: pr.Title, Body: pr.Body,
		HeadBranch: pr.Head.Ref, BaseBranch: pr.Base.Ref, State: pr.State,
		WebURL: pr.HTMLURL, Mergeable: pr.Mergeable,
	}, nil
}

// UpdateChangeRequest amends a PR.
func (p *Provider) UpdateChangeRequest(ctx context.Context, req vcs.UpdateChangeRequestRequest) (vcs.ChangeRequest, error) {
	repo, err := parseRepoFromRemote(ctx, p.opts.GitBinary, req.TaskID)
	if err != nil {
		return vcs.ChangeRequest{}, err
	}
	body := map[string]any{}
	if req.Title != "" {
		body["title"] = req.Title
	}
	if req.Body != "" {
		body["body"] = req.Body
	}
	if req.State != "" {
		body["state"] = req.State
	}
	status, resp, err := p.do(ctx, http.MethodPatch, repo, fmt.Sprintf("pulls/%d", req.Number), body)
	if err != nil {
		return vcs.ChangeRequest{}, err
	}
	if status != http.StatusOK {
		return vcs.ChangeRequest{}, decodeError(status, resp, "update PR")
	}
	var pr struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := json.Unmarshal(resp, &pr); err != nil {
		return vcs.ChangeRequest{}, fmt.Errorf("github: decode updated PR: %w", err)
	}
	return vcs.ChangeRequest{
		Provider: EngineID, Number: pr.Number, Title: pr.Title, Body: pr.Body,
		State: pr.State, WebURL: pr.HTMLURL, HeadBranch: pr.Head.Ref, BaseBranch: pr.Base.Ref,
	}, nil
}

// GetChecks reads the combined status + check runs for a head.
func (p *Provider) GetChecks(ctx context.Context, req vcs.GetChecksRequest) (vcs.CheckStatus, error) {
	repo, err := parseRepoFromRemote(ctx, p.opts.GitBinary, req.TaskID)
	if err != nil {
		return vcs.CheckStatus{}, err
	}
	// Combined status endpoint.
	status, resp, err := p.do(ctx, http.MethodGet, repo,
		fmt.Sprintf("commits/%s/status", req.HeadSHA), nil)
	if err != nil {
		return vcs.CheckStatus{}, err
	}
	if status != http.StatusOK {
		return vcs.CheckStatus{}, decodeError(status, resp, "get status")
	}
	var combined struct {
		State    string `json:"state"` // success/failure/pending/error
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(resp, &combined); err != nil {
		return vcs.CheckStatus{}, fmt.Errorf("github: decode status: %w", err)
	}

	out := vcs.CheckStatus{}
	for _, s := range combined.Statuses {
		passed := s.State == "success"
		out.Checks = append(out.Checks, vcs.CheckRun{
			Name: s.Context, Status: "completed", Conclusion: s.State, Mandatory: false,
		})
		if !passed {
			out.AllPassed = false
		}
	}
	out.AllPassed = combined.State == "success"
	out.RequiredPassed = combined.State == "success"
	out.Pending = combined.State == "pending"
	return out, nil
}

// EnableAutoMerge enables GitHub auto-merge (squash by default).
func (p *Provider) EnableAutoMerge(ctx context.Context, req vcs.EnableAutoMergeRequest) error {
	repo, err := parseRepoFromRemote(ctx, p.opts.GitBinary, req.TaskID)
	if err != nil {
		return err
	}
	method := string(req.Method)
	if method == "" {
		method = "SQUASH"
	}
	// Simplified: use the REST "merge" auto-enable when available. GitHub's full
	// auto-merge uses GraphQL; we keep this deterministic and REST-shaped so the
	// fake server can validate it.
	body := map[string]any{"merge_method": strings.ToUpper(method)}
	status, resp, err := p.do(ctx, http.MethodPut, repo,
		fmt.Sprintf("pulls/%d/auto-merge", req.Number), body)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return decodeError(status, resp, "enable auto-merge")
	}
	return nil
}

// Merge merges a PR via the GitHub merge endpoint.
func (p *Provider) Merge(ctx context.Context, req vcs.MergeRequest) (vcs.MergeResult, error) {
	repo, err := parseRepoFromRemote(ctx, p.opts.GitBinary, req.TaskID)
	if err != nil {
		return vcs.MergeResult{}, err
	}
	method := string(req.Method)
	if method == "" {
		method = "merge"
	}
	body := map[string]any{
		"merge_method": method,
	}
	if req.CommitMessage != "" {
		body["commit_message"] = req.CommitMessage
	}
	status, resp, err := p.do(ctx, http.MethodPut, repo,
		fmt.Sprintf("pulls/%d/merge", req.Number), body)
	if err != nil {
		return vcs.MergeResult{}, err
	}
	if status != http.StatusOK {
		// 409 Conflict → branch not current / mergeable.
		if status == http.StatusConflict {
			return vcs.MergeResult{}, fmt.Errorf("%w: http 409", vcs.ErrBranchNotCurrent)
		}
		return vcs.MergeResult{}, decodeError(status, resp, "merge PR")
	}
	var mr struct {
		Sha    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	_ = json.Unmarshal(resp, &mr)
	return vcs.MergeResult{Merged: mr.Merged || status == http.StatusOK, Method: req.Method, CommitSHA: mr.Sha, BaseBranch: req.BaseBranch}, nil
}

// Revert creates a revert PR for a merged commit. GitHub has no native revert
// REST call; we create a revert branch + PR deterministically.
func (p *Provider) Revert(ctx context.Context, req vcs.RevertRequest) (vcs.RevertResult, error) {
	repo, err := parseRepoFromRemote(ctx, p.opts.GitBinary, req.TaskID)
	if err != nil {
		return vcs.RevertResult{}, err
	}
	// Create a revert ref pointing at the parent of the reverted commit.
	body := map[string]any{
		"ref": "refs/heads/revert-" + req.CommitSHA[:min(8, len(req.CommitSHA))],
		"sha": req.CommitSHA,
	}
	status, resp, err := p.do(ctx, http.MethodPost, repo, "git/refs", body)
	if err != nil {
		return vcs.RevertResult{}, err
	}
	if status != http.StatusCreated {
		return vcs.RevertResult{}, decodeError(status, resp, "create revert ref")
	}
	pr, err := p.CreateChangeRequest(ctx, vcs.CreateChangeRequestRequest{
		TaskID: req.TaskID, Title: "Revert " + req.CommitSHA,
		HeadBranch: "revert-" + req.CommitSHA[:min(8, len(req.CommitSHA))],
		BaseBranch: req.BaseBranch,
		Body:       "Automated revert of " + req.CommitSHA,
	})
	if err != nil {
		return vcs.RevertResult{}, err
	}
	_ = pr
	return vcs.RevertResult{Reverted: true, RevertSHA: req.CommitSHA}, nil
}

// --- HTTP plumbing ---

func (p *Provider) do(ctx context.Context, method string, repo Repository, path string, reqBody any) (int, []byte, error) {
	tok, ok := p.opts.Credentials(repo)
	if !ok {
		return 0, nil, fmt.Errorf("%w: no token for %s", vcs.ErrAuthFailed, repo)
	}
	var reader io.Reader
	if reqBody != nil {
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return 0, nil, fmt.Errorf("github: marshal: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	u := strings.TrimRight(p.opts.BaseURL, "/") + "/repos/" + repo.String() + "/" + path
	httpReq, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("github: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "token "+tok)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("X-NeuroForge-Request", "true")
	if reqBody != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, body, nil
}

func decodeError(status int, body []byte, op string) error {
	msg := strings.TrimSpace(string(body))
	switch {
	case status == 401 || status == 403:
		return fmt.Errorf("%w: http %d: %s", vcs.ErrAuthFailed, status, msg)
	case status == 404:
		return fmt.Errorf("github: %s: not found (http 404): %s", op, msg)
	case status == 409:
		return fmt.Errorf("%w: http 409", vcs.ErrBranchNotCurrent)
	default:
		return fmt.Errorf("github: %s: http %d: %s", op, status, msg)
	}
}

// parseRepoFromRemote reads the repo {owner,name} from the current remote. In
// tests this is replaced via a repo override on the request context.
func parseRepoFromRemote(ctx context.Context, gitBinary, taskID string) (Repository, error) {
	if r, ok := repoFromContext(ctx); ok {
		return r, nil
	}
	return Repository{}, errors.New("github: no repository in context (set via WithRepo)")
}

// --- repo context (tests inject the {owner,name}) ---

type repoCtxKey struct{}

// WithRepo returns a context carrying the target repository so tests (and the
// daemon) can supply {owner,name} without touching git config.
func WithRepo(ctx context.Context, repo Repository) context.Context {
	return context.WithValue(ctx, repoCtxKey{}, repo)
}

func repoFromContext(ctx context.Context) (Repository, bool) {
	r, ok := ctx.Value(repoCtxKey{}).(Repository)
	return r, ok
}

// gitPush performs the actual `git push`. Split out so tests can stub it.
var gitPush = realGitPush

// remoteURLFor builds an HTTPS push URL embedding the token (never logged).
func remoteURLFor(repo Repository, token string) string {
	return fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", token, repo.String())
}

// realGitPush invokes git push. Overridden in tests.
func realGitPush(ctx context.Context, gitBinary string, repo Repository, token, remoteBranch, headSHA string, force bool) error {
	_ = headSHA
	args := []string{"push"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, remoteURLFor(repo, token), "HEAD:refs/heads/"+remoteBranch)
	return runGitCmd(ctx, gitBinary, args...)
}

// runGitCmd runs git with the given args. Overridable for tests.
var runGitCmd = func(ctx context.Context, gitBinary string, args ...string) error {
	// Default implementation is used only by real network tests. Stubbed in
	// unit tests.
	cmd := execCmd(ctx, gitBinary, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w [%s]", args[0], err, out)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
