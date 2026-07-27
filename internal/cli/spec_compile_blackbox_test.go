package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSpecCompile_BlackBox_DeterministicOutput proves the deterministic task
// compiler (M14-02) is reachable from the production binary and produces
// deterministic output (engineering baseline §2: black-box evidence).
//
// Scenario:
//  1. `forge spec compile --project work-app "<text>"` (twice, with identical
//     input) emits two byte-identical JSON documents.
//  2. The compiled specification carries the structured fields the caller
//     would persist (TaskID, Objective, Risk, Complexity, AcceptanceCriteria
//     with stable ids AC-1/AC-2, plus Confidence).
//  3. The compiled specification is unlocked and has Version=0 (the compiler
//     never claims to mutate or version an existing spec).
func TestSpecCompile_BlackBox_DeterministicOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	desc := "Objective: Add a retry button to the network error screen.\n" +
		"Acceptance Criteria:\n" +
		"- Button is shown when a network error occurs.\n" +
		"- Clicking retry re-submits within 500ms.\n" +
		"Non-goals:\n" +
		"- Redesigning the error screen.\n" +
		"Constraints:\n" +
		"- No new third-party dependencies."

	out1, _, code := runForge(t, bin, home, "spec", "compile", "--json",
		"--project", "work-app", "--title", "Add retry button", desc)
	if code != 0 {
		t.Fatalf("spec compile: exit %d out=%s", code, out1)
	}

	out2, _, code := runForge(t, bin, home, "spec", "compile", "--json",
		"--project", "work-app", "--title", "Add retry button", desc)
	if code != 0 {
		t.Fatalf("spec compile (2): exit %d out=%s", code, out2)
	}

	if out1 != out2 {
		t.Fatalf("non-deterministic output across two runs:\nout1=%s\nout2=%s", out1, out2)
	}

	var doc struct {
		Result struct {
			Specification struct {
				TaskID             string `json:"TaskID"`
				Version            int    `json:"Version"`
				Objective          string `json:"Objective"`
				Risk               string `json:"Risk"`
				Complexity         string `json:"Complexity"`
				AcceptanceCriteria []struct {
					ID        string `json:"ID"`
					Statement string `json:"Statement"`
				} `json:"AcceptanceCriteria"`
				NonGoals    []string `json:"NonGoals"`
				Constraints []string `json:"Constraints"`
				Locked      bool     `json:"Locked"`
			} `json:"Specification"`
			Confidence string `json:"Confidence"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out1), &doc); err != nil {
		t.Fatalf("parse compile output: %v\nout=%s", err, out1)
	}
	r := doc.Result
	if r.Specification.TaskID != "work-app-compiled" {
		t.Fatalf("TaskID = %q, want work-app-compiled", r.Specification.TaskID)
	}
	if r.Specification.Version != 0 {
		t.Fatalf("Version = %d, want 0 (compiler must not assign a version)", r.Specification.Version)
	}
	if r.Specification.Locked {
		t.Fatal("freshly compiled spec must NOT be locked")
	}
	if !strings.Contains(r.Specification.Objective, "retry button") {
		t.Fatalf("objective lost: %q", r.Specification.Objective)
	}
	if len(r.Specification.AcceptanceCriteria) != 2 {
		t.Fatalf("ACs = %d, want 2", len(r.Specification.AcceptanceCriteria))
	}
	if r.Specification.AcceptanceCriteria[0].ID != "AC-1" ||
		r.Specification.AcceptanceCriteria[1].ID != "AC-2" {
		t.Fatalf("AC ids not stable: %+v", r.Specification.AcceptanceCriteria)
	}
	if r.Confidence != "HIGH" {
		t.Fatalf("confidence = %q, want HIGH (structured input)", r.Confidence)
	}
	if len(r.Specification.NonGoals) != 1 || len(r.Specification.Constraints) != 1 {
		t.Fatalf("non-goals/constraints lost: ng=%d c=%d",
			len(r.Specification.NonGoals), len(r.Specification.Constraints))
	}
}

// TestSpecCompile_BlackBox_VagueInputLowConfidence proves the compiler surfaces
// LOW confidence + a clarification when the input is too vague, instead of
// inventing a fake HIGH-confidence specification (baseline rule 10).
func TestSpecCompile_BlackBox_VagueInputLowConfidence(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	out, _, code := runForge(t, bin, home, "spec", "compile", "--json", "--project", "p", "fix it")
	if code != 0 {
		t.Fatalf("spec compile: exit %d out=%s", code, out)
	}
	var doc struct {
		Result struct {
			Confidence     string `json:"Confidence"`
			Clarifications []struct {
				Question string `json:"Question"`
				Reason   string `json:"Reason"`
			} `json:"Clarifications"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("parse compile output: %v\nout=%s", err, out)
	}
	if doc.Result.Confidence != "LOW" {
		t.Fatalf("confidence = %q, want LOW", doc.Result.Confidence)
	}
	if len(doc.Result.Clarifications) == 0 {
		t.Fatal("vague input must surface at least one clarification")
	}
}

// TestSpecCompile_BlackBox_AttachmentMetadata proves the compiler consumes
// attachment metadata as input (without reading content) and propagates visual
// requirements when a DESIGN_REFERENCE attachment is supplied.
func TestSpecCompile_BlackBox_AttachmentMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	out, _, code := runForge(t, bin, home, "spec", "compile", "--json",
		"--project", "p",
		"--attach", "sha256:deadbeef=DESIGN_REFERENCE",
		"Implement the profile header per the attached mock.")
	if code != 0 {
		t.Fatalf("spec compile: exit %d out=%s", code, out)
	}
	var doc struct {
		Result struct {
			Specification struct {
				VisualRequirements struct {
					Required   bool     `json:"Required"`
					References []string `json:"References"`
				} `json:"VisualRequirements"`
			} `json:"Specification"`
			AttachmentRoles map[string]string `json:"AttachmentRoles"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("parse compile output: %v\nout=%s", err, out)
	}
	if !doc.Result.Specification.VisualRequirements.Required {
		t.Fatal("DESIGN_REFERENCE attachment must mark visual requirements as required")
	}
	if len(doc.Result.Specification.VisualRequirements.References) != 1 ||
		doc.Result.Specification.VisualRequirements.References[0] != "sha256:deadbeef" {
		t.Fatalf("design reference hash not propagated: %v",
			doc.Result.Specification.VisualRequirements.References)
	}
	if doc.Result.AttachmentRoles["sha256:deadbeef"] != "DESIGN_REFERENCE" {
		t.Fatalf("attachment role mapping lost: %v", doc.Result.AttachmentRoles)
	}
}

// TestSpecCompile_BlackBox_RiskyTaskFlagsClarification proves the R4-safe-
// assumption rule (§9.7) is observable through the compiled binary: an
// auth-related task must surface a Clarification even when structured sections
// are present.
func TestSpecCompile_BlackBox_RiskyTaskFlagsClarification(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	out, _, code := runForge(t, bin, home, "spec", "compile", "--json",
		"--project", "p",
		"Objective: Rotate OAuth client secrets and invalidate sessions.\nAcceptance Criteria:\n- Rotation completes without downtime.")
	if code != 0 {
		t.Fatalf("spec compile: exit %d out=%s", code, out)
	}
	var doc struct {
		Result struct {
			Specification struct {
				Risk string `json:"Risk"`
			} `json:"Specification"`
			Clarifications []struct {
				Question string `json:"Question"`
			} `json:"Clarifications"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("parse compile output: %v\nout=%s", err, out)
	}
	if doc.Result.Specification.Risk != "R4" {
		t.Fatalf("risk = %q, want R4", doc.Result.Specification.Risk)
	}
	if len(doc.Result.Clarifications) == 0 {
		t.Fatal("R4 task must surface at least one clarification")
	}
}

// TestSpecCompile_BlackBox_MissingProject proves the CLI rejects a missing
// --project / --task with a non-zero exit code (contract enforcement is
// observable through the binary).
func TestSpecCompile_BlackBox_MissingProject(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	_, stderr, code := runForge(t, bin, home, "spec", "compile", "fix it")
	if code == 0 {
		t.Fatal("missing --project must exit non-zero")
	}
	if !strings.Contains(stderr, "project") {
		t.Fatalf("stderr should mention --project, got: %s", stderr)
	}
}

// TestSpecCompile_BlackBox_EmptyInput proves the CLI rejects empty input
// (no description AND no attachment).
func TestSpecCompile_BlackBox_EmptyInput(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	_, stderr, code := runForge(t, bin, home, "spec", "compile", "--project", "p")
	if code == 0 {
		t.Fatal("empty input must exit non-zero")
	}
	if !strings.Contains(stderr, "description or attachment is required") {
		t.Fatalf("stderr should mention the empty-input contract, got: %s", stderr)
	}
}

// TestSpecCompile_BlackBox_AttachmentOnlyWithFilename is the MAJOR-1 regression
// test at the binary level. The extended --attach grammar
// (hash=ROLE:filename:mimeType:size) must propagate filename + mimeType + size
// into the compiled specification, and the attachment-only task must produce a
// non-degenerate objective (no empty "()" clause) plus LOW confidence +
// clarification. This pins the review fix end-to-end through the production CLI.
func TestSpecCompile_BlackBox_AttachmentOnlyWithFilename(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	// Attachment-only: no description, just metadata.
	out, _, code := runForge(t, bin, home, "spec", "compile", "--json",
		"--project", "p",
		"--attach", "sha256:feedface=REQUIREMENTS:requirements.md:text/markdown:512")
	if code != 0 {
		t.Fatalf("spec compile: exit %d out=%s", code, out)
	}
	var doc struct {
		Result struct {
			Specification struct {
				Objective string `json:"Objective"`
			} `json:"Specification"`
			Confidence     string `json:"Confidence"`
			Clarifications []struct {
				Question string `json:"Question"`
				Reason   string `json:"Reason"`
			} `json:"Clarifications"`
			AttachmentRoles map[string]string `json:"AttachmentRoles"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("parse compile output: %v\nout=%s", err, out)
	}
	r := doc.Result
	obj := r.Specification.Objective
	if obj == "" {
		t.Fatal("attachment-only task must synthesise an objective")
	}
	// MAJOR-1 headline assertion: the degenerate "()" must be gone, and the
	// filename must appear in the placeholder objective.
	if strings.Contains(obj, "()") {
		t.Fatalf("degenerate empty-paren objective via CLI: %q", obj)
	}
	if !strings.Contains(obj, "requirements.md") {
		t.Fatalf("filename not propagated into objective: %q", obj)
	}
	if r.Confidence != "LOW" {
		t.Fatalf("confidence = %q, want LOW (attachment-only, content not read)", r.Confidence)
	}
	if len(r.Clarifications) == 0 {
		t.Fatal("attachment-only task must surface a clarification")
	}
	if r.AttachmentRoles["sha256:feedface"] != "REQUIREMENTS" {
		t.Fatalf("attachment role lost: %v", r.AttachmentRoles)
	}
}

// TestSpecCompile_BlackBox_AttachmentOnlyLegacyHashRole proves the legacy
// `hash=ROLE` form (no filename) still works after the MAJOR-1 grammar
// extension and produces a valid (LOW + clarification) specification, with the
// defensive non-degenerate objective. Backward compatibility is preserved.
func TestSpecCompile_BlackBox_AttachmentOnlyLegacyHashRole(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	out, _, code := runForge(t, bin, home, "spec", "compile", "--json",
		"--project", "p",
		"--attach", "sha256:abc=REQUIREMENTS")
	if code != 0 {
		t.Fatalf("spec compile: exit %d out=%s", code, out)
	}
	var doc struct {
		Result struct {
			Specification struct {
				Objective string `json:"Objective"`
			} `json:"Specification"`
			Confidence string `json:"Confidence"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("parse compile output: %v\nout=%s", err, out)
	}
	r := doc.Result
	if strings.Contains(r.Specification.Objective, "()") {
		t.Fatalf("legacy hash=ROLE form produced degenerate objective: %q",
			r.Specification.Objective)
	}
	if r.Confidence != "LOW" {
		t.Fatalf("confidence = %q, want LOW", r.Confidence)
	}
}

// TestSpecCompile_BlackBox_TextOutput is the MINOR-1 regression test: the
// --json=false text formatter must produce human-readable output containing
// TaskID, Objective, AC IDs, and Confidence. Before this fix the text path
// had zero test coverage.
func TestSpecCompile_BlackBox_TextOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	// Default output mode is text (--json opt-in since MINOR-2).
	out, _, code := runForge(t, bin, home, "spec", "compile",
		"--project", "work-app",
		"Objective: Add a retry button.\nAcceptance Criteria:\n- Button renders.\n- Retry re-submits within 500ms.")
	if code != 0 {
		t.Fatalf("spec compile: exit %d out=%s", code, out)
	}
	// The text formatter is not JSON; assert the human-readable shape.
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("default output should be text, got JSON: %s", out)
	}
	mustContain := []string{
		"TaskID:",
		"work-app-compiled",
		"Objective:",
		"retry button",
		"AC-1",
		"AC-2",
		"Confidence:",
		"HIGH",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("text output missing %q:\n%s", want, out)
		}
	}
}

// TestSpecCompile_BlackBox_InvalidPriorityRejected is the MINOR-3 regression
// test: an unknown --priority value must exit non-zero with a clear error,
// matching the --attach role-validation behaviour.
func TestSpecCompile_BlackBox_InvalidPriorityRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	_, stderr, code := runForge(t, bin, home, "spec", "compile",
		"--project", "p", "--priority", "BOGUS", "fix it")
	if code == 0 {
		t.Fatal("invalid --priority must exit non-zero")
	}
	if !strings.Contains(stderr, "--priority") {
		t.Fatalf("stderr should mention --priority, got: %s", stderr)
	}
	if !strings.Contains(stderr, "BOGUS") {
		t.Fatalf("stderr should echo the bad value, got: %s", stderr)
	}
}

// TestSpecCompile_BlackBox_InvalidAttachRoleRejected proves the --attach role
// validation is observable through the binary (the role-validation predated
// MAJOR-1 but had no black-box coverage).
func TestSpecCompile_BlackBox_InvalidAttachRoleRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	_, stderr, code := runForge(t, bin, home, "spec", "compile",
		"--project", "p", "--attach", "sha256:abc=NOT_A_ROLE", "fix it")
	if code == 0 {
		t.Fatal("invalid --attach role must exit non-zero")
	}
	if !strings.Contains(stderr, "--attach") {
		t.Fatalf("stderr should mention --attach, got: %s", stderr)
	}
}

// TestSpecCompile_BlackBox_InvalidAttachSizeRejected proves the --attach size
// field (part of the MAJOR-1 grammar extension) is validated: a non-numeric
// size is rejected at the CLI surface.
func TestSpecCompile_BlackBox_InvalidAttachSizeRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test spawns the compiled forge binary")
	}
	bin := forgeBinary(t)
	home := t.TempDir()

	_, stderr, code := runForge(t, bin, home, "spec", "compile",
		"--project", "p",
		"--attach", "sha256:abc=REQUIREMENTS:req.md:text/markdown:not-a-number",
		"fix it")
	if code == 0 {
		t.Fatal("non-numeric --attach size must exit non-zero")
	}
	if !strings.Contains(stderr, "--attach") {
		t.Fatalf("stderr should mention --attach, got: %s", stderr)
	}
}
