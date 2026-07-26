package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/enggate"
)

// gateFixture builds the real forge binary once and provides helpers to write a
// manifest and run `forge gate ...` against it. This is a black-box test: it
// drives the COMPILED binary and asserts only on stdout/stderr/exit codes, with
// no access to internal Go state (engineering baseline §2: blackbox evidence).
type gateFixture struct {
	t   *testing.T
	bin string
	dir string
}

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	bin := forgeBinary(t)
	dir := t.TempDir()
	return &gateFixture{t: t, bin: bin, dir: dir}
}

func (g *gateFixture) writeManifest(name string, m enggate.Manifest) string {
	g.t.Helper()
	p := filepath.Join(g.dir, name)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		g.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		g.t.Fatal(err)
	}
	return p
}

func (g *gateFixture) run(args ...string) (string, string, int) {
	g.t.Helper()
	return runForgeInDir(g.t, g.bin, "", g.dir, args...)
}

// goodGateManifest mirrors enggate.goodManifest but is private to the cli
// package so the black-box test is self-contained.
func goodGateManifest() enggate.Manifest {
	return enggate.Manifest{
		SchemaVersion:   enggate.SchemaVersion,
		BaselineVersion: enggate.BaselineVersion,
		TaskID:          "M14-BB",
		PreviousState:   enggate.StateStarted,
		State:           enggate.StateImplementedTested,
		Criteria: []enggate.Criterion{
			{ID: "AC1", Description: "mandatory", Mandatory: true},
		},
		Evidence: []enggate.Evidence{
			{Criterion: "AC1", Level: enggate.LevelUnit, Reference: "internal/enggate.TestX", Status: enggate.StatusPassed, Automated: true},
			{Criterion: "AC1", Level: enggate.LevelBlackBox, Reference: "internal/cli.TestGateBlackBox", Status: enggate.StatusPassed, Automated: true},
		},
		Commands: []enggate.CommandResult{
			{Label: "make check", Status: enggate.StatusPassed},
		},
		Actors: enggate.Actors{Implementer: "impl", Reviewer: "review", Acceptor: "accept"},
		Reports: enggate.Reports{
			Implementation: "docs/reviews/m14/M14-BB_IMPLEMENTATION.md",
			Review:         "docs/reviews/m14/M14-BB_REVIEW.md",
			Acceptance:     "docs/reviews/m14/M14-BB_ACCEPTANCE.md",
		},
		BlackBox: enggate.BlackBox{
			CompiledBinary: "forge",
			Scenario:       "forge gate validate --manifest ...",
			Status:         enggate.StatusPassed,
		},
	}
}

// TestGateBlackBoxBaseline prints active versions through the compiled binary.
func TestGateBlackBoxBaseline(t *testing.T) {
	g := newGateFixture(t)
	out, _, code := g.run("gate", "baseline")
	if code != 0 {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "baseline_version: 1") {
		t.Fatalf("output missing baseline_version: 1:\n%s", out)
	}
	if !strings.Contains(out, "docs/engineering/ENGINEERING_BASELINE.md") {
		t.Fatalf("output missing baseline doc path:\n%s", out)
	}
}

// TestGateBlackBoxPositiveFlow drives the full lifecycle through the binary,
// validating each transition and finally unlocking the successor.
func TestGateBlackBoxPositiveFlow(t *testing.T) {
	g := newGateFixture(t)

	// STARTED -> IMPLEMENTED_TESTED
	m := goodGateManifest()
	p := g.writeManifest("impl.json", m)
	out, _, code := g.run("gate", "validate", "-m", p)
	if code != 0 {
		t.Fatalf("validate impl: exit=%d stderr如下\n%s", code, out)
	}
	if !strings.Contains(out, "is legal under baseline v1") {
		t.Fatalf("validate impl output: %s", out)
	}

	// IMPLEMENTED_TESTED -> REVIEW_APPROVED
	m.PreviousState = enggate.StateImplementedTested
	m.State = enggate.StateReviewApproved
	p = g.writeManifest("review.json", m)
	out, _, code = g.run("gate", "validate", "-m", p)
	if code != 0 {
		t.Fatalf("validate review: exit=%d out=%s", code, out)
	}

	// REVIEW_APPROVED -> ACCEPTED
	m.PreviousState = enggate.StateReviewApproved
	m.State = enggate.StateAccepted
	p = g.writeManifest("accept.json", m)
	out, _, code = g.run("gate", "validate", "-m", p)
	if code != 0 {
		t.Fatalf("validate accept: exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "ACCEPTED") && !strings.Contains(out, "is legal") {
		t.Fatalf("validate accept output: %s", out)
	}

	// gate next on the ACCEPTED predecessor must succeed.
	out, _, code = g.run("gate", "next", "-m", p)
	if code != 0 {
		t.Fatalf("next after accept: exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "successor task may start") {
		t.Fatalf("next output: %s", out)
	}
}

// TestGateBlackBoxNegativeMissingEvidence: blackbox evidence removed -> rejected.
func TestGateBlackBoxNegativeMissingEvidence(t *testing.T) {
	g := newGateFixture(t)
	m := goodGateManifest()
	m.Evidence = []enggate.Evidence{
		{Criterion: "AC1", Level: enggate.LevelUnit, Reference: "u", Status: enggate.StatusPassed, Automated: true},
	}
	p := g.writeManifest("no-bb.json", m)
	_, stderr, code := g.run("gate", "validate", "-m", p)
	if code == 0 {
		t.Fatalf("expected non-zero exit, got 0")
	}
	if !strings.Contains(stderr, "no passing blackbox-level evidence present") {
		t.Fatalf("expected blackbox-evidence rejection in stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "REJECTED") {
		t.Fatalf("expected REJECTED banner in stderr:\n%s", stderr)
	}
}

// TestGateBlackBoxNegativeSameActor: review actor == implementer -> rejected.
func TestGateBlackBoxNegativeSameActor(t *testing.T) {
	g := newGateFixture(t)
	m := goodGateManifest()
	m.PreviousState = enggate.StateImplementedTested
	m.State = enggate.StateReviewApproved
	m.Actors.Reviewer = m.Actors.Implementer
	p := g.writeManifest("same-actor.json", m)
	_, stderr, code := g.run("gate", "validate", "-m", p)
	if code == 0 {
		t.Fatalf("expected non-zero exit for self-review")
	}
	if !strings.Contains(stderr, "actors.reviewer == actors.implementer") {
		t.Fatalf("expected self-review message in stderr:\n%s", stderr)
	}
}

// TestGateBlackBoxNegativeNextBeforeAccepted: next on a non-ACCEPTED manifest fails.
func TestGateBlackBoxNegativeNextBeforeAccepted(t *testing.T) {
	g := newGateFixture(t)
	m := goodGateManifest() // state == IMPLEMENTED_TESTED
	p := g.writeManifest("not-accepted.json", m)
	_, stderr, code := g.run("gate", "next", "-m", p)
	if code == 0 {
		t.Fatalf("expected non-zero exit for next-before-accepted")
	}
	if !strings.Contains(stderr, "not ACCEPTED") {
		t.Fatalf("expected 'not ACCEPTED' in stderr:\n%s", stderr)
	}
}

// Regression (review BLOCKER-1): a manifest that CLAIMS state=ACCEPTED but was
// not validly accepted must NOT unlock the successor through the compiled binary.
func TestGateBlackBoxNegativeNextFabricatedAccepted(t *testing.T) {
	g := newGateFixture(t)
	m := goodGateManifest()
	m.PreviousState = enggate.StateReviewApproved
	m.State = enggate.StateAccepted
	// Correct schema/baseline, but self-accept and no blackbox -> ACCEPTED never earned.
	m.Actors.Acceptor = m.Actors.Implementer
	m.BlackBox.Status = enggate.StatusFailed
	m.BlackBox.Scenario = ""
	p := g.writeManifest("fabricated-accepted.json", m)
	_, stderr, code := g.run("gate", "next", "-m", p)
	if code == 0 {
		t.Fatalf("expected non-zero exit for fabricated ACCEPTED claim")
	}
	if !strings.Contains(stderr, "not validly accepted") {
		t.Fatalf("expected 'not validly accepted' in stderr:\n%s", stderr)
	}
}

// TestGateBlackBoxNegativeInvalidTransition: STARTED -> ACCEPTED rejected.
func TestGateBlackBoxNegativeInvalidTransition(t *testing.T) {
	g := newGateFixture(t)
	m := goodGateManifest()
	m.PreviousState = enggate.StateStarted
	m.State = enggate.StateAccepted
	p := g.writeManifest("skip.json", m)
	_, stderr, code := g.run("gate", "validate", "-m", p)
	if code == 0 {
		t.Fatalf("expected non-zero exit for skip transition")
	}
	if !strings.Contains(stderr, "illegal transition STARTED -> ACCEPTED") {
		t.Fatalf("expected illegal-transition message:\n%s", stderr)
	}
}

// TestGateBlackBoxMissingManifestFlag: usage error, exit 1.
func TestGateBlackBoxMissingManifestFlag(t *testing.T) {
	g := newGateFixture(t)
	_, stderr, code := g.run("gate", "validate")
	if code == 0 {
		t.Fatalf("expected non-zero exit for missing --manifest")
	}
	if !strings.Contains(stderr, "--manifest is required") {
		t.Fatalf("expected usage message:\n%s", stderr)
	}
}

// TestGateBlackBoxUnknownSubcommand: exit 1 with help.
func TestGateBlackBoxUnknownSubcommand(t *testing.T) {
	g := newGateFixture(t)
	_, stderr, code := g.run("gate", "frobnicate")
	if code == 0 {
		t.Fatalf("expected non-zero exit for unknown subcommand")
	}
	if !strings.Contains(stderr, "unknown subcommand") {
		t.Fatalf("expected unknown-subcommand message:\n%s", stderr)
	}
}
