package gemini

import (
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestBuildRunSpecInlinePrompt(t *testing.T) {
	req := protocol.AgentRunRequest{Prompt: "write a test"}
	spec := buildRunSpec("/bin/gemini", req, nil)
	got := spec.argv0()
	want := []string{"/bin/gemini", "-p", "write a test", "-o", "json"}
	if !equalSlice(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
	if spec.promptFile != "" {
		t.Errorf("promptFile = %q, want empty", spec.promptFile)
	}
}

func TestBuildRunSpecWithModel(t *testing.T) {
	req := protocol.AgentRunRequest{Prompt: "hi", Model: "some-model-id"}
	spec := buildRunSpec("/bin/gemini", req, nil)
	got := strings.Join(spec.argv, " ")
	if !strings.Contains(got, "-m some-model-id") {
		t.Errorf("argv missing model: %s", got)
	}
}

func TestBuildRunSpecPromptFileUsesStdin(t *testing.T) {
	// PromptFile must NOT be passed via argv; it is piped to stdin so large
	// prompts never overflow argv and never touch a shell.
	req := protocol.AgentRunRequest{PromptFile: "/tmp/prompt.txt"}
	spec := buildRunSpec("/bin/gemini", req, nil)
	for _, a := range spec.argv {
		if strings.Contains(a, "prompt.txt") {
			t.Errorf("prompt file leaked into argv: %v", spec.argv)
		}
		if a == "-p" {
			t.Errorf("-p should be omitted when piping via stdin: %v", spec.argv)
		}
	}
	if spec.promptFile != "/tmp/prompt.txt" {
		t.Errorf("promptFile = %q", spec.promptFile)
	}
}

func TestBuildRunSpecNoUnsafeDefaults(t *testing.T) {
	// The adapter must NEVER add YOLO / auto-approve / unrestricted modes by
	// default (task constraint, spec §29).
	req := protocol.AgentRunRequest{Prompt: "x", Model: "m"}
	spec := buildRunSpec("/bin/gemini", req, nil)
	joined := strings.Join(spec.argv, " ")
	for _, bad := range []string{"--yolo", "-y", "yolo", "--approval-mode", "auto_edit", "--all-files"} {
		if strings.Contains(joined, bad) {
			t.Errorf("unsafe default flag %q present in argv: %s", bad, joined)
		}
	}
}

func TestBuildRunSpecExtraArgsAppended(t *testing.T) {
	req := protocol.AgentRunRequest{Prompt: "x"}
	spec := buildRunSpec("/bin/gemini", req, []string{"--foo", "bar"})
	if !endsWith(spec.argv, "--foo", "bar") {
		t.Errorf("extra args not appended: %v", spec.argv)
	}
}

func TestBuildRunSpecDeterministic(t *testing.T) {
	// Same request → identical argv (determinism; no shell, no env leakage).
	req := protocol.AgentRunRequest{Prompt: "p", Model: "m"}
	a := buildRunSpec("/bin/gemini", req, nil).argv0()
	b := buildRunSpec("/bin/gemini", req, nil).argv0()
	if !equalSlice(a, b) {
		t.Errorf("non-deterministic: %v vs %v", a, b)
	}
}

func equalSlice(a, b []string) bool {
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

func endsWith(s []string, last ...string) bool {
	if len(s) < len(last) {
		return false
	}
	off := len(s) - len(last)
	for i, v := range last {
		if s[off+i] != v {
			return false
		}
	}
	return true
}
