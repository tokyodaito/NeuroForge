package policy

import "testing"

func TestIsTestPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"foo_test.go", true},
		{"pkg/foo_test.go", true},
		{"src/main.go", false},
		{"login.test.ts", true},
		{"login.spec.tsx", true},
		{"app/src/test/java/Foo.java", true},
		{"tests/helper.py", true},
		{"__tests__/unit.js", true},
		{"src/main.py", false},
		{"FooTest.java", true},
		{"FooIT.java", true},
		{"models/user.go", false},
		{"lib/user.rb", false},
		{"spec/models/user_spec.rb", true},
	}
	for _, c := range cases {
		got := IsTestPath(c.path)
		if got != c.want {
			t.Errorf("IsTestPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestCheckTestScope_GenerateDisabled(t *testing.T) {
	t.Parallel()
	p := Pipeline{Tests: TestsConfig{Generate: false, ModifyExisting: true}}
	r := CheckTestScope(p, "foo_test.go", true)
	if r.Allowed {
		t.Error("new test file must be forbidden when generate=false (§24.2)")
	}
	if r.DenialCode != "test_generation_disabled" {
		t.Errorf("denial code = %q, want test_generation_disabled", r.DenialCode)
	}
	// Modifying existing tests is also forbidden when generate is off (§24.2).
	r2 := CheckTestScope(p, "foo_test.go", false)
	if r2.Allowed {
		t.Error("modifying existing test must also be forbidden when generate=false")
	}
}

func TestCheckTestScope_GenerateOn_ModifyOff(t *testing.T) {
	t.Parallel()
	p := Pipeline{Tests: TestsConfig{Generate: true, ModifyExisting: false}}
	// New test: allowed.
	if r := CheckTestScope(p, "new_test.go", true); !r.Allowed {
		t.Error("new test file must be allowed when generate=true")
	}
	// Existing test modified: forbidden.
	if r := CheckTestScope(p, "existing_test.go", false); r.Allowed {
		t.Error("modifying existing test must be forbidden when modify_existing=false")
	}
}

func TestCheckTestScope_GenerateOn_ModifyOn(t *testing.T) {
	t.Parallel()
	p := Pipeline{Tests: TestsConfig{Generate: true, ModifyExisting: true}}
	if r := CheckTestScope(p, "foo_test.go", false); !r.Allowed {
		t.Error("modifying test file must be allowed when both are on")
	}
}

func TestCheckTestScope_NonTestPath(t *testing.T) {
	t.Parallel()
	p := Pipeline{Tests: TestsConfig{Generate: false}}
	r := CheckTestScope(p, "src/main.go", true)
	if !r.Allowed {
		t.Error("non-test path must always be allowed")
	}
	if r.IsTest {
		t.Error("non-test path must report IsTest=false")
	}
}

func TestCheckFileChanges_BatchDenial(t *testing.T) {
	t.Parallel()
	p := Pipeline{Tests: TestsConfig{Generate: false}}
	changes := []FileChange{
		{Path: "src/main.go", IsNew: false},
		{Path: "src/foo_test.go", IsNew: true},
		{Path: "src/bar_test.go", IsNew: false},
	}
	r, denied := CheckFileChanges(p, changes)
	if denied != 2 {
		t.Errorf("denied = %d, want 2", denied)
	}
	if !r.IsTest || r.Allowed {
		t.Errorf("first denial should be a test-path denial: %+v", r)
	}
}

func TestNormalize_TestsGenerateFalseForcesModifyExisting(t *testing.T) {
	t.Parallel()
	p := Pipeline{Tests: TestsConfig{Generate: false, ModifyExisting: true, RunGenerated: true}}
	out, adj := Normalize(p)
	if out.Tests.ModifyExisting {
		t.Error("generate=false must force modify_existing=false (R6)")
	}
	if out.Tests.RunGenerated {
		t.Error("generate=false must force run_generated=false (R7)")
	}
	foundR6, foundR7 := false, false
	for _, a := range adj {
		if a.Field == "tests.modify_existing" {
			foundR6 = true
		}
		if a.Field == "tests.run_generated" {
			foundR7 = true
		}
	}
	if !foundR6 || !foundR7 {
		t.Errorf("expected R6 and R7 adjustments, got %+v", adj)
	}
}
