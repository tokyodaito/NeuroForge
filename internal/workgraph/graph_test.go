package workgraph_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"neuroforge/internal/task"
	"neuroforge/internal/workgraph"
)

// helperAC builds an AcceptanceCriterion.
func helperAC(id, stmt string) task.AcceptanceCriterion {
	return task.AcceptanceCriterion{ID: id, Statement: stmt}
}

// helperSpec builds a minimal valid specification for the given ACs.
func helperSpec(taskID, objective string, acs ...task.AcceptanceCriterion) task.Specification {
	return task.Specification{
		TaskID:             taskID,
		Objective:          objective,
		AcceptanceCriteria: acs,
		Risk:               task.RiskR1,
		Complexity:         task.ComplexityC1,
	}
}

// helperPkg builds a WorkPackage with sane defaults.
func helperPkg(id string, acs ...string) workgraph.WorkPackage {
	return workgraph.WorkPackage{
		ID:            id,
		TaskID:        "TASK-1",
		Stage:         workgraph.StageImplementation,
		Title:         "Pkg " + id,
		Objective:     "Do the thing.",
		AcceptedACIDs: acs,
		State:         workgraph.PackagePending,
	}
}

// ---- Stage / PackageState ----

func TestStage_IsValid(t *testing.T) {
	for _, s := range workgraph.AllStages {
		if !s.IsValid() {
			t.Errorf("stage %q in AllStages must be valid", s)
		}
	}
	for _, bad := range []workgraph.Stage{"", "RESEARCH", "build", "random"} {
		if bad.IsValid() {
			t.Errorf("stage %q should be invalid", bad)
		}
	}
}

func TestPackageState_IsValidAndTerminal(t *testing.T) {
	for _, s := range workgraph.AllPackageStates {
		if !s.IsValid() {
			t.Errorf("state %q in AllPackageStates must be valid", s)
		}
	}
	wantTerminal := map[workgraph.PackageState]bool{
		workgraph.PackageSucceeded: true,
		workgraph.PackageFailed:    true,
		workgraph.PackageSkipped:   true,
		workgraph.PackagePending:   false,
		workgraph.PackageReady:     false,
		workgraph.PackageRunning:   false,
		workgraph.PackageBlocked:   false,
	}
	for s, want := range wantTerminal {
		if got := s.IsTerminal(); got != want {
			t.Errorf("state %q IsTerminal=%v want %v", s, got, want)
		}
	}
}

// ---- Valid simple DAG (single package) ----

func TestValidate_SimpleSinglePackage(t *testing.T) {
	g := workgraph.WorkGraph{
		TaskID:   "TASK-1",
		Packages: []workgraph.WorkPackage{helperPkg("TASK-1-AC-1", "AC-1")},
	}
	v, err := workgraph.ValidateWorkGraph(g)
	if err != nil {
		t.Fatalf("ValidateWorkGraph: unexpected error: %v", err)
	}
	if v.TaskID() != "TASK-1" {
		t.Errorf("TaskID() = %q want TASK-1", v.TaskID())
	}
	if got := v.Packages(); len(got) != 1 || got[0].ID != "TASK-1-AC-1" {
		t.Errorf("Packages() = %+v want one package TASK-1-AC-1", got)
	}
	topo, err := v.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder: %v", err)
	}
	if len(topo) != 1 || topo[0] != "TASK-1-AC-1" {
		t.Errorf("TopologicalOrder = %v want [TASK-1-AC-1]", topo)
	}
}

// ---- Valid simple DAG (linear chain: P1 -> P2 -> P3) ----

func TestValidate_LinearChain(t *testing.T) {
	p1 := helperPkg("P1", "AC-1")
	p2 := helperPkg("P2", "AC-2")
	p2.Dependencies = []string{"P1"}
	p3 := helperPkg("P3", "AC-3")
	p3.Dependencies = []string{"P2"}
	g := workgraph.WorkGraph{TaskID: "TASK-1", Packages: []workgraph.WorkPackage{p1, p2, p3}}

	v, err := workgraph.ValidateWorkGraph(g)
	if err != nil {
		t.Fatalf("ValidateWorkGraph: %v", err)
	}
	topo, err := v.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder: %v", err)
	}
	want := []string{"P1", "P2", "P3"}
	if !equalStringSlices(topo, want) {
		t.Errorf("TopologicalOrder = %v want %v", topo, want)
	}
}

// ---- Valid composite DAG (diamond: A -> {B, C} -> D) ----

func TestValidate_CompositeDiamond(t *testing.T) {
	a := helperPkg("A", "AC-1")
	b := helperPkg("B", "AC-2")
	b.Dependencies = []string{"A"}
	c := helperPkg("C", "AC-3")
	c.Dependencies = []string{"A"}
	d := helperPkg("D", "AC-4")
	d.Dependencies = []string{"B", "C"}
	g := workgraph.WorkGraph{TaskID: "TASK-1", Packages: []workgraph.WorkPackage{a, b, c, d}}

	v, err := workgraph.ValidateWorkGraph(g)
	if err != nil {
		t.Fatalf("ValidateWorkGraph: %v", err)
	}
	topo, err := v.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder: %v", err)
	}
	// A must come before B and C; B and C before D. Exact B/C order is by ID.
	if !topoPrecedes(topo, "A", "B") || !topoPrecedes(topo, "A", "C") {
		t.Errorf("A must precede B and C; topo=%v", topo)
	}
	if !topoPrecedes(topo, "B", "D") || !topoPrecedes(topo, "C", "D") {
		t.Errorf("B and C must precede D; topo=%v", topo)
	}
	if len(topo) != 4 {
		t.Errorf("topo len = %d want 4", len(topo))
	}

	// AC ownership lookup.
	if pkg, ok := v.PackageForAC("AC-3"); !ok || pkg.ID != "C" {
		t.Errorf("PackageForAC(AC-3) = %q,%v want C,true", pkg.ID, ok)
	}
	if _, ok := v.PackageForAC("AC-999"); ok {
		t.Errorf("PackageForAC(AC-999) should be false")
	}
}

// ---- Graph().clone immutability ----

func TestValidatedGraph_GraphIsDefensiveCopy(t *testing.T) {
	g := workgraph.WorkGraph{
		TaskID:   "TASK-1",
		Packages: []workgraph.WorkPackage{helperPkg("P1", "AC-1")},
	}
	v, err := workgraph.ValidateWorkGraph(g)
	if err != nil {
		t.Fatalf("ValidateWorkGraph: %v", err)
	}
	got := v.Graph()
	got.Packages[0].AcceptedACIDs[0] = "MUTATED"
	again := v.Graph()
	if again.Packages[0].AcceptedACIDs[0] != "AC-1" {
		t.Errorf("Graph() did not return a defensive copy: mutation leaked into validated state: %q", again.Packages[0].AcceptedACIDs[0])
	}
}

// ---- Decompose: deterministic, AC coverage, ownership ----

func TestDecompose_DeterministicFromSpec(t *testing.T) {
	spec := helperSpec("TASK-D", "Build the feature.",
		helperAC("AC-1", "First AC statement."),
		helperAC("AC-2", "Second AC statement."),
		helperAC("AC-3", "Third AC statement."),
	)
	v1, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("Decompose #1: %v", err)
	}
	v2, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("Decompose #2: %v", err)
	}
	// Structural equality via canonical JSON.
	j1, _ := workgraph.MarshalValidated(v1)
	j2, _ := workgraph.MarshalValidated(v2)
	if !equalBytes(j1, j2) {
		t.Errorf("Decompose not deterministic:\nfirst:  %s\nsecond: %s", j1, j2)
	}
	// Cross-run byte-identity (10 runs).
	first := string(j1)
	for i := 0; i < 9; i++ {
		vn, err := workgraph.Decompose(spec)
		if err != nil {
			t.Fatalf("Decompose run %d: %v", i, err)
		}
		jn, _ := workgraph.MarshalValidated(vn)
		if string(jn) != first {
			t.Fatalf("Decompose run %d produced different bytes:\n%s", i, jn)
		}
	}
}

func TestDecompose_ACOverageAndChainShape(t *testing.T) {
	spec := helperSpec("TASK-D", "Build the feature.",
		helperAC("AC-1", "First."),
		helperAC("AC-2", "Second."),
		helperAC("AC-3", "Third."),
	)
	v, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	pkgs := v.Packages()
	if len(pkgs) != 3 {
		t.Fatalf("Decompose produced %d packages want 3", len(pkgs))
	}
	// IDs derived from (taskID, AC ID), deterministic.
	wantIDs := []string{"TASK-D-AC-1", "TASK-D-AC-2", "TASK-D-AC-3"}
	gotIDs := make([]string, 3)
	for i, p := range pkgs {
		gotIDs[i] = p.ID
	}
	if !equalStringSlices(gotIDs, wantIDs) {
		t.Errorf("package IDs = %v want %v", gotIDs, wantIDs)
	}
	// Each package owns exactly one distinct AC; every AC covered.
	for _, p := range pkgs {
		if len(p.AcceptedACIDs) != 1 {
			t.Errorf("package %q owns %d ACs want 1", p.ID, len(p.AcceptedACIDs))
		}
		if p.Objective != spec.Objective {
			t.Errorf("package %q objective = %q want %q", p.ID, p.Objective, spec.Objective)
		}
		if p.Stage != workgraph.StageImplementation {
			t.Errorf("package %q stage = %q want implementation", p.ID, p.Stage)
		}
		if p.State != workgraph.PackagePending {
			t.Errorf("package %q state = %q want pending", p.ID, p.State)
		}
	}
	// Chain shape: P[i] depends on P[i-1]; P[0] has no deps.
	if len(pkgs[0].Dependencies) != 0 {
		t.Errorf("pkgs[0] deps = %v want none", pkgs[0].Dependencies)
	}
	for i := 1; i < len(pkgs); i++ {
		if len(pkgs[i].Dependencies) != 1 || pkgs[i].Dependencies[0] != pkgs[i-1].ID {
			t.Errorf("pkgs[%d] deps = %v want [%s]", i, pkgs[i].Dependencies, pkgs[i-1].ID)
		}
	}
}

func TestDecompose_SingleACSpec(t *testing.T) {
	spec := helperSpec("TASK-S", "Do one thing.", helperAC("AC-1", "The only AC."))
	v, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(v.Packages()) != 1 {
		t.Fatalf("Decompose single-AC produced %d packages want 1", len(v.Packages()))
	}
}

func TestDecompose_RejectsInvalidSpec(t *testing.T) {
	// Empty objective -> spec invalid -> Decompose errors.
	spec := task.Specification{
		TaskID: "TASK-X",
		AcceptanceCriteria: []task.AcceptanceCriterion{
			helperAC("AC-1", "stmt"),
		},
	}
	if _, err := workgraph.Decompose(spec); err == nil {
		t.Errorf("Decompose with empty objective should error")
	}
	// Duplicate AC IDs -> spec invalid.
	spec2 := task.Specification{
		TaskID:    "TASK-X",
		Objective: "obj",
		AcceptanceCriteria: []task.AcceptanceCriterion{
			helperAC("AC-1", "stmt"),
			helperAC("AC-1", "dup"),
		},
	}
	if _, err := workgraph.Decompose(spec2); err == nil {
		t.Errorf("Decompose with duplicate AC IDs should error")
	}
}

// ---- ValidateAgainstSpec: coverage + existence checks ----

func TestValidateAgainstSpec_HappyPath(t *testing.T) {
	spec := helperSpec("TASK-1", "obj", helperAC("AC-1", "a"), helperAC("AC-2", "b"))
	p1 := helperPkg("P1", "AC-1")
	p2 := helperPkg("P2", "AC-2")
	p2.Dependencies = []string{"P1"} // connected chain (single weakly-connected component)
	g := workgraph.WorkGraph{TaskID: "TASK-1", Packages: []workgraph.WorkPackage{p1, p2}}
	v, err := workgraph.ValidateAgainstSpec(g, spec)
	if err != nil {
		t.Fatalf("ValidateAgainstSpec: %v", err)
	}
	if v.TaskID() != "TASK-1" {
		t.Errorf("TaskID = %q", v.TaskID())
	}
}

// ---- helpers ----

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func topoPrecedes(topo []string, before, after string) bool {
	bi, ai := -1, -1
	for i, id := range topo {
		if id == before {
			bi = i
		}
		if id == after {
			ai = i
		}
	}
	if bi < 0 || ai < 0 {
		return false
	}
	return bi < ai
}

// guard: ensure workgraph.ErrInvalidWorkGraph is usable with errors.Is/As for
// callers that need to classify validation failures.
func TestErrInvalidWorkGraph_Classifiable(t *testing.T) {
	g := workgraph.WorkGraph{TaskID: "", Packages: nil}
	_, err := workgraph.ValidateWorkGraph(g)
	if err == nil {
		t.Fatal("expected error for empty graph")
	}
	if !errors.Is(err, workgraph.ErrInvalidWorkGraph) {
		t.Errorf("errors.Is(err, ErrInvalidWorkGraph) = false; err=%v", err)
	}
	// The joined error should mention a concrete problem.
	if !strings.Contains(err.Error(), "task_id is required") {
		t.Errorf("err should mention task_id requirement; got: %v", err)
	}
}

// compile-time: ensure JSON tags produce the documented wire shape by
// round-tripping through encoding/json on a sample (stable-serialisation is
// exercised in detail in serialize_test.go, this is just a smoke check that the
// types are JSON-compatible).
func TestWorkPackage_JSONShape_Smoke(t *testing.T) {
	p := workgraph.WorkPackage{
		ID:            "P1",
		TaskID:        "TASK-1",
		Stage:         workgraph.StageImplementation,
		Title:         "t",
		Objective:     "o",
		AcceptedACIDs: []string{"AC-1"},
		State:         workgraph.PackagePending,
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"accepted_ac_ids":["AC-1"]`) {
		t.Errorf("unexpected JSON shape: %s", b)
	}
}
