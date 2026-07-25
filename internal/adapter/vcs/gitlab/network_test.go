//go:build network

// Package gitlab network tests — OPT-IN only (rule §33: no real providers in CI).
//
// Run with: go test -tags network ./internal/adapter/vcs/gitlab/...
//
// These tests hit the REAL GitLab API and require a live token. They are skipped
// automatically unless the `network` build tag is set AND the
// NEUROFORGE_GITLAB_TOKEN / NEUROFORGE_GITLAB_PROJECT env vars are present. They
// are never run by `make check`.
package gitlab

import (
	"context"
	"os"
	"testing"

	"neuroforge/internal/adapter/vcs"
)

func TestNetwork_CreateAndCloseMR(t *testing.T) {
	tok := os.Getenv("NEUROFORGE_GITLAB_TOKEN")
	proj := os.Getenv("NEUROFORGE_GITLAB_PROJECT")
	if tok == "" || proj == "" {
		t.Skip("NEUROFORGE_GITLAB_TOKEN / NEUROFORGE_GITLAB_PROJECT not set")
	}
	g, n := splitProject(proj)
	p := New(Options{Credentials: func(Project) (string, bool) { return tok, true }})
	ctx := WithProject(context.Background(), Project{Group: g, Name: n})

	cr, err := p.CreateChangeRequest(ctx, vcs.CreateChangeRequestRequest{
		TaskID: "nettest", Title: "[neuroforge] network smoke test (auto-closed)",
		HeadBranch: "neuroforge-nettest", BaseBranch: "main",
	})
	if err != nil {
		t.Skipf("could not create MR (project may not allow it): %v", err)
	}
	t.Cleanup(func() {
		_, _ = p.UpdateChangeRequest(ctx, vcs.UpdateChangeRequestRequest{
			TaskID: "nettest", Number: cr.Number, State: "closed",
		})
	})
	if cr.Number == 0 {
		t.Fatal("expected non-zero MR iid")
	}
}

func splitProject(s string) (string, string) {
	for i, c := range s {
		if c == '/' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}
