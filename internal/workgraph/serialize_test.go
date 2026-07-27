package workgraph_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/workgraph"
)

// ---- Round-trip: Marshal -> Unmarshal preserves structure ----

func TestSerialize_RoundTrip(t *testing.T) {
	spec := helperSpec("TASK-1", "Build it.",
		helperAC("AC-1", "First."),
		helperAC("AC-2", "Second."),
	)
	v, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	data, err := workgraph.MarshalValidated(v)
	if err != nil {
		t.Fatalf("MarshalValidated: %v", err)
	}

	got, err := workgraph.UnmarshalWorkGraph(data)
	if err != nil {
		t.Fatalf("UnmarshalWorkGraph: %v", err)
	}
	// Re-validate the round-tripped graph against the same spec; it must still
	// be a runnable, valid graph identical to the original.
	v2, err := workgraph.ValidateAgainstSpec(got, spec)
	if err != nil {
		t.Fatalf("round-tripped graph failed re-validation: %v", err)
	}
	data2, _ := workgraph.MarshalValidated(v2)
	if !bytes.Equal(data, data2) {
		t.Errorf("round-trip not stable:\norig: %s\nrt:   %s", data, data2)
	}
}

// ---- Stable serialization: input ordering does not affect output ----

func TestSerialize_Canonical_IgnoresInputOrdering(t *testing.T) {
	pkgA := helperPkg("ZZZ", "AC-1")
	pkgB := helperPkg("AAA", "AC-2")
	pkgB.Dependencies = []string{"ZZZ"}
	g1 := workgraph.WorkGraph{TaskID: "TASK-1", Packages: []workgraph.WorkPackage{pkgA, pkgB}}
	g2 := workgraph.WorkGraph{TaskID: "TASK-1", Packages: []workgraph.WorkPackage{pkgB, pkgA}} // swapped

	b1, err := workgraph.MarshalWorkGraph(g1)
	if err != nil {
		t.Fatalf("MarshalWorkGraph g1: %v", err)
	}
	b2, err := workgraph.MarshalWorkGraph(g2)
	if err != nil {
		t.Fatalf("MarshalWorkGraph g2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("canonical form must ignore package input order:\n%s\n%s", b1, b2)
	}
	// AAA must sort before ZZZ in the output.
	var doc struct {
		Packages []struct {
			ID string `json:"id"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(b1, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Packages) != 2 || doc.Packages[0].ID != "AAA" || doc.Packages[1].ID != "ZZZ" {
		t.Errorf("packages not sorted by ID: %+v", doc.Packages)
	}
}

func TestSerialize_Canonical_SortsPerPackageLists(t *testing.T) {
	// ACs and dependencies given out-of-order must be sorted in the output.
	p := helperPkg("P1", "AC-3", "AC-1", "AC-2")
	p.Dependencies = []string{"Z", "A", "M"}
	g := workgraph.WorkGraph{TaskID: "TASK-1", Packages: []workgraph.WorkPackage{p}}
	b, err := workgraph.MarshalWorkGraph(g)
	if err != nil {
		t.Fatalf("MarshalWorkGraph: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `"accepted_ac_ids":["AC-1","AC-2","AC-3"]`) {
		t.Errorf("ACs not sorted in output: %s", s)
	}
	if !strings.Contains(s, `"dependencies":["A","M","Z"]`) {
		t.Errorf("dependencies not sorted in output: %s", s)
	}
}

// ---- Determinism across many runs (byte-identical for identical input) ----

func TestSerialize_Deterministic_10Runs(t *testing.T) {
	spec := helperSpec("TASK-D", "Build the feature.",
		helperAC("AC-1", "First."),
		helperAC("AC-2", "Second."),
		helperAC("AC-3", "Third."),
	)
	v, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	first, err := workgraph.MarshalValidated(v)
	if err != nil {
		t.Fatalf("MarshalValidated: %v", err)
	}
	for i := 0; i < 9; i++ {
		vn, err := workgraph.Decompose(spec)
		if err != nil {
			t.Fatalf("Decompose run %d: %v", i, err)
		}
		bn, err := workgraph.MarshalValidated(vn)
		if err != nil {
			t.Fatalf("MarshalValidated run %d: %v", i, err)
		}
		if !bytes.Equal(first, bn) {
			t.Fatalf("run %d differs from first:\nfirst: %s\nrun:   %s", i, first, bn)
		}
	}
}

// ---- Unmarshal rejects unknown fields (forward-compatible strictness) ----

func TestSerialize_Unmarshal_RejectsUnknownFields(t *testing.T) {
	valid := workgraph.WorkGraph{
		TaskID:   "TASK-1",
		Packages: []workgraph.WorkPackage{helperPkg("P1", "AC-1")},
	}
	data, err := workgraph.MarshalWorkGraph(valid)
	if err != nil {
		t.Fatalf("MarshalWorkGraph: %v", err)
	}
	// Inject an unknown field.
	tampered := bytes.Replace(data, []byte(`"task_id":"TASK-1"`), []byte(`"task_id":"TASK-1","rogue":42`), 1)
	if _, err := workgraph.UnmarshalWorkGraph(tampered); err == nil {
		t.Errorf("UnmarshalWorkGraph should reject unknown fields")
	}
}

// ---- Canonicalize is pure (does not mutate input) ----

func TestSerialize_Canonicalize_DoesNotMutateInput(t *testing.T) {
	p := helperPkg("P1", "AC-3", "AC-1")
	g := workgraph.WorkGraph{TaskID: "TASK-1", Packages: []workgraph.WorkPackage{p}}
	original := append([]string(nil), g.Packages[0].AcceptedACIDs...)
	_ = workgraph.Canonicalize(g)
	if !equalStringSlices(g.Packages[0].AcceptedACIDs, original) {
		t.Errorf("Canonicalize mutated its input: %v", g.Packages[0].AcceptedACIDs)
	}
}

// ---- Attempts round-trip ----

func TestSerialize_AttemptsRoundTrip(t *testing.T) {
	p := helperPkg("P1", "AC-1")
	started := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	finished := started.Add(5 * time.Minute)
	p.Attempts = []workgraph.Attempt{
		{Index: 1, State: workgraph.PackageFailed, StartedAt: started, FinishedAt: finished, FailureReason: "timeout", ExitCode: 1},
		{Index: 2, State: workgraph.PackageSucceeded, StartedAt: finished, FinishedAt: finished.Add(3 * time.Minute), ExitCode: 0},
	}
	g := workgraph.WorkGraph{TaskID: "TASK-1", Packages: []workgraph.WorkPackage{p}}
	b, err := workgraph.MarshalWorkGraph(g)
	if err != nil {
		t.Fatalf("MarshalWorkGraph: %v", err)
	}
	got, err := workgraph.UnmarshalWorkGraph(b)
	if err != nil {
		t.Fatalf("UnmarshalWorkGraph: %v", err)
	}
	if len(got.Packages[0].Attempts) != 2 {
		t.Fatalf("attempts round-trip lost entries: %+v", got.Packages[0].Attempts)
	}
	a := got.Packages[0].Attempts[0]
	if a.Index != 1 || a.State != workgraph.PackageFailed || a.FailureReason != "timeout" || a.ExitCode != 1 {
		t.Errorf("attempt[0] mismatch: %+v", a)
	}
	if got.Packages[0].Attempts[1].State != workgraph.PackageSucceeded {
		t.Errorf("attempt[1] state mismatch: %+v", got.Packages[0].Attempts[1])
	}
}

// ---- Empty AllowedScope/Dependencies/Attempts: omitempty keeps payload small ----

func TestSerialize_OmitEmpty(t *testing.T) {
	p := helperPkg("P1", "AC-1")
	// No deps, no scope, no attempts.
	g := workgraph.WorkGraph{TaskID: "TASK-1", Packages: []workgraph.WorkPackage{p}}
	b, err := workgraph.MarshalWorkGraph(g)
	if err != nil {
		t.Fatalf("MarshalWorkGraph: %v", err)
	}
	s := string(b)
	for _, key := range []string{`"allowed_scope"`, `"dependencies"`, `"attempts"`} {
		if strings.Contains(s, key) {
			t.Errorf("empty %s should be omitted; payload: %s", key, s)
		}
	}
}

// ---- Valid JSON: the marshalled payload parses with a standard decoder ----

func TestSerialize_ValidJSON(t *testing.T) {
	spec := helperSpec("TASK-1", "Build it.", helperAC("AC-1", "x"))
	v, _ := workgraph.Decompose(spec)
	b, _ := workgraph.MarshalValidated(v)
	var any map[string]any
	if err := json.Unmarshal(b, &any); err != nil {
		t.Fatalf("marshalled payload is not valid JSON: %v\n%s", err, b)
	}
}

// ---- MarshalValidated nil guard ----

func TestMarshalValidated_NilGuard(t *testing.T) {
	if _, err := workgraph.MarshalValidated(nil); err == nil {
		t.Errorf("MarshalValidated(nil) should error")
	}
}

// ---- MarshalValidated matches MarshalWorkGraph of the underlying graph ----

func TestMarshalValidated_MatchesMarshalWorkGraph(t *testing.T) {
	spec := helperSpec("TASK-1", "Build it.", helperAC("AC-1", "x"))
	v, _ := workgraph.Decompose(spec)
	mv, _ := workgraph.MarshalValidated(v)
	mg, _ := workgraph.MarshalWorkGraph(v.Graph())
	if !bytes.Equal(mv, mg) {
		t.Errorf("MarshalValidated differs from MarshalWorkGraph(Graph()):\n%s\n%s", mv, mg)
	}
}
