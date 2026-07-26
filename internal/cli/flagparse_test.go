package cli

import (
	"flag"
	"strings"
	"testing"
)

// TestReorderFlagsAndArgs_AllForms proves the helper supports interspersed
// flags and positionals for both canonical orderings (blocker 2 fix). This is
// the core of the regression guard: Go's flag package stops at the first
// positional, so without reordering `run <id> --engine x` silently keeps the
// default engine.
func TestReorderFlagsAndArgs_AllForms(t *testing.T) {
	tests := []struct {
		name string
		args []string
		// build a FlagSet whose shape mirrors `workspace run`
		wantEngine string
		wantJSON   bool
		wantModel  string
		wantID     string
		wantNArg   int
	}{
		{
			name:       "form-A id then flags",
			args:       []string{"ws-1", "--engine", "opencode", "--json"},
			wantEngine: "opencode", wantJSON: true, wantID: "ws-1", wantNArg: 1,
		},
		{
			name:       "form-B flags then id",
			args:       []string{"--engine", "opencode", "--json", "ws-1"},
			wantEngine: "opencode", wantJSON: true, wantID: "ws-1", wantNArg: 1,
		},
		{
			name:       "form-A with model and prompt (real E2E shape)",
			args:       []string{"ws-1", "--engine", "opencode", "--model", "zai-coding-plan/glm-5.2", "--prompt", "do it", "--json"},
			wantEngine: "opencode", wantModel: "zai-coding-plan/glm-5.2",
			wantJSON: true, wantID: "ws-1", wantNArg: 1,
		},
		{
			name:       "equals form interspersed",
			args:       []string{"ws-1", "--engine=opencode", "--json"},
			wantEngine: "opencode", wantJSON: true, wantID: "ws-1", wantNArg: 1,
		},
		{
			name:       "default engine when no flag (back-compat)",
			args:       []string{"ws-1"},
			wantEngine: "fake", wantID: "ws-1", wantNArg: 1,
		},
		{
			name:       "flag only, no id",
			args:       []string{"--engine", "opencode"},
			wantEngine: "opencode", wantID: "", wantNArg: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(&strings.Builder{})
			engine := fs.String("engine", "fake", "")
			model := fs.String("model", "", "")
			prompt := fs.String("prompt", "", "")
			jsonOut := fs.Bool("json", false, "")
			_ = prompt

			positional, err := parseWithPositionalReorder(fs, tc.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if *engine != tc.wantEngine {
				t.Errorf("engine = %q, want %q", *engine, tc.wantEngine)
			}
			if *jsonOut != tc.wantJSON {
				t.Errorf("json = %v, want %v", *jsonOut, tc.wantJSON)
			}
			if *model != tc.wantModel {
				t.Errorf("model = %q, want %q", *model, tc.wantModel)
			}
			if len(positional) != tc.wantNArg {
				t.Fatalf("positional len = %d, want %d (%v)", len(positional), tc.wantNArg, positional)
			}
			if tc.wantID != "" && (len(positional) == 0 || positional[0] != tc.wantID) {
				t.Errorf("positional[0] = %q, want %q", firstOrEmpty(positional), tc.wantID)
			}
		})
	}
}

// TestReorderFlagsAndArgs_UnknownFlagErrors proves an unknown flag is NOT
// silently swallowed: it is passed to fs.Parse which rejects it. This satisfies
// the "unknown flag → error" requirement.
func TestReorderFlagsAndArgs_UnknownFlagErrors(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	_ = fs.String("engine", "fake", "")

	_, err := parseWithPositionalReorder(fs, []string{"ws-1", "--bogus", "x"})
	if err == nil {
		t.Fatal("unknown flag --bogus was silently accepted; want parse error")
	}
}

// TestReorderFlagsAndArgs_PromptValueWithSpaces proves a prompt value
// containing spaces (and a prompt-file path with spaces) is not split — the
// shell delivers it as one arg and the helper keeps it as the flag's value.
func TestReorderFlagsAndArgs_PromptValueWithSpaces(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	prompt := fs.String("prompt", "", "")
	_ = fs.Bool("json", false, "")

	_, err := parseWithPositionalReorder(fs, []string{"ws-1", "--prompt", "hello world with spaces", "--json"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *prompt != "hello world with spaces" {
		t.Errorf("prompt = %q, want the full spaced string", *prompt)
	}
}

// TestReorderFlagsAndArgs_DoubleDashTerminator proves `--` stops flag scanning
// (so a positional that looks like a flag after `--` is treated as an id).
func TestReorderFlagsAndArgs_DoubleDashTerminator(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	engine := fs.String("engine", "fake", "")

	positional, err := parseWithPositionalReorder(fs, []string{"--engine", "opencode", "--", "ws-1"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *engine != "opencode" {
		t.Errorf("engine = %q, want opencode", *engine)
	}
	if len(positional) != 1 || positional[0] != "ws-1" {
		t.Errorf("positional = %v, want [ws-1]", positional)
	}
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
