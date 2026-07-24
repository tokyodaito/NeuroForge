package kimi

import (
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestBuildArgvBaseline(t *testing.T) {
	// Streaming + all flags supported (1.3.0 profile, fully probed).
	profile := newVersionProfile(parsedVersion{1, 3, 0, true}, false)
	pf := probedFlags{streamJSON: true, model: true, continued: true, maxTurns: true}
	spec := runSpec{prompt: "do thing", model: "kimi/default", turnLimit: 5, isResume: false}
	argv := buildArgv(spec, pf, profile)

	want := []string{"-p", "do thing", "--output", "stream-json", "--model", "kimi/default", "--max-turns", "5"}
	if !equalSlice(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
}

func TestBuildArgvResumeAddsContinue(t *testing.T) {
	profile := newVersionProfile(parsedVersion{1, 3, 0, true}, false)
	pf := probedFlags{streamJSON: true, model: true, continued: true, maxTurns: true}
	spec := runSpec{prompt: "x", model: "m", sessionID: "sess-1", isResume: true}
	argv := buildArgv(spec, pf, profile)

	if !contains(argv, "--continue") || !contains(argv, "sess-1") {
		t.Errorf("resume argv missing --continue sess-1: %v", argv)
	}
}

func TestBuildArgvOldVersionDropsUnsupportedFlags(t *testing.T) {
	// Old version: no streaming, no resume, no max-turns. Probed flags absent.
	profile := newVersionProfile(parsedVersion{0, 9, 0, true}, false)
	pf := probedFlags{} // not probed
	spec := runSpec{prompt: "x", model: "m", sessionID: "s", turnLimit: 3, isResume: true}
	argv := buildArgv(spec, pf, profile)

	// Only -p and --model (model selection is baseline) remain.
	want := []string{"-p", "x", "--model", "m"}
	if !equalSlice(argv, want) {
		t.Errorf("old-version argv = %v, want %v", argv, want)
	}
}

func TestBuildArgvProbedFlagOverridesVersion(t *testing.T) {
	// Probe says stream-json is supported even though version is old.
	profile := newVersionProfile(parsedVersion{0, 5, 0, true}, false)
	pf := probedFlags{streamJSON: true, model: true}
	spec := runSpec{prompt: "x", model: "m"}
	argv := buildArgv(spec, pf, profile)
	if !contains(argv, "--output") {
		t.Errorf("probed stream-json should be honoured: %v", argv)
	}
}

func TestBuildArgvExtraArgs(t *testing.T) {
	profile := newVersionProfile(parsedVersion{1, 3, 0, true}, false)
	pf := probedFlags{streamJSON: true, model: true}
	spec := runSpec{prompt: "x", extraArgs: []string{"--verbose"}}
	argv := buildArgv(spec, pf, profile)
	if !contains(argv, "--verbose") {
		t.Errorf("extra args not appended: %v", argv)
	}
}

func TestBuildArgvEmptyPromptUsesPlaceholder(t *testing.T) {
	profile := newVersionProfile(parsedVersion{1, 3, 0, true}, false)
	pf := probedFlags{streamJSON: true}
	argv := buildArgv(runSpec{}, pf, profile)
	if len(argv) < 2 || argv[0] != "-p" || argv[1] == "" {
		t.Errorf("empty prompt should yield -p <placeholder>: %v", argv)
	}
}

func TestBuildArgvDeterministic(t *testing.T) {
	// Same inputs → identical argv (no shell, no globbing, no env dependence).
	profile := newVersionProfile(parsedVersion{1, 3, 0, true}, false)
	pf := probedFlags{streamJSON: true, model: true, continued: true, maxTurns: true}
	spec := runSpec{prompt: "hello world", model: "m", sessionID: "s", turnLimit: 2, isResume: true}
	a := buildArgv(spec, pf, profile)
	b := buildArgv(spec, pf, profile)
	if !equalSlice(a, b) {
		t.Errorf("argv not deterministic: %v vs %v", a, b)
	}
}

func TestBuildArgvUnicodeAndSpaces(t *testing.T) {
	// Spaces, quotes and Unicode are passed verbatim as separate argv entries
	// (no shell quoting needed).
	profile := newVersionProfile(parsedVersion{1, 3, 0, true}, false)
	pf := probedFlags{streamJSON: true, model: true}
	spec := runSpec{prompt: "fix café — 'quotes' & spaces", model: "café/model"}
	argv := buildArgv(spec, pf, profile)
	if argv[1] != "fix café — 'quotes' & spaces" {
		t.Errorf("prompt not verbatim: %q", argv[1])
	}
	if !contains(argv, "café/model") {
		t.Errorf("unicode model not verbatim: %v", argv)
	}
}

func TestResolvePrompt(t *testing.T) {
	// Inline prompt wins.
	got, err := resolvePrompt(protocol.AgentRunRequest{Prompt: "inline"}, func(string) (string, error) {
		t.Fatal("should not read file when inline prompt present")
		return "", nil
	})
	if err != nil || got != "inline" {
		t.Fatalf("resolvePrompt inline = %q, %v", got, err)
	}
	// Prompt file read when no inline prompt.
	got, err = resolvePrompt(protocol.AgentRunRequest{PromptFile: "/p.txt"}, func(p string) (string, error) {
		if p != "/p.txt" {
			t.Errorf("read wrong path: %q", p)
		}
		return "from-file", nil
	})
	if err != nil || got != "from-file" {
		t.Fatalf("resolvePrompt file = %q, %v", got, err)
	}
	// Neither: empty.
	got, err = resolvePrompt(protocol.AgentRunRequest{}, nil)
	if err != nil || got != "" {
		t.Fatalf("resolvePrompt empty = %q, %v", got, err)
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

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
