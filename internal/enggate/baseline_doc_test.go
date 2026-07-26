package enggate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the repository root derived from the test working directory
// (internal/enggate -> ../..).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root lookup failed (no go.mod at %s): %v", root, err)
	}
	return root
}

// TestActiveBaselinePathExists is an integration-level regression (baseline §1/§9):
// the ActiveBaselinePath constant must reference a real file in the repository,
// and that file must declare the active baseline version. If the doc is renamed
// or the version bumped without updating the constant, this test fails.
func TestActiveBaselinePathExists(t *testing.T) {
	root := repoRoot(t)
	doc := filepath.Join(root, ActiveBaselinePath)
	data, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("active baseline doc %s missing: %v (constant out of sync with repo)", ActiveBaselinePath, err)
	}
	s := string(data)
	if !strings.Contains(s, "Version: 1") {
		t.Errorf("baseline doc must declare 'Version: 1'")
	}
	if !strings.Contains(s, "baseline_version") {
		t.Errorf("baseline doc must define the manifest baseline_version field")
	}
}

// TestAgentsMandatesBaseline is an integration-level regression (baseline §1):
// AGENTS.md must reference the baseline document and mark it MANDATORY, so every
// coding agent entering the repository is routed to it. Removing the link
// silently would break the "baseline automatically available" criterion.
func TestAgentsMandatesBaseline(t *testing.T) {
	root := repoRoot(t)
	agents := filepath.Join(root, "AGENTS.md")
	data, err := os.ReadFile(agents)
	if err != nil {
		t.Fatalf("AGENTS.md missing: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, ActiveBaselinePath) {
		t.Errorf("AGENTS.md must link the baseline at %s", ActiveBaselinePath)
	}
	if !strings.Contains(s, "MANDATORY") {
		t.Errorf("AGENTS.md must mark the baseline MANDATORY")
	}
	if !strings.Contains(s, "forge gate") {
		t.Errorf("AGENTS.md must document the `forge gate` enforcement command")
	}
}

// TestBaselineDefinesEvidenceLevelsAndHonesty is an integration-level regression
// (baseline §2/§9): the baseline must define the four evidence levels and the
// documentation-honesty rule, so the evidence model and the "no production
// readiness claim without proof" rule are present in the source of truth.
func TestBaselineDefinesEvidenceLevelsAndHonesty(t *testing.T) {
	root := repoRoot(t)
	doc := filepath.Join(root, ActiveBaselinePath)
	data, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, lvl := range []string{"unit", "integration", "blackbox", "live"} {
		if !strings.Contains(s, lvl) {
			t.Errorf("baseline doc must define evidence level %q", lvl)
		}
	}
	if !strings.Contains(s, "Documentation honesty") {
		t.Errorf("baseline doc must contain the §9 Documentation honesty section")
	}
	if !strings.Contains(s, "actor") || !strings.Contains(s, "separation") {
		t.Errorf("baseline doc must define the actor-separation rule")
	}
}
