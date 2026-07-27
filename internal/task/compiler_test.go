package task

import (
	"errors"
	"strings"
	"testing"
)

// TestCompile_Fixture_Bugfix is the bugfix fixture (spec §9.1 example): a free
// -form bug report with no structured sections. The compiler must still produce
// a complete, valid specification by deriving the objective from the first
// paragraph and synthesising at least one acceptance criterion.
func TestCompile_Fixture_Bugfix(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-1",
		Title:       "Double progress indicator on payment screen",
		Description: "На экране оплаты иногда показывается два progress indicator. Исправь.",
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := mustValidateSpec(t, res.Specification); err != nil {
		t.Fatalf("compiled spec invalid: %v", err)
	}
	if res.Specification.Objective == "" {
		t.Fatal("objective empty for bugfix fixture")
	}
	if !strings.Contains(strings.ToLower(res.Specification.Objective), "progress") {
		t.Fatalf("objective should mention progress: %q", res.Specification.Objective)
	}
	if len(res.Specification.AcceptanceCriteria) == 0 {
		t.Fatal("bugfix must have at least one synthesised AC")
	}
	// Bugfix with payment-screen context must classify at R4 (payment keyword).
	if res.Specification.Risk != RiskR4 {
		t.Fatalf("bugfix on payment screen risk = %s, want R4", res.Specification.Risk)
	}
	if !res.Specification.Complexity.IsValid() {
		t.Fatalf("complexity invalid: %q", res.Specification.Complexity)
	}
	// A free-form bug report cannot reach HIGH confidence (no explicit ACs).
	if res.Confidence == ConfidenceHigh {
		t.Fatalf("free-form bugfix should not be HIGH confidence")
	}
}

// TestCompile_Fixture_Feature covers a typical feature request with explicit
// sections (objective + ACs). This must produce HIGH confidence and preserve the
// caller's ACs verbatim.
func TestCompile_Fixture_Feature(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID: "proj-2",
		Title:  "Add retry button",
		Description: "Objective: Add a retry button to the network error screen.\n" +
			"Acceptance Criteria:\n" +
			"- Button is shown when a network error occurs.\n" +
			"- Clicking retry re-submits within 500ms.\n" +
			"Non-goals:\n" +
			"- Redesigning the error screen.\n" +
			"Constraints:\n" +
			"- No new third-party dependencies.",
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := mustValidateSpec(t, res.Specification); err != nil {
		t.Fatalf("compiled spec invalid: %v", err)
	}
	if res.Confidence != ConfidenceHigh {
		t.Fatalf("feature with explicit sections: confidence = %s, want HIGH", res.Confidence)
	}
	if len(res.Specification.AcceptanceCriteria) != 2 {
		t.Fatalf("ACs = %d, want 2", len(res.Specification.AcceptanceCriteria))
	}
	if res.Specification.AcceptanceCriteria[0].Statement != "Button is shown when a network error occurs." {
		t.Fatalf("AC-1 statement lost: %q", res.Specification.AcceptanceCriteria[0].Statement)
	}
	if len(res.Specification.NonGoals) != 1 {
		t.Fatalf("non-goals = %d, want 1", len(res.Specification.NonGoals))
	}
	if len(res.Specification.Constraints) != 1 {
		t.Fatalf("constraints = %d, want 1", len(res.Specification.Constraints))
	}
}

// TestCompile_Fixture_UITask covers a UI task: it must mark visual requirements
// when a design reference attachment is provided.
func TestCompile_Fixture_UITask(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-3",
		Title:       "Implement profile header",
		Description: "Implement the profile header per the attached mock.",
		Attachments: []Attachment{
			{Hash: "sha256:deadbeef", Filename: "mock.png", MimeType: "image/png", Size: 1024, Role: RoleDesignReference},
		},
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := mustValidateSpec(t, res.Specification); err != nil {
		t.Fatalf("compiled spec invalid: %v", err)
	}
	if !res.Specification.VisualRequirements.Required {
		t.Fatal("UI task with design reference must mark visual requirements as required")
	}
	if len(res.Specification.VisualRequirements.References) != 1 ||
		res.Specification.VisualRequirements.References[0] != "sha256:deadbeef" {
		t.Fatalf("design reference hash not propagated: %v",
			res.Specification.VisualRequirements.References)
	}
}

// TestCompile_Fixture_AuthPaymentRisky covers the auth/payment risky task: the
// risk classifier must hard-floor at R4, and the compiler must surface the
// explicit clarification that auth/payment changes carry an elevated review
// requirement (unsafe ambiguity: cannot be silently assumed).
func TestCompile_Fixture_AuthPaymentRisky(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-4",
		Title:       "Rotate OAuth client secrets",
		Description: "Rotate OAuth client secrets and invalidate active sessions.",
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := mustValidateSpec(t, res.Specification); err != nil {
		t.Fatalf("compiled spec invalid: %v", err)
	}
	if res.Specification.Risk != RiskR4 {
		t.Fatalf("auth/payment risk = %s, want R4", res.Specification.Risk)
	}
	// R4 changes are inherently unsafe to assume silently: at least one
	// clarification is expected.
	if len(res.Clarifications) == 0 {
		t.Fatal("R4 task must surface at least one clarification")
	}
}

// TestCompile_Fixture_VagueTask proves the compiler does NOT invent a fake
// complete specification when the input is too vague. It must emit LOW
// confidence and a clarification instead of disguising the gap.
func TestCompile_Fixture_VagueTask(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-5",
		Title:       "fix it",
		Description: "fix it",
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.Confidence != ConfidenceLow {
		t.Fatalf("vague task confidence = %s, want LOW", res.Confidence)
	}
	if len(res.Clarifications) == 0 {
		t.Fatal("vague task must produce at least one clarification")
	}
	// Even with LOW confidence, the compiled spec must still validate (the
	// caller can save it; the clarifications are advisory). The objective may
	// be the title/description but it must be non-empty.
	if res.Specification.Objective == "" {
		t.Fatal("vague task should still carry a synthesised objective")
	}
	if err := mustValidateSpec(t, res.Specification); err != nil {
		t.Fatalf("compiled spec for vague task fails validation: %v", err)
	}
}

// TestCompile_Fixture_AttachmentOnly covers the attachment-only task (spec §9.2:
// a task may be created with just an attachment and no description). The
// compiler must derive an objective from the attachment metadata and emit LOW
// confidence with a clarification (no content is read).
func TestCompile_Fixture_AttachmentOnly(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-6",
		Description: "",
		Attachments: []Attachment{
			{Hash: "sha256:feedface", Filename: "requirements.md", MimeType: "text/markdown", Size: 512, Role: RoleRequirements},
		},
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := mustValidateSpec(t, res.Specification); err != nil {
		t.Fatalf("compiled spec invalid: %v", err)
	}
	if res.Specification.Objective == "" {
		t.Fatal("attachment-only task must still synthesise an objective")
	}
	if res.Confidence != ConfidenceLow {
		t.Fatalf("attachment-only task confidence = %s, want LOW (content not read)", res.Confidence)
	}
	if len(res.Clarifications) == 0 {
		t.Fatal("attachment-only task must produce a clarification (compiler cannot read content)")
	}
}

// TestCompile_Deterministic is the headline determinism test: identical input
// must produce identical output across two calls. The compiler is pure.
func TestCompile_Deterministic(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-7",
		Title:       "Stable output test",
		Description: "Objective: Build the dashboard widget.\nAcceptance Criteria:\n- Widget renders.\n- Widget updates every 5s.",
	}
	r1, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile r1: %v", err)
	}
	r2, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile r2: %v", err)
	}
	if !sameCompileResult(r1, r2) {
		t.Fatalf("non-deterministic output:\nr1=%+v\nr2=%+v", r1, r2)
	}
}

// TestCompile_Deterministic_AcrossVaryingWhitespace proves determinism is robust
// to trailing-whitespace and line-ending variations that have no semantic
// meaning. Two inputs that differ only in CRLF vs LF must compile to specs with
// identical objectives and AC statements.
func TestCompile_Deterministic_AcrossVaryingWhitespace(t *testing.T) {
	t.Parallel()
	lf := "Objective: Build X.\nAcceptance Criteria:\n- AC1.\n- AC2."
	crlf := "Objective: Build X.\r\nAcceptance Criteria:\r\n- AC1.\r\n- AC2."
	r1, err := Compile(CompileInput{TaskID: "p", Description: lf})
	if err != nil {
		t.Fatalf("Compile lf: %v", err)
	}
	r2, err := Compile(CompileInput{TaskID: "p", Description: crlf})
	if err != nil {
		t.Fatalf("Compile crlf: %v", err)
	}
	if r1.Specification.Objective != r2.Specification.Objective {
		t.Fatalf("objective differs: %q vs %q", r1.Specification.Objective, r2.Specification.Objective)
	}
	if len(r1.Specification.AcceptanceCriteria) != len(r2.Specification.AcceptanceCriteria) {
		t.Fatalf("AC count differs: %d vs %d",
			len(r1.Specification.AcceptanceCriteria), len(r2.Specification.AcceptanceCriteria))
	}
	for i, ac := range r1.Specification.AcceptanceCriteria {
		if ac.Statement != r2.Specification.AcceptanceCriteria[i].Statement {
			t.Fatalf("AC %d statement differs: %q vs %q",
				i, ac.Statement, r2.Specification.AcceptanceCriteria[i].Statement)
		}
	}
}

// TestCompile_RejectsMissingTaskID proves the compiler enforces the
// Specification invariant at compile time: no TaskID → ErrInvalidSpecification.
func TestCompile_RejectsMissingTaskID(t *testing.T) {
	t.Parallel()
	_, err := Compile(CompileInput{
		Description: "Objective: x\nAcceptance Criteria:\n- y",
	})
	if !errors.Is(err, ErrInvalidSpecification) {
		t.Fatalf("expected ErrInvalidSpecification, got %v", err)
	}
}

// TestCompile_RejectsEmptyInput proves the compiler does not silently produce a
// fake specification when there is no description and no attachment. It must
// return a LOW-confidence result with a clarification AND the compiled spec
// must fail validation (caller cannot persist it through SpecificationStore).
func TestCompile_RejectsEmptyInput(t *testing.T) {
	t.Parallel()
	res, err := Compile(CompileInput{TaskID: "proj-x"})
	if err != nil {
		t.Fatalf("Compile empty: unexpected error %v", err)
	}
	if res.Confidence != ConfidenceLow {
		t.Fatalf("empty input confidence = %s, want LOW", res.Confidence)
	}
	if len(res.Clarifications) == 0 {
		t.Fatal("empty input must produce a clarification")
	}
	_, verr := ValidateSpecification(res.Specification)
	if verr == nil {
		t.Fatal("empty-input spec should fail ValidateSpecification")
	}
}

// TestCompile_Regression_TrailingSpacesInHeader is a regression test for a
// parser defect: section headers with trailing whitespace (e.g.
// "Objective:  \n") must still be recognised.
func TestCompile_Regression_TrailingSpacesInHeader(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-r1",
		Description: "Objective:   \nBuild the module.\nAcceptance Criteria:   \n- It works.",
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.Specification.Objective != "Build the module." {
		t.Fatalf("objective with trailing-space header lost: %q", res.Specification.Objective)
	}
	if len(res.Specification.AcceptanceCriteria) != 1 {
		t.Fatalf("ACs with trailing-space header lost: %d", len(res.Specification.AcceptanceCriteria))
	}
}

// TestCompile_Regression_CaseInsensitiveHeaders is a regression test for a
// parser defect: headers in any case (objective:, OBJECTIVE:, Objective:) must
// all be recognised.
func TestCompile_Regression_CaseInsensitiveHeaders(t *testing.T) {
	t.Parallel()
	cases := []string{"objective:", "OBJECTIVE:", "Objective:", "ObJeCtIvE:"}
	for _, h := range cases {
		in := CompileInput{
			TaskID:      "proj-r2",
			Description: h + " Build it.\nAcceptance Criteria:\n- It works.",
		}
		res, err := Compile(in)
		if err != nil {
			t.Fatalf("Compile %q: %v", h, err)
		}
		if res.Specification.Objective != "Build it." {
			t.Fatalf("header %q not recognised; objective=%q", h, res.Specification.Objective)
		}
	}
}

// TestCompile_Regression_NumberedListACs is a regression test for a parser
// defect: numbered ACs ("1. ...", "2. ...") must be parsed as list items, not
// collapsed into a single string.
func TestCompile_Regression_NumberedListACs(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID: "proj-r3",
		Description: "Objective: Build it.\n" +
			"Acceptance Criteria:\n" +
			"1. First AC.\n" +
			"2. Second AC.\n" +
			"3. Third AC.",
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(res.Specification.AcceptanceCriteria) != 3 {
		t.Fatalf("numbered ACs collapsed: got %d (statements=%v)",
			len(res.Specification.AcceptanceCriteria), res.Specification.AcceptanceCriteria)
	}
	want := []string{"First AC.", "Second AC.", "Third AC."}
	for i, ac := range res.Specification.AcceptanceCriteria {
		if ac.Statement != want[i] {
			t.Fatalf("numbered AC %d = %q, want %q", i+1, ac.Statement, want[i])
		}
	}
}

// TestCompile_Regression_SectionHeaderAtEOF is a regression test: a header at
// the very end of the description with no body must not crash and must fall
// back to the synthesised defaults.
func TestCompile_Regression_SectionHeaderAtEOF(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-r4",
		Description: "Build the module.\nAcceptance Criteria:",
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := mustValidateSpec(t, res.Specification); err != nil {
		t.Fatalf("compiled spec invalid: %v", err)
	}
	// Empty AC section header means we still synthesise at least one AC.
	if len(res.Specification.AcceptanceCriteria) == 0 {
		t.Fatal("expected at least one synthesised AC for empty Acceptance Criteria section")
	}
}

// TestCompile_Regression_AttachmentRoleMapping proves attachment metadata roles
// are surfaced in the CompileResult so the caller can persist them (§18.1).
func TestCompile_Regression_AttachmentRoleMapping(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-r5",
		Description: "Objective: Investigate the bug.",
		Attachments: []Attachment{
			{Hash: "h1", Filename: "shot.png", MimeType: "image/png", Role: RoleBugScreenshot},
			{Hash: "h2", Filename: "log.txt", MimeType: "text/plain", Role: RoleLog},
			{Hash: "h3", Filename: "api.json", MimeType: "application/json", Role: RoleAPISpec},
		},
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(res.AttachmentRoles) != 3 {
		t.Fatalf("attachment roles = %d, want 3", len(res.AttachmentRoles))
	}
	if res.AttachmentRoles["h1"] != RoleBugScreenshot {
		t.Errorf("h1 role = %s, want %s", res.AttachmentRoles["h1"], RoleBugScreenshot)
	}
	if res.AttachmentRoles["h2"] != RoleLog {
		t.Errorf("h2 role = %s, want %s", res.AttachmentRoles["h2"], RoleLog)
	}
	if res.AttachmentRoles["h3"] != RoleAPISpec {
		t.Errorf("h3 role = %s, want %s", res.AttachmentRoles["h3"], RoleAPISpec)
	}
}

// TestCompile_Regression_AttachmentOnlyEmptyFilename is the MAJOR-1 regression
// test at the compiler level: when the caller omits Filename (e.g. legacy
// `hash=ROLE` CLI form), the synthesised placeholder objective must NOT contain
// the degenerate empty-paren clause "()" that the original implementation
// emitted. The objective must still be non-empty and the spec must still
// validate, with LOW confidence + a clarification.
//
// This test pins the defensive fix in synthesiseObjectiveFromAttachments so a
// future refactor cannot reintroduce the "attached requirements ()." output.
func TestCompile_Regression_AttachmentOnlyEmptyFilename(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID: "proj-r6",
		Attachments: []Attachment{
			{Hash: "sha256:abc", Role: RoleRequirements},
		},
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	obj := res.Specification.Objective
	if obj == "" {
		t.Fatal("attachment-only task must still synthesise an objective")
	}
	if strings.Contains(obj, "()") {
		t.Fatalf("degenerate empty-paren objective reappeared: %q", obj)
	}
	if !strings.Contains(obj, "requirements") {
		t.Fatalf("objective should mention the attachment role: %q", obj)
	}
	if err := mustValidateSpec(t, res.Specification); err != nil {
		t.Fatalf("compiled spec invalid: %v", err)
	}
	if res.Confidence != ConfidenceLow {
		t.Fatalf("confidence = %s, want LOW (attachment-only)", res.Confidence)
	}
	if len(res.Clarifications) == 0 {
		t.Fatal("attachment-only task must produce a clarification")
	}
}

// TestCompile_NeverMutatesLockedSpec is the headline §28 invariant at the
// compiler level: the compiler is pure and never reads or writes storage, so
// running Compile against an input whose prior version is locked cannot change
// the locked content. Combined with SpecificationStore.Save this is enforced
// end-to-end via TestCompile_LockedSpecCannotBeMutatedViaSave below.
func TestCompile_NeverMutatesLockedSpec(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-lock-1",
		Description: "Objective: Original.\nAcceptance Criteria:\n- Original AC.",
	}
	r1, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile r1: %v", err)
	}
	if r1.Specification.Locked {
		t.Fatal("freshly compiled spec must NOT be locked")
	}
	if r1.Specification.Version != 0 {
		t.Fatalf("compiled spec version = %d, want 0 (Save assigns version)", r1.Specification.Version)
	}
	// Compiling the same input again must not produce a "locked" or
	// version-bumped specification.
	r2, _ := Compile(in)
	if r2.Specification.Locked || r2.Specification.Version != 0 {
		t.Fatalf("re-compile mutated lock/version: %+v", r2.Specification)
	}
}

// TestCompile_LockedSpecCannotBeMutatedViaSave is the end-to-end proof of the
// "compiler never mutates a locked specification" AC: compile → save → lock →
// compile-and-save-again must NOT change the locked content. The compiler
// returns Version=0 so Save attempts to allocate a NEW version; the locked v1
// is preserved byte-for-byte.
func TestCompile_LockedSpecCannotBeMutatedViaSave(t *testing.T) {
	store, _, taskID := newSpecStoreDB(t)
	ctx := t.Context()

	r1, err := Compile(CompileInput{
		TaskID:      taskID,
		Description: "Objective: Original.\nAcceptance Criteria:\n- Original AC.",
	})
	if err != nil {
		t.Fatalf("Compile r1: %v", err)
	}
	saved1, err := store.Save(ctx, r1.Specification)
	if err != nil {
		t.Fatalf("Save r1: %v", err)
	}
	if _, err := store.Lock(ctx, taskID, saved1.Version, "reviewer-1"); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// Re-compile a CHANGED description and Save. The compiler returns Version=0
	// (new version), the locked v1 must be untouched.
	r2, err := Compile(CompileInput{
		TaskID:      taskID,
		Description: "Objective: TAMPERED.\nAcceptance Criteria:\n- TAMPERED AC.",
	})
	if err != nil {
		t.Fatalf("Compile r2: %v", err)
	}
	saved2, err := store.Save(ctx, r2.Specification)
	if err != nil {
		t.Fatalf("Save r2: %v", err)
	}
	if saved2.Version != 2 {
		t.Fatalf("expected new version 2 (compiler + Save must not overwrite locked v1), got %d",
			saved2.Version)
	}

	locked, err := store.Get(ctx, taskID, saved1.Version)
	if err != nil {
		t.Fatalf("Get locked: %v", err)
	}
	if locked.Objective != "Original." {
		t.Fatalf("locked v1 mutated: objective=%q", locked.Objective)
	}
	if !locked.Locked || locked.LockedBy != "reviewer-1" {
		t.Fatalf("lock provenance lost: locked=%v by=%q", locked.Locked, locked.LockedBy)
	}
}

// TestCompile_RiskLevels exercises every risk band so the classifier cascade
// is exercised end-to-end through the compiler.
func TestCompile_RiskLevels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc string
		want Risk
	}{
		{"Fix a typo in the README.", RiskR0},
		{"Add a chart to the analytics dashboard.", RiskR1},
		{"Add a new public API endpoint for webhook integration.", RiskR2},
		{"Add a database migration for the users table.", RiskR3},
		{"Rotate OAuth tokens and invalidate sessions.", RiskR4},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			res, err := Compile(CompileInput{TaskID: "p", Description: tc.desc})
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.Specification.Risk != tc.want {
				t.Fatalf("risk = %s, want %s (reasons=%v)",
					res.Specification.Risk, tc.want, res.RiskReasons)
			}
			if len(res.RiskReasons) == 0 {
				t.Fatalf("expected risk reasons for %s", tc.want)
			}
		})
	}
}

// TestCompile_ACsHaveStableIDs proves the compiler assigns deterministic AC IDs
// (AC-1, AC-2, ...) so the resulting specification can be persisted and the
// durable AC IDs (spec §27) remain stable.
func TestCompile_ACsHaveStableIDs(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID: "proj-ids",
		Description: "Objective: Build it.\n" +
			"Acceptance Criteria:\n- First.\n- Second.\n- Third.",
	}
	r1, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	r2, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile again: %v", err)
	}
	for i, ac := range r1.Specification.AcceptanceCriteria {
		want := "AC-" + itoa(i+1)
		if ac.ID != want {
			t.Fatalf("AC %d id = %q, want %q", i+1, ac.ID, want)
		}
		if ac.ID != r2.Specification.AcceptanceCriteria[i].ID {
			t.Fatalf("non-deterministic AC id: %q vs %q",
				ac.ID, r2.Specification.AcceptanceCriteria[i].ID)
		}
	}
}

// TestCompile_SynthesisesACWhenAbsent proves the "safe assumption" rule: when
// no explicit ACs are present but a clear objective exists, the compiler
// synthesises a default AC (so the resulting spec validates) and emits MEDIUM
// confidence with an explicit uncertainty reason.
func TestCompile_SynthesisesACWhenAbsent(t *testing.T) {
	t.Parallel()
	in := CompileInput{
		TaskID:      "proj-synth",
		Description: "Add a search bar to the catalog page.",
	}
	res, err := Compile(in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(res.Specification.AcceptanceCriteria) == 0 {
		t.Fatal("expected synthesised AC")
	}
	if res.Confidence != ConfidenceMedium {
		t.Fatalf("confidence = %s, want MEDIUM (safe assumption)", res.Confidence)
	}
	if len(res.UncertaintyReasons) == 0 {
		t.Fatal("expected at least one uncertainty reason for synthesised AC")
	}
}

// mustValidateSpec is a test helper that asserts the specification passes
// ValidateSpecification.
func mustValidateSpec(t *testing.T, s Specification) error {
	t.Helper()
	_, err := ValidateSpecification(s)
	return err
}

// sameCompileResult compares two CompileResults by value (excluding
// Specification which is compared separately). Used by determinism tests.
func sameCompileResult(a, b CompileResult) bool {
	if a.Confidence != b.Confidence {
		return false
	}
	if len(a.UncertaintyReasons) != len(b.UncertaintyReasons) {
		return false
	}
	for i := range a.UncertaintyReasons {
		if a.UncertaintyReasons[i] != b.UncertaintyReasons[i] {
			return false
		}
	}
	if len(a.Clarifications) != len(b.Clarifications) {
		return false
	}
	for i := range a.Clarifications {
		if a.Clarifications[i].Question != b.Clarifications[i].Question {
			return false
		}
		if a.Clarifications[i].Reason != b.Clarifications[i].Reason {
			return false
		}
		if !sameStringSlice(a.Clarifications[i].Options, b.Clarifications[i].Options) {
			return false
		}
	}
	if len(a.RiskReasons) != len(b.RiskReasons) {
		return false
	}
	for i := range a.RiskReasons {
		if a.RiskReasons[i] != b.RiskReasons[i] {
			return false
		}
	}
	if len(a.ComplexityReasons) != len(b.ComplexityReasons) {
		return false
	}
	// Compare ComplexityReasons IN ORDER (MINOR-4 review fix): the production
	// classifier appends reasons in a fixed sequence, so the determinism test
	// must assert that ordering, not a sorted equivalence that would hide
	// ordering drift.
	for i := range a.ComplexityReasons {
		if a.ComplexityReasons[i] != b.ComplexityReasons[i] {
			return false
		}
	}
	if !sameAttachmentRoles(a.AttachmentRoles, b.AttachmentRoles) {
		return false
	}
	return sameSpecification(a.Specification, b.Specification)
}

func sameAttachmentRoles(a, b map[string]AttachmentRole) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func sameSpecification(a, b Specification) bool {
	if a.TaskID != b.TaskID || a.Version != b.Version || a.Objective != b.Objective {
		return false
	}
	if a.Risk != b.Risk || a.Complexity != b.Complexity {
		return false
	}
	if len(a.AcceptanceCriteria) != len(b.AcceptanceCriteria) {
		return false
	}
	for i := range a.AcceptanceCriteria {
		if a.AcceptanceCriteria[i] != b.AcceptanceCriteria[i] {
			return false
		}
	}
	if !sameStringSlice(a.NonGoals, b.NonGoals) {
		return false
	}
	if !sameStringSlice(a.Assumptions, b.Assumptions) {
		return false
	}
	if !sameStringSlice(a.Constraints, b.Constraints) {
		return false
	}
	if !sameStringSlice(a.ProposedScope, b.ProposedScope) {
		return false
	}
	if !sameVisualRequirements(a.VisualRequirements, b.VisualRequirements) {
		return false
	}
	return true
}

func sameStringSlice(a, b []string) bool {
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

func sameVisualRequirements(a, b VisualRequirements) bool {
	if a.Required != b.Required {
		return false
	}
	if a.Viewport != b.Viewport || a.Theme != b.Theme || a.Locale != b.Locale || a.Density != b.Density {
		return false
	}
	return sameStringSlice(a.References, b.References)
}
