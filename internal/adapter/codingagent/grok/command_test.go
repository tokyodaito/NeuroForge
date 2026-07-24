package grok

import (
	"reflect"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func baseCaps() protocol.AgentCapabilities {
	return protocol.AgentCapabilities{
		HeadlessMode:    true,
		StreamingEvents: true,
		ModelSelection:  true,
		SessionResume:   true,
	}
}

func TestBuildArgvCoreAlwaysPresent(t *testing.T) {
	argv := buildArgv("grok", protocol.AgentRunRequest{}, baseCaps(), false)
	want := []string{"grok", "--no-auto-update", "-p", "--output-format", "streaming-json"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
}

func TestBuildArgvDeterministic(t *testing.T) {
	req := protocol.AgentRunRequest{
		Model:     "provider/coding-1",
		Prompt:    "do the thing",
		TurnLimit: 8,
		SessionID: "sess-1",
	}
	caps := baseCaps()
	a := buildArgv("/usr/local/bin/grok", req, caps, true)
	b := buildArgv("/usr/local/bin/grok", req, caps, true)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("buildArgv is not deterministic: %v vs %v", a, b)
	}
	want := []string{
		"/usr/local/bin/grok", "--no-auto-update", "-p", "--output-format", "streaming-json",
		"--model", "provider/coding-1",
		"--resume", "sess-1",
		"--max-turns", "8",
		"do the thing",
	}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("argv = %v, want %v", a, want)
	}
}

func TestBuildArgvGatedFlags(t *testing.T) {
	caps := baseCaps()

	// No model → no --model.
	argv := buildArgv("grok", protocol.AgentRunRequest{Prompt: "x"}, caps, false)
	if contains(argv, "--model") {
		t.Errorf("--model emitted without a model: %v", argv)
	}

	// ModelSelection off → no --model even when Model set.
	capsNoSel := caps
	capsNoSel.ModelSelection = false
	argv = buildArgv("grok", protocol.AgentRunRequest{Model: "m"}, capsNoSel, false)
	if contains(argv, "--model") {
		t.Errorf("--model emitted when ModelSelection=false: %v", argv)
	}

	// SessionResume off → no --resume even when SessionID set.
	capsNoRes := caps
	capsNoRes.SessionResume = false
	argv = buildArgv("grok", protocol.AgentRunRequest{Model: "m", SessionID: "s"}, capsNoRes, false)
	if contains(argv, "--resume") {
		t.Errorf("--resume emitted when SessionResume=false: %v", argv)
	}

	// TurnLimit disabled → no --max-turns.
	argv = buildArgv("grok", protocol.AgentRunRequest{Model: "m", TurnLimit: 5}, caps, false)
	if contains(argv, "--max-turns") {
		t.Errorf("--max-turns emitted when turn-limit disabled: %v", argv)
	}
}

func TestBuildArgvPromptFileWinsOverPrompt(t *testing.T) {
	req := protocol.AgentRunRequest{Prompt: "inline", PromptFile: "/tmp/p.txt"}
	argv := buildArgv("grok", req, baseCaps(), false)
	if !contains(argv, "/tmp/p.txt") {
		t.Errorf("PromptFile path not in argv: %v", argv)
	}
	if contains(argv, "inline") {
		t.Errorf("inline prompt should not appear when PromptFile set: %v", argv)
	}
}

func TestBuildArgvNoShell(t *testing.T) {
	// argv-only: spaces and shell metacharacters are passed verbatim, never
	// quoted into a shell string.
	req := protocol.AgentRunRequest{Prompt: "hello world; rm -rf /"}
	argv := buildArgv("grok", req, baseCaps(), false)
	if len(argv) == 0 || argv[len(argv)-1] != "hello world; rm -rf /" {
		t.Errorf("prompt not passed verbatim as last argv element: %v", argv)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
