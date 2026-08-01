package claude

import (
	"context"
	"errors"
	"os"
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

// ---- LookPath ----

func TestDefaultLookPathFindsExec(t *testing.T) {
	// Use a command guaranteed to exist to validate defaultLookPath delegates.
	if _, err := defaultLookPath("go"); err != nil {
		t.Fatalf("defaultLookPath(go): %v", err)
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
	out, _, code, err := defaultProbe(context.Background(), "echo", []string{"hello"}, nil)
	if err != nil {
		t.Fatalf("defaultProbe: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d", code)
	}
	if !strings.Contains(strings.ToLower(string(out)), "hello") {
		t.Errorf("output = %q", out)
	}
}
