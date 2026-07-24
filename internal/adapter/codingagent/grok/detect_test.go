package grok

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// TestLookPathMissing verifies a missing binary is not found.
func TestLookPathMissing(t *testing.T) {
	_, err := lookPath("definitely-not-a-real-grok-binary-xyz")
	if err == nil {
		t.Fatal("expected not-found error for missing binary")
	}
}

// TestLookPathAbsPath finds a binary given by absolute/relative path.
func TestLookPathAbsPath(t *testing.T) {
	bin := stubBin(t)
	resolved, err := lookPath(bin)
	if err != nil {
		t.Fatalf("lookPath(abs) failed: %v", err)
	}
	if resolved == "" {
		t.Error("resolved path empty")
	}
}

// TestLookPathBareNameOnPATH puts the stub on PATH under its bare name and
// resolves it, including Windows extension trials.
func TestLookPathBareNameOnPATH(t *testing.T) {
	bin := stubBin(t)
	dir := filepath.Dir(bin)
	withPath(t, dir)

	resolved, err := lookPath("grokstub")
	if err != nil {
		t.Fatalf("bare-name lookup failed: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(resolved), strings.ToLower(filepath.Base(bin))) {
		t.Errorf("resolved %q does not end with the stub base name", resolved)
	}
}

// TestLookPathWindowsExtensionTrial verifies PATHEXT-style extension trials by
// creating a shim file with a .cmd extension on Windows (and a no-ext shim on
// unix) and resolving the bare name.
func TestLookPathWindowsExtensionTrial(t *testing.T) {
	dir := t.TempDir()
	withPath(t, dir)

	base := "grok-shim"
	if runtime.GOOS == "windows" {
		// Create a .cmd shim; bare "grok-shim" should resolve via PATHEXT.
		shim := filepath.Join(dir, base+".cmd")
		if err := os.WriteFile(shim, []byte("@echo off\necho grok version 0.9.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		shim := filepath.Join(dir, base)
		if err := os.WriteFile(shim, []byte("#!/bin/sh\necho grok version 0.9.0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	resolved, err := lookPath(base)
	if err != nil {
		t.Fatalf("extension trial failed: %v", err)
	}
	if !strings.HasPrefix(strings.ToLower(resolved), strings.ToLower(dir)) {
		t.Errorf("resolved %q not in shim dir %q", resolved, dir)
	}
}

// TestLookPathBatAndExeShims covers .bat and (on Windows) .exe resolution from
// PATHEXT, plus the npm-shim pattern (a .cmd wrapper). PATHEXT extension
// resolution is a Windows concern: on Unix a bare name is resolved by the exec
// bit, and .bat/.cmd/.exe shims are not executable, so the whole scenario is
// Windows-only.
func TestLookPathBatAndExeShims(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT .bat/.cmd/.exe shim resolution is Windows-specific")
	}
	dir := t.TempDir()
	withPath(t, dir)

	exts := []string{".bat"}
	if runtime.GOOS == "windows" {
		exts = append(exts, ".cmd", ".exe")
	}
	for _, ext := range exts {
		name := "groktool" + ext
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stub\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// "groktool" should resolve to one of the created extensions.
	resolved, err := lookPath("groktool")
	if err != nil {
		t.Fatalf("groktool not resolved: %v", err)
	}
	lower := strings.ToLower(resolved)
	if !(strings.HasSuffix(lower, ".bat") || strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".exe")) {
		t.Errorf("resolved %q did not use a PATHEXT extension", resolved)
	}
}

// TestLookPathUnicodeAndSpaces verifies paths with spaces and non-ASCII runes.
func TestLookPathUnicodeAndSpaces(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "projeto café ☕")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	binName := "grok unicode bin"
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	full := filepath.Join(nested, binName+ext)
	if err := os.WriteFile(full, []byte("stub\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := lookPath(full)
	if err != nil {
		t.Fatalf("unicode/spaces path not resolved: %v", err)
	}
	if !strings.EqualFold(resolved, full) {
		t.Errorf("resolved %q != %q", resolved, full)
	}
}

// TestDetectStubInstalled runs Detect against the stub and confirms it reports
// installed + caches a parseable version.
func TestDetectStubInstalled(t *testing.T) {
	a := New(stubOptions(t, "success"))
	res := a.Detect(testContext(t))
	if !res.Installed {
		t.Fatalf("stub not detected as installed: %+v", res)
	}
	if res.Version == "" {
		t.Error("version string empty")
	}
	v := a.versionSnapshot()
	if !v.known {
		t.Errorf("version not cached/parsed: %+v", v)
	}
}

func TestDetectMissingReportsNotInstalled(t *testing.T) {
	a := New(Options{Binary: "definitely-not-a-real-grok-binary-xyz"})
	res := a.Detect(testContext(t))
	if res.Installed {
		t.Errorf("missing binary reported installed: %+v", res)
	}
}

// TestHealthStatuses verifies ok/down transitions against the stub.
func TestHealthOKAgainstStub(t *testing.T) {
	a := New(stubOptions(t, "success"))
	hr := a.Health(testContext(t), protocol.Account{})
	if hr.Status != protocol.HealthOK {
		t.Errorf("status = %s, want ok (detail=%s)", hr.Status, hr.Detail)
	}
}

func TestHealthDownWhenMissing(t *testing.T) {
	a := New(Options{Binary: "definitely-not-a-real-grok-binary-xyz"})
	hr := a.Health(testContext(t), protocol.Account{})
	if hr.Status != protocol.HealthDown {
		t.Errorf("status = %s, want down", hr.Status)
	}
}

// withPath prepends dir to PATH for the duration of the test.
func withPath(t *testing.T, dir string) {
	t.Helper()
	old := os.Getenv("PATH")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+old)
	t.Cleanup(func() { os.Setenv("PATH", old) })
}
