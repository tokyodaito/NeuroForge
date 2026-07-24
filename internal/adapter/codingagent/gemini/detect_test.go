package gemini

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

var errBoom = errors.New("boom")

func TestParseGeminiVersion(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"0.23.0", "0.23.0", true},
		{"v0.1.0\n", "0.1.0", true},
		{"@google/gemini-cli/0.23.0\r\n", "0.23.0", true},
		{"Gemini CLI version: 1.2.3 (build 4)", "1.2.3", true},
		{"", "", false},
		{"no version here", "", false},
		{"1", "", false},
	}
	for _, c := range cases {
		v, err := parseGeminiVersion(c.in)
		if c.wantOK {
			if err != nil {
				t.Errorf("parseGeminiVersion(%q) err=%v, want nil", c.in, err)
				continue
			}
			if v.String() != c.want {
				t.Errorf("parseGeminiVersion(%q) = %q, want %q", c.in, v.String(), c.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("parseGeminiVersion(%q) want error, got %q", c.in, v.String())
		}
	}
}

func TestSemverAtLeast(t *testing.T) {
	v := semver{major: 0, minor: 23, patch: 0, raw: "0.23.0"}
	if !v.atLeast(0, 1, 0) {
		t.Error("0.23.0 should be >= 0.1.0")
	}
	if v.atLeast(0, 24, 0) {
		t.Error("0.23.0 should not be >= 0.24.0")
	}
	if !v.atLeast(0, 23, 0) {
		t.Error("0.23.0 should be >= 0.23.0")
	}
}

func TestDetectInstalled(t *testing.T) {
	h := &stubHost{
		lookPathFn: func(string) (string, error) { return "/bin/gemini", nil },
		probeFn:    func(context.Context, []string, []string) (string, string, error) { return "0.23.0\n", "", nil },
	}
	a := newTestAdapter(h)
	d := a.Detect(context.Background())
	if !d.Installed {
		t.Fatalf("not installed: %+v", d)
	}
	if d.Path != "/bin/gemini" {
		t.Errorf("path = %s", d.Path)
	}
	if d.Version != "0.23.0" {
		t.Errorf("version = %s", d.Version)
	}
}

func TestDetectNotInstalled(t *testing.T) {
	h := &stubHost{
		lookPathFn: func(string) (string, error) { return "", &notFoundError{name: "gemini"} },
	}
	a := newTestAdapter(h)
	d := a.Detect(context.Background())
	if d.Installed {
		t.Fatalf("should not be installed: %+v", d)
	}
	if !strings.Contains(d.Detail, "not found") {
		t.Errorf("detail should mention not found: %s", d.Detail)
	}
}

func TestDetectProbeNonZeroStillInstalled(t *testing.T) {
	// Binary exists but --version exits non-zero: still installed, version
	// surfaced where possible, detail carries the diagnostic.
	h := &stubHost{
		lookPathFn: func(string) (string, error) { return "/bin/gemini", nil },
		probeFn: func(context.Context, []string, []string) (string, string, error) {
			return "0.23.0", "warn", errBoom
		},
	}
	a := newTestAdapter(h)
	d := a.Detect(context.Background())
	if !d.Installed {
		t.Fatalf("should be installed despite probe error: %+v", d)
	}
	if d.Version != "0.23.0" {
		t.Errorf("version = %s, want 0.23.0", d.Version)
	}
}

func TestVersionReportsProtocolOne(t *testing.T) {
	h := &stubHost{
		probeFn: func(context.Context, []string, []string) (string, string, error) { return "0.23.0", "", nil },
	}
	a := newTestAdapter(h)
	v := a.Version(context.Background())
	if v.ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1", v.ProtocolVersion)
	}
	if v.EngineVersion != "0.23.0" {
		t.Errorf("EngineVersion = %s", v.EngineVersion)
	}
	if v.AdapterVersion == "" {
		t.Error("AdapterVersion empty")
	}
}

func TestHealthInstalledUnknown(t *testing.T) {
	// Installed but account reachability unverifiable offline → Unknown.
	h := &stubHost{
		lookPathFn: func(string) (string, error) { return "/bin/gemini", nil },
		probeFn:    func(context.Context, []string, []string) (string, string, error) { return "0.23.0", "", nil },
	}
	a := newTestAdapter(h)
	hr := a.Health(context.Background(), protocol.Account{})
	if hr.Status != "unknown" {
		t.Errorf("Health = %s, want unknown (no paid probe)", hr.Status)
	}
}

func TestHealthNotInstalledDown(t *testing.T) {
	h := &stubHost{
		lookPathFn: func(string) (string, error) { return "", &notFoundError{name: "gemini"} },
	}
	a := newTestAdapter(h)
	hr := a.Health(context.Background(), protocol.Account{})
	if hr.Status != "down" {
		t.Errorf("Health = %s, want down", hr.Status)
	}
}

func TestLookPathFindsCmdShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT/shim test is Windows-specific")
	}
	dir := t.TempDir()
	// Simulate an npm install: a .cmd shim and a .ps1 script. lookPath must pick
	// the .cmd (PowerShell-only .ps1 cannot be spawned by os/exec).
	if err := os.WriteFile(filepath.Join(dir, "gemini.cmd"), []byte("@echo off"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gemini.ps1"), []byte("# ps1"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := lookPath("gemini")
	if err != nil {
		t.Fatalf("lookPath: %v", err)
	}
	// PATHEXT extensions are uppercased on Windows; compare case-insensitively.
	if !strings.EqualFold(filepath.Ext(got), ".cmd") {
		t.Errorf("lookPath picked %s, want the .cmd shim", got)
	}
}

func TestLookPathMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := lookPath("definitely-not-present-binary")
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestLookPathPATHEXTOrdering(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT ordering is Windows-specific")
	}
	dir := t.TempDir()
	// Provide both .exe and .cmd; with default PATHEXT order (.COM;.EXE;.BAT;.CMD)
	// the .exe should win.
	if err := os.WriteFile(filepath.Join(dir, "mytool.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mytool.cmd"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	got, err := lookPath("mytool")
	if err != nil {
		t.Fatalf("lookPath: %v", err)
	}
	if !strings.EqualFold(filepath.Ext(got), ".exe") {
		t.Errorf("want .exe to win PATHEXT ordering, got %s", got)
	}
}

func TestLookPathUnicodeAndSpaces(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "with space", "Ünïcödé")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	binName := "mybin"
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".cmd"
	}
	full := filepath.Join(nested, binName+suffix)
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", nested)
	got, err := lookPath("mybin")
	if err != nil {
		t.Fatalf("lookPath in unicode/spaces dir: %v", err)
	}
	if !strings.EqualFold(got, full) {
		t.Errorf("got %s, want %s", got, full)
	}
}
