package kimi

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// copyStubTo copies the built kimistub binary to dir under name, so it can be
// found on PATH.
func copyStubTo(t *testing.T, dir, name string) string {
	t.Helper()
	stub := buildStub(t)
	dst := filepath.Join(dir, name)
	in, err := os.ReadFile(stub)
	if err != nil {
		t.Fatalf("read stub: %v", err)
	}
	if err := os.WriteFile(dst, in, 0o755); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	return dst
}

// withPath prepends dir to PATH for the duration of the test (LookPath honours
// the live environment).
func withPath(t *testing.T, dir string) {
	t.Helper()
	cur := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+cur)
}

func TestDetectViaPATH(t *testing.T) {
	// A bare executable on PATH (kimi on unix, kimi.exe on Windows) is found by
	// exec.LookPath. (The version probe does not receive ExtraEnv, so the stub
	// reports its default version — we assert presence, not the override.)
	dir := t.TempDir()
	name := "kimi"
	if runtime.GOOS == "windows" {
		name = "kimi.exe"
	}
	copyStubTo(t, dir, name)
	withPath(t, dir)

	a := New(Options{BinaryName: "kimi"})
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf("PATH detection failed: %+v", d)
	}
	if !strings.Contains(d.Version, "1.4.0") {
		t.Errorf("version = %q, want the stub default 1.4.0", d.Version)
	}
}

func TestDetectNotOnPath(t *testing.T) {
	a := New(Options{BinaryName: "kimi-not-real-zzz-999"})
	if d := a.Detect(testContext()); d.Installed {
		t.Errorf("should not be installed: %+v", d)
	}
}

func TestDetectSpacesInPath(t *testing.T) {
	// A binary living in a directory whose path contains spaces is found and
	// invoked correctly (argv-only, no shell quoting needed).
	dir := filepath.Join(t.TempDir(), "dir with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "kimi"
	if runtime.GOOS == "windows" {
		name = "kimi.exe"
	}
	copyStubTo(t, dir, name)
	withPath(t, dir)

	a := New(Options{BinaryName: "kimi", ExtraEnv: []string{"KIMI_STUB_VERSION=2.0.0"}})
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf("spaces-in-path detection failed: %+v", d)
	}
}

func TestDetectUnicodePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "café-測試-Λ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "kimi"
	if runtime.GOOS == "windows" {
		name = "kimi.exe"
	}
	copyStubTo(t, dir, name)
	withPath(t, dir)

	a := New(Options{BinaryName: "kimi"})
	if d := a.Detect(testContext()); !d.Installed {
		t.Errorf("unicode-path detection failed: %+v", d)
	}
}

// TestDetectCMDShim verifies exec.LookPath + PATHEXT resolution of an npm-style
// .cmd shim on Windows. On non-Windows there is no PATHEXT, so the same code
// path is exercised via the plain-PATH test above.
func TestDetectCMDShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT .cmd shims are a Windows concern")
	}
	stub := buildStub(t)
	dir := t.TempDir()
	shim := filepath.Join(dir, "kimi.cmd")
	// A minimal npm-style shim forwarding all args to the real binary.
	body := "@echo off\r\n\"" + stub + "\" %*\r\n"
	if err := os.WriteFile(shim, []byte(body), 0o644); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	withPath(t, dir)

	a := New(Options{BinaryName: "kimi"})
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf(".cmd shim not detected: %+v", d)
	}
	if !strings.Contains(d.Version, "1.4.0") {
		t.Errorf(".cmd shim version = %q, want the stub default 1.4.0", d.Version)
	}
}

// TestDetectBATShim verifies .bat shim resolution via PATHEXT on Windows.
func TestDetectBATShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT .bat shims are a Windows concern")
	}
	stub := buildStub(t)
	dir := t.TempDir()
	shim := filepath.Join(dir, "kimi.bat")
	body := "@echo off\r\n\"" + stub + "\" %*\r\n"
	if err := os.WriteFile(shim, []byte(body), 0o644); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	withPath(t, dir)

	a := New(Options{BinaryName: "kimi"})
	if d := a.Detect(testContext()); !d.Installed {
		t.Errorf(".bat shim not detected: %+v", d)
	}
}

func TestDetectPATHEXTOrdering(t *testing.T) {
	// PATHEXT governs which extension wins. With PATHEXT=.EXE only, a .cmd shim
	// must NOT be picked up (it is not an executable extension). PATH is
	// isolated to the temp dir so a real `kimi` elsewhere cannot satisfy the
	// lookup.
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT is Windows-only")
	}
	stub := buildStub(t)
	dir := t.TempDir()
	cmdShim := filepath.Join(dir, "kimi.cmd")
	if err := os.WriteFile(cmdShim, []byte("@echo off\r\n\""+stub+"\" %*\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir) // only this dir is searched
	t.Setenv("PATHEXT", ".EXE")

	a := New(Options{BinaryName: "kimi"})
	if d := a.Detect(testContext()); d.Installed {
		t.Errorf("with PATHEXT=.EXE a .cmd-only dir must not resolve: %+v", d)
	}
}

// Ensure exec is referenced even when the windows-only tests are skipped.
var _ = exec.LookPath
