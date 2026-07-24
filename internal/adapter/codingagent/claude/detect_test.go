package claude

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in  string
		maj int
		ok  bool
	}{
		{"2.1.205 (Claude Code)", 2, true},
		{"1.0.0", 1, true},
		{"claude version 2.1.0-beta", 2, true},
		{"not-a-version", 0, false},
		{"", 0, false},
		{"3", 0, false},
	}
	for _, c := range cases {
		pv, ok := parseVersion(c.in)
		if ok != c.ok {
			t.Errorf("parseVersion(%q) ok=%v want %v", c.in, ok, c.ok)
			continue
		}
		if ok && pv.Major != c.maj {
			t.Errorf("parseVersion(%q) major=%d want %d", c.in, pv.Major, c.maj)
		}
	}
}

func TestParsedVersionAtLeast(t *testing.T) {
	pv := parsedVersion{Major: 2, Minor: 1, Patch: 205, Full: "2.1.205"}
	if !pv.atLeast(2, 1, 0) {
		t.Error("2.1.205 should be >= 2.1.0")
	}
	if !pv.atLeast(2, 1, 205) {
		t.Error("2.1.205 should be >= 2.1.205")
	}
	if pv.atLeast(2, 2, 0) {
		t.Error("2.1.205 should not be >= 2.2.0")
	}
	if pv.atLeast(3, 0, 0) {
		t.Error("2.1.205 should not be >= 3.0.0")
	}
}

// TestDetectMissingExecutable verifies Detect reports not installed when the
// binary cannot be resolved and the probe is not invoked.
func TestDetectMissingExecutable(t *testing.T) {
	a, err := New(Options{
		BinaryPath: "",
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
	})
	if err != nil {
		t.Fatal(err)
	}
	res := a.Detect(context.Background())
	if res.Installed {
		t.Errorf("expected not installed, got %+v", res)
	}
}

// TestDetectVersionProbe verifies Detect parses the version probe output.
func TestDetectVersionProbe(t *testing.T) {
	a, err := New(Options{
		BinaryPath: "claude",
		LookPath:   func(string) (string, error) { return "/usr/bin/claude", nil },
		Probe: func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error) {
			return []byte("2.1.205 (Claude Code)\n"), nil, 0, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := a.Detect(context.Background())
	if !res.Installed {
		t.Fatalf("expected installed: %+v", res)
	}
	if res.Version != "2.1.205" {
		t.Errorf("Version = %q", res.Version)
	}
}

func TestDetectProbeNonZero(t *testing.T) {
	a, err := New(Options{
		BinaryPath: "claude",
		LookPath:   func(string) (string, error) { return "claude", nil },
		Probe: func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error) {
			return nil, []byte("broken"), 1, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Detect(context.Background()).Installed {
		t.Fatal("non-zero probe should not be installed")
	}
}

func TestVersionResultProtocolOne(t *testing.T) {
	a, err := New(Options{
		BinaryPath: "claude",
		LookPath:   func(string) (string, error) { return "claude", nil },
		Probe: func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error) {
			return []byte("2.1.205 (Claude Code)\n"), nil, 0, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	v := a.Version(context.Background())
	if v.ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1", v.ProtocolVersion)
	}
	if v.EngineVersion != "2.1.205" {
		t.Errorf("EngineVersion = %q", v.EngineVersion)
	}
	if v.AdapterVersion != DefaultAdapterVersion {
		t.Errorf("AdapterVersion = %q", v.AdapterVersion)
	}
}

func TestVersionResultWhenMissing(t *testing.T) {
	a, err := New(Options{LookPath: func(string) (string, error) { return "", os.ErrNotExist }})
	if err != nil {
		t.Fatal(err)
	}
	v := a.Version(context.Background())
	if v.ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d", v.ProtocolVersion)
	}
	if v.Error == "" {
		t.Errorf("expected non-empty Error")
	}
}

// ---- PATHEXT-aware LookPath ----

func TestPathExts(t *testing.T) {
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	exts := pathExts()
	want := []string{".COM", ".EXE", ".BAT", ".CMD"}
	if len(exts) != len(want) {
		t.Fatalf("pathExts = %v, want %v", exts, want)
	}
	for i := range want {
		if exts[i] != want[i] {
			t.Errorf("pathExts[%d] = %q, want %q", i, exts[i], want[i])
		}
	}
}

func TestCandidatePathsAppendsExtensions(t *testing.T) {
	t.Setenv("PATHEXT", ".EXE;.CMD")
	got := candidatePaths("claude")
	// base + .EXE + .CMD
	if len(got) != 3 {
		t.Fatalf("candidatePaths = %v", got)
	}
	if got[0] != "claude" {
		t.Errorf("first candidate = %q, want claude", got[0])
	}
}

func TestCandidatePathsSkipsExistingExtension(t *testing.T) {
	t.Setenv("PATHEXT", ".EXE;.CMD")
	got := candidatePaths("claude.exe")
	for _, p := range got {
		if strings.HasSuffix(p, ".exe.exe") {
			t.Errorf("duplicated extension: %q", p)
		}
	}
}

// TestSearchPathExtFindsCmdShim writes a fake claude.cmd on PATH and verifies
// the manual fallback resolves it (Windows .cmd / npm-shim case).
func TestSearchPathExtFindsShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT shim test is Windows-specific")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "claude.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	got, err := searchPathExt("claude")
	if err != nil {
		t.Fatalf("searchPathExt: %v", err)
	}
	if !strings.EqualFold(got, shim) {
		t.Errorf("searchPathExt = %q, want %q", got, shim)
	}
}

// TestSearchPathExtFindsBareShim verifies the unix/npm bare-shim case.
func TestSearchPathExtFindsBareShim(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "claude")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", "")
	got, err := searchPathExt("claude")
	if err != nil {
		t.Fatalf("searchPathExt: %v", err)
	}
	if got != shim {
		t.Errorf("searchPathExt = %q, want %q", got, shim)
	}
}

func TestSearchPathExtMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PATHEXT", ".EXE")
	if _, err := searchPathExt("definitely-not-here-xyz"); err == nil {
		t.Fatal("expected ErrNotExist")
	}
}

func TestDefaultLookPathFindsExec(t *testing.T) {
	// Use a command guaranteed to exist to validate defaultLookPath delegates.
	name := "go"
	if runtime.GOOS == "windows" {
		name = "where"
	}
	if _, err := defaultLookPath(name); err != nil {
		t.Fatalf("defaultLookPath(%s): %v", name, err)
	}
}

func TestDefaultLookPathMissing(t *testing.T) {
	if _, err := defaultLookPath("no-such-claude-binary-12345"); err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// TestBinaryPrefersExplicitPath verifies Options.BinaryPath bypasses LookPath.
func TestBinaryPrefersExplicitPath(t *testing.T) {
	a, _ := New(Options{
		BinaryPath: "/opt/claude",
		LookPath:   func(string) (string, error) { return "", errors.New("must not be called") },
	})
	got, err := a.binary()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/opt/claude" {
		t.Errorf("binary = %q", got)
	}
}

// TestDefaultProbeExecutesRealCmd sanity-checks the production probe against a
// known command.
func TestDefaultProbeExecutesRealCmd(t *testing.T) {
	var name string
	var args []string
	if runtime.GOOS == "windows" {
		name = "cmd"
		args = []string{"/c", "echo hello"}
	} else {
		name = "echo"
		args = []string{"hello"}
	}
	out, _, code, err := defaultProbe(context.Background(), name, args, nil)
	if err != nil {
		t.Fatalf("defaultProbe: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d", code)
	}
	if !strings.Contains(strings.ToLower(string(out)), "hello") {
		t.Errorf("output = %q", out)
	}
	// exec.LookPath sanity for the chosen command.
	if _, err := exec.LookPath(name); err != nil {
		t.Logf("note: %s resolved via shell", name)
	}
}
