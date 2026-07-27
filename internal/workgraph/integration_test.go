package workgraph_test

import (
	"bytes"
	"testing"

	"neuroforge/internal/task"
	"neuroforge/internal/workgraph"
)

// Integration-level evidence (baseline rule 1: exemption-eligible tasks that
// touch no production wiring still need ≥1 integration-level passing evidence).
//
// This file proves the two M14 layers compose end-to-end at the library level:
// the M14-02 deterministic Task Compiler's output (task.Specification) feeds
// cleanly into the M14-04 work-graph Decomposer, and the resulting graph is
// valid, deterministic, and covers every acceptance criterion the compiler
// produced. No daemon, no CLI, no storage is involved — M14-04 is explicitly
// scoped to "domain model, not execution wiring".

// TestIntegration_CompileThenDecompose is the cross-package integration probe:
// task.Compile (M14-02) → workgraph.Decompose (M14-04) → ValidateAgainstSpec,
// asserting every compiler-produced AC is owned by exactly one package and the
// marshalled graph is byte-stable across repeated decomposition.
func TestIntegration_CompileThenDecompose(t *testing.T) {
	input := task.CompileInput{
		TaskID:   "TASK-INT",
		Title:    "Add retry button",
		Priority: task.PriorityHigh,
		Description: "Objective: Add a retry button to the login form.\n" +
			"Acceptance Criteria:\n" +
			"- The retry button is visible on the login screen.\n" +
			"- Clicking it retries the last failed login attempt.\n" +
			"- After 3 failed retries, the button is disabled for 30s.",
	}
	result, err := task.Compile(input)
	if err != nil {
		t.Fatalf("task.Compile: %v", err)
	}
	spec, err := task.ValidateSpecification(result.Specification)
	if err != nil {
		t.Fatalf("ValidateSpecification: %v", err)
	}
	if len(spec.AcceptanceCriteria) < 2 {
		t.Fatalf("expected ≥2 ACs from compiler, got %d (%+v)", len(spec.AcceptanceCriteria), spec.AcceptanceCriteria)
	}

	// Decompose the compiler output.
	v1, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("workgraph.Decompose: %v", err)
	}

	// Every compiler-produced AC must be owned by exactly one package.
	for _, ac := range spec.AcceptanceCriteria {
		pkg, ok := v1.PackageForAC(ac.ID)
		if !ok {
			t.Fatalf("AC %q not owned by any package", ac.ID)
		}
		// Exactly-one ownership: re-scan and confirm no second owner.
		owners := 0
		for _, p := range v1.Packages() {
			for _, id := range p.AcceptedACIDs {
				if id == ac.ID {
					owners++
				}
			}
		}
		if owners != 1 {
			t.Errorf("AC %q owned by %d packages, want 1", ac.ID, owners)
		}
		// Every package is linked to the objective.
		if pkg.Objective == "" {
			t.Errorf("package %q has empty objective", pkg.ID)
		}
	}

	// Determinism: re-decompose and compare bytes.
	b1, err := workgraph.MarshalValidated(v1)
	if err != nil {
		t.Fatalf("MarshalValidated: %v", err)
	}
	v2, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("workgraph.Decompose #2: %v", err)
	}
	b2, err := workgraph.MarshalValidated(v2)
	if err != nil {
		t.Fatalf("MarshalValidated #2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("decomposition not deterministic across runs:\n%s\n%s", b1, b2)
	}

	// The round-tripped marshalled graph must re-validate against the same spec.
	rt, err := workgraph.UnmarshalWorkGraph(b1)
	if err != nil {
		t.Fatalf("UnmarshalWorkGraph: %v", err)
	}
	if _, err := workgraph.ValidateAgainstSpec(rt, spec); err != nil {
		t.Fatalf("round-tripped graph failed re-validation: %v", err)
	}
}

// TestIntegration_CompileVagueInputDecomposesSafely proves that a LOW-confidence
// compiler output (vague input → synthesised default AC) still decomposes into a
// valid work graph rather than producing an invalid runnable. This guards the
// "invalid DAG cannot become runnable" AC at the integration boundary.
func TestIntegration_CompileVagueInputDecomposesSafely(t *testing.T) {
	input := task.CompileInput{
		TaskID:      "TASK-VAGUE",
		Description: "fix it", // deliberately vague
	}
	result, err := task.Compile(input)
	if err != nil {
		t.Fatalf("task.Compile: %v", err)
	}
	spec, verr := task.ValidateSpecification(result.Specification)
	if verr != nil {
		// A vague input may yield a spec that fails validation (no ACs / empty
		// objective). In that case Decompose must ALSO refuse — it must never
		// produce an invalid runnable. This is the honest contract.
		if _, derr := workgraph.Decompose(result.Specification); derr == nil {
			t.Fatalf("spec failed ValidateSpecification (%v) but Decompose accepted it — invalid DAG became runnable", verr)
		}
		t.Logf("vague input correctly rejected by ValidateSpecification (%v) and Decompose (%v) — no invalid runnable", verr, verr)
		return
	}
	// If the spec did validate, Decompose must succeed and produce a valid graph.
	v, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("workgraph.Decompose on valid vague-derived spec: %v", err)
	}
	if len(v.Packages()) < 1 {
		t.Fatalf("expected ≥1 package, got 0")
	}
}
