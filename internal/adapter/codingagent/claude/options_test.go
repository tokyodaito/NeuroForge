package claude

import (
	"strings"
	"testing"
)

func TestNewAppliesDefaults(t *testing.T) {
	a, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.opts.PermissionMode != "default" {
		t.Errorf("PermissionMode = %q, want default", a.opts.PermissionMode)
	}
	if a.opts.AdapterVersion != DefaultAdapterVersion {
		t.Errorf("AdapterVersion = %q", a.opts.AdapterVersion)
	}
	if a.opts.PromptStrategy != PromptStdin {
		t.Errorf("PromptStrategy = %q", a.opts.PromptStrategy)
	}
	if a.opts.ProbeTimeout <= 0 {
		t.Errorf("ProbeTimeout not set")
	}
	if a.opts.LookPath == nil || a.opts.Probe == nil || a.opts.Spawn == nil || a.opts.Now == nil {
		t.Errorf("default seams not wired")
	}
	if len(a.opts.Models) == 0 {
		t.Errorf("default models not set")
	}
}

func TestNewRejectsBypassPermissionMode(t *testing.T) {
	if _, err := New(Options{PermissionMode: "bypassPermissions"}); err == nil {
		t.Fatal("expected error for bypassPermissions")
	}
}

func TestNewRejectsBadPermissionMode(t *testing.T) {
	if _, err := New(Options{PermissionMode: "weird"}); err == nil {
		t.Fatal("expected error for unsupported mode")
	}
}

func TestValidatePermissionModeAccepts(t *testing.T) {
	for _, m := range []string{"", "default", "acceptEdits", "plan", "auto", "dontAsk", "manual"} {
		if err := validatePermissionMode(m); err != nil {
			t.Errorf("validatePermissionMode(%q): %v", m, err)
		}
	}
}

func TestValidateExtraArgsRejectsDangerous(t *testing.T) {
	bad := []string{
		"--dangerously-skip-permissions",
		"--permission-mode",
		"--permission-mode=bypassPermissions",
		"--output-format",
		"--model",
		"--max-turns",
		"--resume",
		"-c",
		"-r",
		"--session-id",
		"bypassPermissions",
	}
	for _, a := range bad {
		if err := validateExtraArgs([]string{a}); err == nil {
			t.Errorf("expected rejection for %q", a)
		}
	}
}

func TestValidateExtraArgsAcceptsBenign(t *testing.T) {
	good := []string{"--add-dir", "/tmp/x", "--mcp-config", "{}", "--append-system-prompt", "be helpful"}
	if err := validateExtraArgs(good); err != nil {
		t.Errorf("validateExtraArgs(%v): %v", good, err)
	}
}

func TestValidateEffort(t *testing.T) {
	for _, e := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		if err := validateEffort(e); err != nil {
			t.Errorf("effort %q: %v", e, err)
		}
	}
	for _, e := range []string{"nope", "ULTRA"} {
		if err := validateEffort(e); err == nil {
			t.Errorf("expected rejection for effort %q", e)
		}
	}
}

func TestForbiddenArgTokensCoverBypass(t *testing.T) {
	joined := strings.Join(forbiddenArgTokens, ",")
	for _, must := range []string{"--dangerously-skip-permissions", "bypassPermissions", "--permission-mode"} {
		if !strings.Contains(joined, must) {
			t.Errorf("forbiddenArgTokens missing %q", must)
		}
	}
}

func TestOptionsArtifactsDirDefaultsToTempDir(t *testing.T) {
	o := Options{}
	if got := o.artifactsDir(); got == "" {
		t.Fatal("artifactsDir empty")
	}
	o.ArtifactsDir = "  /custom/dir  "
	if got := o.artifactsDir(); !strings.HasSuffix(got, "custom/dir") {
		t.Errorf("artifactsDir = %q", got)
	}
}
