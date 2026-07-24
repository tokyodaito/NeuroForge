package codex

import "testing"

func TestBuildExecArgvDefault(t *testing.T) {
	argv, err := buildExecArgv("/usr/local/bin/codex", "some-model", "fix the bug", nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"/usr/local/bin/codex", "exec",
		"--sandbox", "workspace-write",
		"--ask-for-approval", "never",
		"--model", "some-model",
		"fix the bug",
	}
	assertStringSlice(t, argv, want)
}

func TestBuildExecArgvCustomExecArgs(t *testing.T) {
	custom := []string{"--sandbox", "read-only"}
	argv, err := buildExecArgv("/bin/codex", "m", "do thing", custom, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"/bin/codex", "exec", "--sandbox", "read-only", "--model", "m", "do thing"}
	assertStringSlice(t, argv, want)
}

func TestBuildExecArgvNoModel(t *testing.T) {
	argv, err := buildExecArgv("/bin/codex", "", "hello", nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No --model selector when model empty: Codex falls back to its configured
	// default (rule §36.8: the adapter never injects a model name).
	for i, a := range argv {
		if a == "--model" {
			t.Fatalf("argv should not contain --model: %v (at %d)", argv, i)
		}
	}
	want := []string{"/bin/codex", "exec", "--sandbox", "workspace-write", "--ask-for-approval", "never", "hello"}
	assertStringSlice(t, argv, want)
}

func TestBuildExecArgvResume(t *testing.T) {
	argv, err := buildExecArgv("/bin/codex", "m", "", nil, true, "sess-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"/bin/codex", "exec",
		"--sandbox", "workspace-write",
		"--ask-for-approval", "never",
		"--resume", "sess-123",
		"--model", "m",
	}
	assertStringSlice(t, argv, want)
}

func TestBuildExecArgvResumeWithoutSessionOmitsFlag(t *testing.T) {
	argv, err := buildExecArgv("/bin/codex", "m", "", nil, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range argv {
		if a == "--resume" {
			t.Fatalf("argv should not contain --resume without a session: %v", argv)
		}
	}
}

func TestBuildExecArgvRequiresBinary(t *testing.T) {
	if _, err := buildExecArgv("  ", "m", "p", nil, false, ""); err == nil {
		t.Fatal("expected error for empty binary")
	}
}

func TestBuildExecArgvEmptyPromptOmitsPositional(t *testing.T) {
	// An empty prompt is translated faithfully: no trailing positional argument
	// is synthesized. The supervisor always supplies a prompt for real runs; the
	// adapter never fabricates one.
	argv, err := buildExecArgv("/bin/codex", "m", "", nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"/bin/codex", "exec", "--sandbox", "workspace-write", "--ask-for-approval", "never", "--model", "m"}
	assertStringSlice(t, argv, want)
}

func TestBuildExecArgvNeverEmitsDangerModeByDefault(t *testing.T) {
	// The default sandbox set must never include a privilege-bypass mode.
	argv, _ := buildExecArgv("/bin/codex", "m", "p", nil, false, "")
	joined := joinForAssert(argv)
	if contains(joined, "danger") || contains(joined, "full-access") {
		t.Fatalf("default argv must not enable a bypass mode: %v", argv)
	}
}

func TestBuildExecArgvPromptWithSpacesAndUnicodeIsOneArg(t *testing.T) {
	// argv-only (no shell): a prompt containing spaces and Unicode is a single
	// positional argument and needs no quoting.
	prompt := "Fix the café logger — naïve façade"
	argv, err := buildExecArgv("/bin/codex", "m", prompt, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argv[len(argv)-1] != prompt {
		t.Errorf("last argv = %q, want %q", argv[len(argv)-1], prompt)
	}
}

func TestVersionArgv(t *testing.T) {
	got := versionArgv("/bin/codex")
	assertStringSlice(t, got, []string{"/bin/codex", "--version"})
}

// helpers

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
}

func joinForAssert(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += " "
		}
		out += v
	}
	return out
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
