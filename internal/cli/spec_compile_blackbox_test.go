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

	out1, _, code := runForge(t, bin, home, "spec", "compile",
		"--project", "work-app", "--title", "Add retry button", desc)
	if code != 0 {
		t.Fatalf("spec compile: exit %d out=%s", code, out1)
	}

	out2, _, code := runForge(t, bin, home, "spec", "compile",
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

	out, _, code := runForge(t, bin, home, "spec", "compile", "--project", "p", "fix it")
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

	out, _, code := runForge(t, bin, home, "spec", "compile",
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

	out, _, code := runForge(t, bin, home, "spec", "compile",
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
