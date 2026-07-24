package opencode

import (
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestBuildArgvBasic(t *testing.T) {
	a := New(Options{Binary: "/usr/bin/opencode", Agent: "build"})
	argv := a.buildArgv(protocol.AgentRunRequest{
		Workspace: "/ws", Model: "anthropic/claude-x", Prompt: "do the thing",
	}, false)
	want := []string{"/usr/bin/opencode", "run", "--format", "json", "--dir", "/ws", "--model", "anthropic/claude-x", "--agent", "build", "do the thing"}
	if !eqSlice(argv, want) {
		t.Errorf("argv = %v\nwant    %v", argv, want)
	}
}

func TestBuildArgvDeterministic(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	req := protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", Prompt: "hi"}
	first := a.buildArgv(req, false)
	second := a.buildArgv(req, false)
	if !eqSlice(first, second) {
		t.Errorf("argv not deterministic: %v vs %v", first, second)
	}
}

func TestBuildArgvResumesWithSession(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	argv := a.buildArgv(protocol.AgentRunRequest{
		Workspace: "/w", Model: "p/m", Prompt: "hi", SessionID: "sess-123",
	}, true)
	if !contains(argv, "--session", "sess-123") {
		t.Errorf("resume argv missing --session sess-123: %v", argv)
	}
}

func TestBuildArgvNoResumeWhenNoSession(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	argv := a.buildArgv(protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", Prompt: "hi"}, true)
	for i, v := range argv {
		if v == "--session" {
			t.Errorf("should not emit --session without id: %v (at %d)", argv, i)
		}
	}
}

func TestBuildArgvNeverShare(t *testing.T) {
	a := New(Options{Binary: "opencode", ExtraArgs: []string{"--auto"}})
	argv := a.buildArgv(protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", Prompt: "hi"}, false)
	for _, v := range argv {
		if v == "--share" || strings.Contains(v, "share") {
			t.Errorf("--share must NEVER appear: %v", argv)
		}
	}
}

func TestBuildArgvPromptFileFallback(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	argv := a.buildArgv(protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", PromptFile: "/p.txt"}, false)
	if !contains(argv, "/p.txt") {
		t.Errorf("prompt file not in argv: %v", argv)
	}
}

func TestBuildArgvNoShell(t *testing.T) {
	a := New(Options{Binary: "opencode"})
	argv := a.buildArgv(protocol.AgentRunRequest{Workspace: "/w", Model: "p/m", Prompt: "rm -rf /"}, false)
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

func TestBuildArgvWindowsSpacesAndUnicode(t *testing.T) {
	a := New(Options{Binary: `C:\Program Files\ünïcode\opencode.exe`})
	argv := a.buildArgv(protocol.AgentRunRequest{
		Workspace: `C:\My Workspace\proj`, Model: "p/m", Prompt: "héllo",
	}, false)
	if argv[0] != `C:\Program Files\ünïcode\opencode.exe` {
		t.Errorf("binary path mangled: %v", argv)
	}
	// Locate --dir and verify the following token is the workspace verbatim.
	found := false
	for i, v := range argv {
		if v == "--dir" && i+1 < len(argv) {
			found = true
			if argv[i+1] != `C:\My Workspace\proj` {
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
