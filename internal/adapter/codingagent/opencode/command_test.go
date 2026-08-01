package opencode

import (
	"context"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestBuildArgvBasic(t *testing.T) {
	a := New(Options{Binary: "/usr/bin/opencode", Agent: "build"})
	argv := mustArgv(t, a, protocol.AgentRunRequest{
		Workspace: "/ws", Model: "anthropic/claude-x", Prompt: "do the thing",
	}, false)
	want := []string{"/usr/bin/opencode", "run", "--format", "json", "--dir", "/ws", "--model", "anthropic/claude-x", "--agent", "build", "--", "do the thing"}
	if !eqSlice(argv, want) {
		t.Errorf("argv = %v\nwant    %v", argv, want)
	}
}

func TestBuildArgvDeterministic(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	req := protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", Prompt: "hi"}
	first := mustArgv(t, a, req, false)
	second := mustArgv(t, a, req, false)
	if !eqSlice(first, second) {
		t.Errorf("argv not deterministic: %v vs %v", first, second)
	}
}

func TestBuildArgvResumesWithSession(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	argv := mustArgv(t, a, protocol.AgentRunRequest{
		Workspace: "/w", Model: "p/m", Prompt: "hi", SessionID: "sess-123",
	}, true)
	if !contains(argv, "--session", "sess-123") {
		t.Errorf("resume argv missing --session sess-123: %v", argv)
	}
}

func TestBuildArgvNoResumeWhenNoSession(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	argv := mustArgv(t, a, protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", Prompt: "hi"}, true)
	for i, v := range argv {
		if v == "--session" {
			t.Errorf("should not emit --session without id: %v (at %d)", argv, i)
		}
	}
}

func TestBuildArgvNeverShare(t *testing.T) {
	a := New(Options{Binary: "opencode", ExtraArgs: []string{"--auto"}})
	argv := mustArgv(t, a, protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", Prompt: "hi"}, false)
	for _, v := range argv {
		if v == "--share" || strings.Contains(v, "share") {
			t.Errorf("--share must NEVER appear: %v", argv)
		}
	}
}

func TestBuildArgvPromptFileFallback(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	argv := mustArgv(t, a, protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", PromptFile: "/p.txt"}, false)
	if !contains(argv, "/p.txt") {
		t.Errorf("prompt file not in argv: %v", argv)
	}
}

func TestBuildArgvNoShell(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	argv := mustArgv(t, a, protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", Prompt: "rm -rf /"}, false)
	// argv must be a real token slice, never a shell string with /bin/sh or cmd /c.
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "/bin/sh") || strings.Contains(joined, "cmd /c") {
		t.Errorf("argv must not shell out: %v", argv)
	}
	// The prompt survives as a single token even with shell metacharacters.
	if argv[len(argv)-1] != "rm -rf /" {
		t.Errorf("prompt token split: %v", argv)
	}
}

func TestBuildArgvSpacesAndUnicode(t *testing.T) {
	a := New(Options{Binary: `/opt/ünïcode dir/opencode`})
	argv := mustArgv(t, a, protocol.AgentRunRequest{
		Workspace: `/home/me/My Workspace/proj`, Model: "p/m", Prompt: "héllo",
	}, false)
	if argv[0] != `/opt/ünïcode dir/opencode` {
		t.Errorf("binary path mangled: %v", argv)
	}
	// Locate --dir and verify the following token is the workspace verbatim.
	found := false
	for i, v := range argv {
		if v == "--dir" && i+1 < len(argv) {
			found = true
			if argv[i+1] != `/home/me/My Workspace/proj` {
				t.Errorf("workspace path mangled: got %q in %v", argv[i+1], argv)
			}
		}
	}
	if !found {
		t.Errorf("--dir missing from argv: %v", argv)
	}
	if argv[len(argv)-1] != "héllo" {
		t.Errorf("unicode prompt mangled: %v", argv)
	}
}

func TestBuildEnvAllowlistOnly(t *testing.T) {
	t.Setenv("FORGE_DAEMON_TOKEN", "super-secret-daemon")
	t.Setenv("GITHUB_TOKEN", "ghp_secrettokenvalue")
	t.Setenv("MY_ALLOW", "ok-val")
	env := buildEnv([]string{"MY_ALLOW", "EXTRA=k=v"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "super-secret-daemon") {
		t.Errorf("daemon token leaked:\n%s", joined)
	}
	if strings.Contains(joined, "ghp_secrettokenvalue") {
		t.Errorf("github token leaked:\n%s", joined)
	}
	if !strings.Contains(joined, "MY_ALLOW=ok-val") {
		t.Errorf("allowlist KEY copy missing:\n%s", joined)
	}
	if !strings.Contains(joined, "EXTRA=k=v") {
		t.Errorf("allowlist KEY=VAL missing:\n%s", joined)
	}
	if !strings.Contains(joined, "PATH=") {
		t.Errorf("PATH missing:\n%s", joined)
	}
}

func TestBuildEnvDropsForbiddenEvenInAllowlist(t *testing.T) {
	t.Setenv("FORGE_DAEMON_TOKEN", "leak")
	// Even if the supervisor (mis)allowlists it, the adapter drops it.
	env := buildEnv([]string{"FORGE_DAEMON_TOKEN"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "leak") {
		t.Errorf("forbidden key forwarded despite defense-in-depth:\n%s", joined)
	}
}

func TestBuildEnvCaseInsensitive(t *testing.T) {
	t.Setenv("MyPath2", "v")
	env := buildEnv([]string{"MYPATH2"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "v") {
		t.Errorf("case-insensitive key copy failed:\n%s", joined)
	}
}

// mustArgv builds argv or fails the test on a validation error.
func mustArgv(t *testing.T, a *Adapter, req protocol.AgentRunRequest, isResume bool) []string {
	t.Helper()
	argv, err := a.buildArgv(req, isResume)
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	return argv
}

// helpers
func eqSlice(a, b []string) bool {
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

func contains(s []string, kv ...string) bool {
	for i := 0; i+len(kv) <= len(s); i++ {
		match := true
		for j := range kv {
			if s[i+j] != kv[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestBuildArgvRejectsFlagInjection (M4): model and session id values
// beginning with '-' would be parsed as flags by the opencode CLI (option
// injection) and must be rejected with a clear error before spawn.
func TestBuildArgvRejectsFlagInjection(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	if _, err := a.buildArgv(protocol.AgentRunRequest{Workspace: "/w", Model: "--help", Prompt: "hi"}, false); err == nil {
		t.Error("model starting with '-' must be rejected")
	}
	if _, err := a.buildArgv(protocol.AgentRunRequest{
		Workspace: "/w", Model: "p/m", Prompt: "hi", SessionID: "--session=other",
	}, true); err == nil {
		t.Error("session id starting with '-' must be rejected on resume")
	}
	// The session id is only validated when actually used (resume).
	if _, err := a.buildArgv(protocol.AgentRunRequest{
		Workspace: "/w", Model: "p/m", Prompt: "hi", SessionID: "",
	}, true); err != nil {
		t.Errorf("empty session id on resume must not error: %v", err)
	}
}

// TestBuildArgvDashLeadingPrompt (N4): a prompt beginning with '-' must reach
// the opencode CLI verbatim as the message positional — never parsed as a
// flag. The `--` end-of-options separator (supported by opencode's yargs
// parser, verified against 1.18.11) guarantees that.
func TestBuildArgvDashLeadingPrompt(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	argv := mustArgv(t, a, protocol.AgentRunRequest{
		Workspace: "/w", Model: "p/m", Prompt: "--help me fix this",
	}, false)
	// The prompt follows the `--` separator as the final token, verbatim.
	if len(argv) < 2 || argv[len(argv)-2] != "--" || argv[len(argv)-1] != "--help me fix this" {
		t.Errorf("dash-leading prompt not shielded by --: %v", argv)
	}
	// Same for the prompt-file fallback.
	argv = mustArgv(t, a, protocol.AgentRunRequest{
		Workspace: "/w", Model: "p/m", PromptFile: "-weird-name.txt",
	}, false)
	if len(argv) < 2 || argv[len(argv)-2] != "--" || argv[len(argv)-1] != "-weird-name.txt" {
		t.Errorf("dash-leading prompt file not shielded by --: %v", argv)
	}
}

// TestResolvedBinaryUsesDetectedPath (L5): without Options.Binary, spawn must
// use the absolute path Detect resolved (cached), not the bare PATH name.
func TestResolvedBinaryUsesDetectedPath(t *testing.T) {
	a := New(Options{})
	a.lookPath = func(string) (string, error) { return "/resolved/abs/opencode", nil }
	a.runProbe = func(context.Context, string) (string, string, error) { return "opencode 0.1.48", "", nil }

	// Before Detect: a fresh lookup resolves the absolute path.
	if got := a.resolvedBinary(); got != "/resolved/abs/opencode" {
		t.Errorf("resolvedBinary before Detect = %q, want the looked-up absolute path", got)
	}
	// After Detect: the cached detection path is used.
	res := a.Detect(context.Background())
	if !res.Installed || res.Path != "/resolved/abs/opencode" {
		t.Fatalf("Detect = %+v", res)
	}
	argv := mustArgv(t, a, protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", Prompt: "hi"}, false)
	if argv[0] != "/resolved/abs/opencode" {
		t.Errorf("argv[0] = %q, want the detected absolute path (L5)", argv[0])
	}
}
