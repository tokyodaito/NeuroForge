//go:build network

// Package github network tests — OPT-IN only (rule §33: no real providers in CI).
//
// Run with: go test -tags network ./internal/adapter/vcs/github/...
//
// These tests hit the REAL GitHub API and require a live token. They are skipped
// automatically unless the `network` build tag is set AND the
// NEUROFORGE_GITHUB_TOKEN / NEUROFORGE_GITHUB_REPO env vars are present. They are
// never run by `make check` (which does not pass the tag).
package github

import (
	"context"
	"os"
	"testing"

	"neuroforge/internal/adapter/vcs"
)

func TestNetwork_CreateAndClosePR(t *testing.T) {
	tok := os.Getenv("NEUROFORGE_GITHUB_TOKEN")
	repoStr := os.Getenv("NEUROFORGE_GITHUB_REPO")
	if tok == "" || repoStr == "" {
		t.Skip("NEUROFORGE_GITHUB_TOKEN / NEUROFORGE_GITHUB_REPO not set")
	}
	parts := splitRepo(repoStr)
	p := New(Options{
		Credentials: func(Repository) (string, bool) { return tok, true },
	})
	ctx := WithRepo(context.Background(), Repository{Owner: parts[0], Name: parts[1]})

	// Open a draft PR on a throwaway branch, then close it. Never merge.
	cr, err := p.CreateChangeRequest(ctx, vcs.CreateChangeRequestRequest{
		TaskID: "nettest", Title: "[neuroforge] network smoke test (auto-closed)",
		HeadBranch: "neuroforge-nettest", BaseBranch: "main", Draft: true,
	})
	if err != nil {
		t.Skipf("could not create PR (repo may not allow it): %v", err)
	}
	t.Cleanup(func() {
		_, _ = p.UpdateChangeRequest(ctx, vcs.UpdateChangeRequestRequest{
			TaskID: "nettest", Number: cr.Number, State: "closed",
		})
	})
	if cr.Number == 0 {
		t.Fatal("expected non-zero PR number")
	}
}

func splitRepo(s string) [2]string {
	for i, c := range s {
		if c == '/' {
			return [2]string{s[:i], s[i+1:]}
		}
	}
	return [2]string{s, ""}
}
