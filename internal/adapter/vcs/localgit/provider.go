// Package localgit is the Local Git change-request provider (spec §17.5, AC-10).
//
// STATUS: implemented for milestone M11.
//
// Unlike the GitHub/GitLab providers, Local Git performs NO Git network
// operations: it integrates a result branch into the user's current checkout
// via merge / squash / cherry-pick / apply-patch (§17.5 "Accept into current
// branch"). This is the only component besides the workspace manager that
// touches the user's primary checkout, and only when the user explicitly
// accepts/merges a result through the Authority (agents never hold a provider
// handle — §17.1, AC-28).
//
// §17.5 preconditions enforced before any write:
//  1. verify the user's checkout is clean of conflicting changes;
//  2. create a backup reference (refs/forge/backup/<task>);
//  3. perform the action;
//  4. return the result.
package localgit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"neuroforge/internal/adapter/vcs"
)

// EngineID is the stable provider identifier.
const EngineID vcs.ProviderID = vcs.ProviderLocalGit

// Provider implements [vcs.ChangeRequestProvider] for local Git (§17.5).
type Provider struct {
	// CheckoutPath is the user's primary checkout (the merge target).
	CheckoutPath string
	// GitPath overrides the git binary (defaults to "git").
	GitPath string
	// Now is the clock used for backup-ref naming (tests inject a fixed clock).
	Now func() time.Time
}

// New returns a Provider rooted at the user's checkout.
func New(checkoutPath string) *Provider {
	return &Provider{
		CheckoutPath: checkoutPath,
		GitPath:      "git",
		Now:          func() time.Time { return time.Now().UTC() },
	}
}

// ID returns the provider id.
func (p *Provider) ID() vcs.ProviderID { return EngineID }

// Capabilities: Local Git supports local merge + revert only. It is NOT a
// network provider (AC-7).
func (p *Provider) Capabilities() vcs.Capabilities {
	return vcs.Capabilities{
		Merge:  true,
		Revert: true,
		// PushBranch / CreateChangeRequest / UpdateChangeRequest / GetChecks /
		// EnableAutoMerge are remote concepts — local-git does not implement
		// them. Callers asking for them get ErrUnsupported.
		IsNetwork: false,
	}
}

// PushBranch is unsupported locally (no remote). AC-7: this never performs a
// network operation.
func (p *Provider) PushBranch(ctx context.Context, req vcs.PushBranchRequest) (vcs.PushResult, error) {
	return vcs.PushResult{}, vcs.Unsupported(EngineID, "PushBranch")
}

// CreateChangeRequest is unsupported locally.
func (p *Provider) CreateChangeRequest(ctx context.Context, req vcs.CreateChangeRequestRequest) (vcs.ChangeRequest, error) {
	return vcs.ChangeRequest{}, vcs.Unsupported(EngineID, "CreateChangeRequest")
}

// UpdateChangeRequest is unsupported locally.
func (p *Provider) UpdateChangeRequest(ctx context.Context, req vcs.UpdateChangeRequestRequest) (vcs.ChangeRequest, error) {
	return vcs.ChangeRequest{}, vcs.Unsupported(EngineID, "UpdateChangeRequest")
}

// GetChecks is unsupported locally (the test engine handles local checks).
func (p *Provider) GetChecks(ctx context.Context, req vcs.GetChecksRequest) (vcs.CheckStatus, error) {
	return vcs.CheckStatus{}, vcs.Unsupported(EngineID, "GetChecks")
}

// EnableAutoMerge is unsupported locally.
func (p *Provider) EnableAutoMerge(ctx context.Context, req vcs.EnableAutoMergeRequest) error {
	return vcs.Unsupported(EngineID, "EnableAutoMerge")
}

// Merge integrates a result branch into the current branch of the user's
// checkout (§17.5). The requested Method selects merge / squash / cherry-pick /
// patch.
//
// Pre-write safety (§17.5):
//  1. Refuse if the checkout has uncommitted conflicting changes.
//  2. Create a backup reference at the current HEAD.
//  3. Apply the action.
func (p *Provider) Merge(ctx context.Context, req vcs.MergeRequest) (vcs.MergeResult, error) {
	if p.CheckoutPath == "" {
		return vcs.MergeResult{}, errors.New("localgit: checkout path is required")
	}
	if req.HeadBranch == "" {
		return vcs.MergeResult{}, errors.New("localgit: HeadBranch is required")
	}

	// 1. Verify the checkout is clean (no conflicting uncommitted changes).
	if dirty, err := p.hasConflictingChanges(ctx); err != nil {
		return vcs.MergeResult{}, err
	} else if dirty {
		return vcs.MergeResult{}, errors.New("localgit: checkout has uncommitted changes; refusing to merge (§17.5)")
	}

	// Resolve the current branch as the merge target.
	baseBefore, err := p.revParse(ctx, "HEAD")
	if err != nil {
		return vcs.MergeResult{}, err
	}
	currentBranch, err := p.currentBranch(ctx)
	if err != nil {
		return vcs.MergeResult{}, err
	}
	target := req.BaseBranch
	if target == "" {
		target = currentBranch
	}

	// 2. Create a backup reference so the user can undo (§17.5 step 4).
	backupRef := p.backupRef(req.TaskID, baseBefore)
	if err := p.updateRef(ctx, backupRef, baseBefore); err != nil {
		return vcs.MergeResult{}, fmt.Errorf("localgit: create backup ref: %w", err)
	}

	// 3. Apply the requested integration method.
	switch req.Method {
	case vcs.MergeMethodMerge:
		sha, err := p.doMerge(ctx, req.HeadBranch, req.CommitMessage)
		if err != nil {
			return vcs.MergeResult{}, err
		}
		return vcs.MergeResult{Merged: true, Method: req.Method, CommitSHA: sha, BaseBranch: target}, nil

	case vcs.MergeMethodSquash:
		sha, err := p.doSquash(ctx, req.HeadBranch, req.CommitMessage)
		if err != nil {
			return vcs.MergeResult{}, err
		}
		return vcs.MergeResult{Merged: true, Method: req.Method, CommitSHA: sha, BaseBranch: target}, nil

	case vcs.MergeMethodCherryPick:
		sha, err := p.doCherryPick(ctx, req.HeadBranch, baseBefore)
		if err != nil {
			return vcs.MergeResult{}, err
		}
		return vcs.MergeResult{Merged: true, Method: req.Method, CommitSHA: sha, BaseBranch: target}, nil

	case vcs.MergeMethodPatch:
		if err := p.doPatch(ctx, req.HeadBranch, baseBefore); err != nil {
			return vcs.MergeResult{}, err
		}
		sha, err := p.revParse(ctx, "HEAD")
		if err != nil {
			return vcs.MergeResult{}, err
		}
		return vcs.MergeResult{Merged: true, Method: req.Method, CommitSHA: sha, BaseBranch: target}, nil

	case vcs.MergeMethodRebase:
		sha, err := p.doRebase(ctx, req.HeadBranch)
		if err != nil {
			return vcs.MergeResult{}, err
		}
		return vcs.MergeResult{Merged: true, Method: req.Method, CommitSHA: sha, BaseBranch: target}, nil

	default:
		return vcs.MergeResult{}, fmt.Errorf("localgit: unsupported merge method %q", req.Method)
	}
}

// Revert undoes a merged change by creating a revert commit on the current
// branch.
func (p *Provider) Revert(ctx context.Context, req vcs.RevertRequest) (vcs.RevertResult, error) {
	if p.CheckoutPath == "" {
		return vcs.RevertResult{}, errors.New("localgit: checkout path is required")
	}
	if req.CommitSHA == "" {
		return vcs.RevertResult{}, errors.New("localgit: CommitSHA is required")
	}
	if dirty, err := p.hasConflictingChanges(ctx); err != nil {
		return vcs.RevertResult{}, err
	} else if dirty {
		return vcs.RevertResult{}, errors.New("localgit: checkout has uncommitted changes; refusing to revert (§17.5)")
	}
	if out, err := p.run(ctx, "revert", "--no-edit", req.CommitSHA); err != nil {
		return vcs.RevertResult{}, fmt.Errorf("localgit: revert %s: %w [%s]", req.CommitSHA, err, out)
	}
	sha, err := p.revParse(ctx, "HEAD")
	if err != nil {
		return vcs.RevertResult{}, err
	}
	return vcs.RevertResult{Reverted: true, RevertSHA: sha}, nil
}

// --- git helpers ---

// run executes a git command in the checkout. Unlike the workspace manager's
// allowlist runner, local-git legitimately needs merge/cherry-pick/revert/apply
// (these are local operations, never network). It explicitly forbids every
// network subcommand as defense-in-depth (AC-7).
func (p *Provider) run(ctx context.Context, args ...string) (string, error) {
	for _, a := range args {
		if isNetworkSub(a) {
			return "", fmt.Errorf("%w: localgit refuses %q", vcs.ErrNetworkLocked, a)
		}
	}
	gitPath := p.GitPath
	if gitPath == "" {
		gitPath = "git"
	}
	full := append([]string{"-C", p.CheckoutPath}, args...)
	cmd := exec.CommandContext(ctx, gitPath, full...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(ee.Stderr), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

var networkSubs = map[string]bool{
	"push": true, "fetch": true, "pull": true, "clone": true,
	"ls-remote": true, "send-pack": true, "fetch-pack": true,
}

func isNetworkSub(a string) bool { return networkSubs[a] }

func (p *Provider) revParse(ctx context.Context, ref string) (string, error) {
	out, err := p.run(ctx, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (p *Provider) currentBranch(ctx context.Context) (string, error) {
	out, err := p.run(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (p *Provider) updateRef(ctx context.Context, ref, sha string) error {
	_, err := p.run(ctx, "update-ref", ref, sha)
	return err
}

// hasConflictingChanges reports whether the working tree has staged or unstaged
// tracked changes that would conflict with a merge (§17.5 step 1).
func (p *Provider) hasConflictingChanges(ctx context.Context) (bool, error) {
	out, err := p.run(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (p *Provider) backupRef(taskID, sha string) string {
	seg := strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == ':' {
			return '-'
		}
		return r
	}, taskID)
	if seg == "" {
		seg = "x"
	}
	short := sha
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("refs/forge/backup/%s-%s", seg, short)
}

func (p *Provider) doMerge(ctx context.Context, branch, msg string) (string, error) {
	args := []string{"merge", "--no-ff"}
	if msg != "" {
		args = append(args, "-m", msg)
	}
	args = append(args, branch)
	if out, err := p.run(ctx, args...); err != nil {
		return "", fmt.Errorf("localgit: merge %s: %w [%s]", branch, err, out)
	}
	return p.revParse(ctx, "HEAD")
}

func (p *Provider) doSquash(ctx context.Context, branch, msg string) (string, error) {
	if msg == "" {
		msg = fmt.Sprintf("Squash merge of %s", branch)
	}
	if out, err := p.run(ctx, "merge", "--squash", branch); err != nil {
		return "", fmt.Errorf("localgit: squash %s: %w [%s]", branch, err, out)
	}
	if out, err := p.run(ctx, "commit", "-m", msg); err != nil {
		return "", fmt.Errorf("localgit: squash commit: %w [%s]", err, out)
	}
	return p.revParse(ctx, "HEAD")
}

func (p *Provider) doCherryPick(ctx context.Context, branch, baseBefore string) (string, error) {
	// Cherry-pick the range baseBefore..branch (the commits the result branch
	// added). Equivalent to: git cherry-pick baseBefore..branch
	if out, err := p.run(ctx, "cherry-pick", baseBefore+".."+branch); err != nil {
		// Abort on failure to leave the checkout clean.
		_, _ = p.run(ctx, "cherry-pick", "--abort")
		return "", fmt.Errorf("localgit: cherry-pick %s..%s: %w [%s]", baseBefore, branch, err, out)
	}
	return p.revParse(ctx, "HEAD")
}

func (p *Provider) doPatch(ctx context.Context, branch, baseBefore string) error {
	// git diff baseBefore..branch | git apply --3way — produces working-tree
	// changes the user can review; committed here as a single patch commit.
	diffOut, err := p.run(ctx, "diff", baseBefore+".."+branch)
	if err != nil {
		return fmt.Errorf("localgit: patch diff: %w", err)
	}
	if strings.TrimSpace(diffOut) == "" {
		return errors.New("localgit: patch is empty")
	}
	// Write the diff to a temp file and apply it.
	tmp := filepath.Join(p.CheckoutPath, ".git", "forge-patch.diff")
	if err := writePatchFile(tmp, diffOut); err != nil {
		return fmt.Errorf("localgit: write patch: %w", err)
	}
	if out, err := p.run(ctx, "apply", "--3way", tmp); err != nil {
		return fmt.Errorf("localgit: apply patch: %w [%s]", err, out)
	}
	if out, err := p.run(ctx, "add", "-A"); err != nil {
		return fmt.Errorf("localgit: patch add: %w [%s]", err, out)
	}
	if out, err := p.run(ctx, "commit", "-m", "Apply patch from "+branch); err != nil {
		return fmt.Errorf("localgit: patch commit: %w [%s]", err, out)
	}
	return nil
}

func (p *Provider) doRebase(ctx context.Context, branch string) (string, error) {
	// Rebase the current branch onto the result branch tip is not the §17.5
	// semantics; for local merge we fast-forward when possible.
	if out, err := p.run(ctx, "merge", "--ff-only", branch); err != nil {
		return "", fmt.Errorf("localgit: fast-forward %s: %w [%s]", branch, err, out)
	}
	return p.revParse(ctx, "HEAD")
}

// writePatchFile writes content to path, creating parent directories as needed.
func writePatchFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
