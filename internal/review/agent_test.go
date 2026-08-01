package review

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func testRequest() ReviewRequest {
	return ReviewRequest{
		Diff:         "diff --git a/main.go b/main.go\n+deleted_line()\n",
		ChangedFiles: []string{"main.go"},
		Context:      "spec: deletes are forbidden here",
	}
}

func TestAgentReviewer_ParsesCleanArray(t *testing.T) {
	r := NewAgentReviewer(func(_ context.Context, _ string) (string, error) {
		return `[]`, nil
	}, AgentReviewerOptions{})
	findings, err := r.Review(context.Background(), RoleCorrectness, testRequest())
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
}

func TestAgentReviewer_ParsesFindingsAndNormalizes(t *testing.T) {
	r := NewAgentReviewer(func(_ context.Context, _ string) (string, error) {
		return `Here is my review:
` + "```json" + `
[
  {"severity":"BLOCKER","title":" nil deref ","description":"crashes","file":"main.go","line":-3,"remediation":"check nil"},
  {"severity":"weird","title":"style","description":"nit","file":"main.go","line":10,"remediation":"rename"}
]
` + "```" + `
Done.`, nil
	}, AgentReviewerOptions{})
	findings, err := r.Review(context.Background(), RoleSecurity, testRequest())
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("len(findings) = %d, want 2", len(findings))
	}
	f0 := findings[0]
	if f0.Role != RoleSecurity || f0.Severity != SeverityBlocker || f0.Line != 0 || f0.Title != "nil deref" {
		t.Fatalf("finding[0] not normalized: %+v", f0)
	}
	if findings[1].Severity != SeverityInfo {
		t.Fatalf("unknown severity = %q, want info", findings[1].Severity)
	}
}

func TestAgentReviewer_UsesLastJSONArray(t *testing.T) {
	r := NewAgentReviewer(func(_ context.Context, _ string) (string, error) {
		return `The format looks like [{"severity":"info","title":"example"}] but my real answer is:
[{"severity":"major","title":"real finding","description":"d","file":"f.go","line":1,"remediation":"fix"}]`, nil
	}, AgentReviewerOptions{})
	findings, err := r.Review(context.Background(), RoleCorrectness, testRequest())
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != 1 || findings[0].Title != "real finding" {
		t.Fatalf("findings = %+v, want the last array's finding", findings)
	}
}

func TestAgentReviewer_EmptyOutputIsClean(t *testing.T) {
	r := NewAgentReviewer(func(_ context.Context, _ string) (string, error) {
		return "  \n\t ", nil
	}, AgentReviewerOptions{})
	findings, err := r.Review(context.Background(), RoleCorrectness, testRequest())
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
}

func TestAgentReviewer_UnparseableOutputWrapsSentinel(t *testing.T) {
	r := NewAgentReviewer(func(_ context.Context, _ string) (string, error) {
		return "I could not review this, sorry.", nil
	}, AgentReviewerOptions{})
	_, err := r.Review(context.Background(), RoleCorrectness, testRequest())
	if !errors.Is(err, ErrUnparseableReview) {
		t.Fatalf("err = %v, want wrapping ErrUnparseableReview", err)
	}
}

func TestAgentReviewer_InvalidJSONInsideArrayWrapsSentinel(t *testing.T) {
	r := NewAgentReviewer(func(_ context.Context, _ string) (string, error) {
		return `[{not valid json}]`, nil
	}, AgentReviewerOptions{})
	_, err := r.Review(context.Background(), RoleCorrectness, testRequest())
	if !errors.Is(err, ErrUnparseableReview) {
		t.Fatalf("err = %v, want wrapping ErrUnparseableReview", err)
	}
}

func TestAgentReviewer_RunErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	r := NewAgentReviewer(func(_ context.Context, _ string) (string, error) {
		return "", boom
	}, AgentReviewerOptions{})
	_, err := r.Review(context.Background(), RoleCorrectness, testRequest())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapping boom", err)
	}
	if errors.Is(err, ErrUnparseableReview) {
		t.Fatal("run errors must not be classified as unparseable output")
	}
}

func TestAgentReviewer_FindingsCapped(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < maxFindings+10; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"severity":"minor","title":"t"}`)
	}
	sb.WriteString("]")
	r := NewAgentReviewer(func(_ context.Context, _ string) (string, error) {
		return sb.String(), nil
	}, AgentReviewerOptions{})
	findings, err := r.Review(context.Background(), RoleCorrectness, testRequest())
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(findings) != maxFindings {
		t.Fatalf("len(findings) = %d, want cap %d", len(findings), maxFindings)
	}
}

func TestAgentReviewer_PromptContents(t *testing.T) {
	var gotPrompt string
	r := NewAgentReviewer(func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "[]", nil
	}, AgentReviewerOptions{})
	if _, err := r.Review(context.Background(), RoleArchitecture, testRequest()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	for _, want := range []string{
		"ARCHITECTURE",            // role instruction
		"do not modify any files", // review-only constraint
		"main.go",                 // changed file
		"deleted_line()",          // diff body
		"deletes are forbidden",   // context
		`"severity"`,              // output contract
		"[]",                      // clean-response contract
	} {
		if !strings.Contains(gotPrompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, gotPrompt)
		}
	}
}

func TestAgentReviewer_DiffTruncation(t *testing.T) {
	big := strings.Repeat("x\n", 100) // 200 bytes
	var gotPrompt string
	r := NewAgentReviewer(func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "[]", nil
	}, AgentReviewerOptions{MaxDiffBytes: 50})
	if _, err := r.Review(context.Background(), RoleCorrectness, ReviewRequest{Diff: big}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !strings.Contains(gotPrompt, "diff truncated at 50 bytes") {
		t.Fatalf("prompt missing truncation notice:\n%s", gotPrompt)
	}
	if strings.Contains(gotPrompt, big) {
		t.Fatal("prompt contains the full untruncated diff")
	}
}

func TestTruncateDiff_CutsOnLineBoundary(t *testing.T) {
	got := truncateDiff("aaa\nbbb\nccc\n", 5)
	if !strings.HasPrefix(got, "aaa\n") {
		t.Fatalf("truncateDiff cut mid-line: %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("truncateDiff missing notice: %q", got)
	}
}

func TestTruncateDiff_Disabled(t *testing.T) {
	big := strings.Repeat("y", 1000)
	if got := truncateDiff(big, -1); got != big {
		t.Fatal("negative MaxDiffBytes must disable truncation")
	}
	if got := truncateDiff(big, 1000); got != big {
		t.Fatal("diff within the limit must pass through unchanged")
	}
}
