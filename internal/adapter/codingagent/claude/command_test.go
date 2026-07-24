package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func newCmdAdapter(t *testing.T) *Adapter {
	t.Helper()
	a, err := New(Options{BinaryPath: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBuildArgvDeterministic(t *testing.T) {
	a := newCmdAdapter(t)
	req := protocol.AgentRunRequest{Model: "sonnet", TurnLimit: 5}
	first := a.buildArgv("claude", req, false)
	second := a.buildArgv("claude", req, false)
	if len(first) != len(second) {
		t.Fatalf("argv length differs")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("argv not deterministic at %d: %q vs %q", i, first[i], second[i])
		}
	}
}

func TestBuildArgvBaseShape(t *testing.T) {
	a := newCmdAdapter(t)
	argv := a.buildArgv("claude", protocol.AgentRunRequest{Model: "sonnet"}, false)
	if argv[0] != "claude" {
		t.Errorf("argv[0] = %q", argv[0])
	}
	if !argvContains(argv, "-p") {
		t.Errorf("missing -p: %v", argv)
	}
	if !argvContains(argv, "--output-format") {
		t.Errorf("missing --output-format: %v", argv)
	}
	if !argvContains(argv, "stream-json") {
		t.Errorf("missing stream-json value: %v", argv)
	}
	if !argvContains(argv, "--verbose") {
		t.Errorf("missing --verbose: %v", argv)
	}
}

func TestBuildArgvNeverIncludesBypass(t *testing.T) {
	a := newCmdAdapter(t)
	argv := a.buildArgv("claude", protocol.AgentRunRequest{Model: "sonnet"}, false)
	joined := strings.Join(argv, " ")
	for _, bad := range []string{"--dangerously-skip-permissions", "bypassPermissions", "--bare --dangerously"} {
		_ = bad
	}
	if strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Errorf("argv contains bypass flag: %s", joined)
	}
	if strings.Contains(joined, "bypassPermissions") {
		t.Errorf("argv contains bypassPermissions: %s", joined)
	}
}

func TestBuildArgvPermissionModeAlwaysPresent(t *testing.T) {
	a := newCmdAdapter(t)
	argv := a.buildArgv("claude", protocol.AgentRunRequest{Model: "sonnet"}, false)
	found := false
	for i, tok := range argv {
		if tok == "--permission-mode" && i+1 < len(argv) {
			if argv[i+1] != "default" {
				t.Errorf("permission mode = %q, want default", argv[i+1])
			}
			found = true
		}
	}
	if !found {
		t.Errorf("--permission-mode not present: %v", argv)
	}
}

func TestBuildArgvModelAndTurns(t *testing.T) {
	a := newCmdAdapter(t)
	argv := a.buildArgv("claude", protocol.AgentRunRequest{Model: "opus", TurnLimit: 7}, false)
	if !argvContains(argv, "--model") {
		t.Fatalf("missing --model: %v", argv)
	}
	if !argvContains(argv, "opus") {
		t.Errorf("missing model value: %v", argv)
	}
	if !argvContains(argv, "--max-turns") {
		t.Errorf("missing --max-turns: %v", argv)
	}
	if !argvContains(argv, "7") {
		t.Errorf("missing max-turns value: %v", argv)
	}
}

func TestBuildArgvOmitsTurnsWhenZero(t *testing.T) {
	a := newCmdAdapter(t)
	argv := a.buildArgv("claude", protocol.AgentRunRequest{Model: "sonnet", TurnLimit: 0}, false)
	if argvContains(argv, "--max-turns") {
		t.Errorf("max-turns should be omitted when 0: %v", argv)
	}
}

func TestBuildArgvResumeAddsResumeFlag(t *testing.T) {
	a := newCmdAdapter(t)
	req := protocol.AgentRunRequest{Model: "sonnet", SessionID: "sess-123"}
	argv := a.buildArgv("claude", req, true)
	if !argvContains(argv, "--resume") {
		t.Fatalf("missing --resume: %v", argv)
	}
	if !argvContains(argv, "sess-123") {
		t.Errorf("missing session id: %v", argv)
	}
}

func TestBuildArgvResumeWithoutSessionOmitsFlag(t *testing.T) {
	a := newCmdAdapter(t)
	argv := a.buildArgv("claude", protocol.AgentRunRequest{Model: "sonnet"}, true)
	if argvContains(argv, "--resume") {
		t.Errorf("resume without session should omit --resume: %v", argv)
	}
}

func TestBuildArgvBare(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude", Bare: true})
	argv := a.buildArgv("claude", protocol.AgentRunRequest{Model: "sonnet"}, false)
	if !argvContains(argv, "--bare") {
		t.Errorf("missing --bare: %v", argv)
	}
}

func TestBuildArgvEffortAndAddDirs(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude", Effort: "high", AdditionalDirs: []string{"../a", "../b"}})
	argv := a.buildArgv("claude", protocol.AgentRunRequest{Model: "sonnet"}, false)
	if !argvContains(argv, "--effort") {
		t.Errorf("missing --effort")
	}
	if !argvContains(argv, "high") {
		t.Errorf("missing effort value")
	}
	if !argvContains(argv, "--add-dir") {
		t.Errorf("missing --add-dir")
	}
	if !argvContains(argv, "../a") {
		t.Errorf("missing add-dir value ../a")
	}
}

func TestBuildArgvExtraArgsAppended(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude", ExtraArgs: []string{"--mcp-config", "{}"}})
	argv := a.buildArgv("claude", protocol.AgentRunRequest{Model: "sonnet"}, false)
	if !argvContains(argv, "--mcp-config") || !argvContains(argv, "{}") {
		t.Errorf("extra args not appended: %v", argv)
	}
}

// ---- env allowlist ----

func TestBuildEnvBaselineAndAllowlist(t *testing.T) {
	t.Setenv("PATH", "/bin:/usr/bin")
	t.Setenv("HOME", "/home/test")
	t.Setenv("MY_TOOL_VAR", "keep")
	env := buildEnv([]string{"MY_TOOL_VAR", "EXTRA=present"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "PATH=") {
		t.Errorf("baseline PATH missing: %s", joined)
	}
	if !strings.Contains(joined, "MY_TOOL_VAR=keep") {
		t.Errorf("allowlist MY_TOOL_VAR not copied: %s", joined)
	}
	if !strings.Contains(joined, "EXTRA=present") {
		t.Errorf("allowlist EXTRA not passed: %s", joined)
	}
}

func TestBuildEnvDropsDaemonAndVCSTokens(t *testing.T) {
	t.Setenv("FORGE_DAEMON_TOKEN", "super-secret-daemon")
	t.Setenv("GITHUB_TOKEN", "ghs_supersecret")
	t.Setenv("GITLAB_TOKEN", "glpt-secret")
	env := buildEnv([]string{
		"FORGE_DAEMON_TOKEN", "GITHUB_TOKEN=ghs_direct", "GITLAB_TOKEN=glpt_direct",
		"ANTHROPIC_API_KEY=sk-ant-provider-cred",
	})
	joined := strings.Join(env, "\n")
	for _, secret := range []string{"super-secret-daemon", "ghs_supersecret", "glpt-secret", "ghs_direct", "glpt_direct"} {
		if strings.Contains(joined, secret) {
			t.Errorf("forbidden token leaked into env: %q in:\n%s", secret, joined)
		}
	}
	// Provider credential intentionally allowlisted MUST survive (AC-28 scope is
	// daemon/VCS/merge tokens only).
	if !strings.Contains(joined, "ANTHROPIC_API_KEY=sk-ant-provider-cred") {
		t.Errorf("provider API key should survive allowlist: %s", joined)
	}
}

func TestBuildEnvCaseInsensitiveDedup(t *testing.T) {
	t.Setenv("PATH", "/first")
	env := buildEnv([]string{"path=/second"}) // lowercase duplicate
	// Whichever wins, there must be exactly one PATH-ish entry that is trusted.
	count := 0
	for _, e := range env {
		k := strings.SplitN(e, "=", 2)[0]
		if strings.EqualFold(k, "PATH") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 PATH entry, got %d: %v", count, env)
	}
}

// ---- prompt resolution ----

func TestResolvePromptInline(t *testing.T) {
	a := newCmdAdapter(t)
	got, err := a.resolvePrompt(protocol.AgentRunRequest{Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("prompt = %q", got)
	}
}

func TestResolvePromptFromFile(t *testing.T) {
	a := newCmdAdapter(t)
	dir := t.TempDir()
	pf := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(pf, []byte("file prompt body"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := a.resolvePrompt(protocol.AgentRunRequest{PromptFile: pf})
	if err != nil {
		t.Fatal(err)
	}
	if got != "file prompt body" {
		t.Errorf("prompt = %q", got)
	}
}

func TestResolvePromptEmpty(t *testing.T) {
	a := newCmdAdapter(t)
	got, err := a.resolvePrompt(protocol.AgentRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("empty prompt expected, got %q", got)
	}
}

func TestUseStdinDefault(t *testing.T) {
	a := newCmdAdapter(t)
	if !a.useStdin() {
		t.Error("default strategy should be stdin")
	}
	a2, _ := New(Options{BinaryPath: "claude", PromptStrategy: PromptPositional})
	if a2.useStdin() {
		t.Error("positional strategy should not use stdin")
	}
}
