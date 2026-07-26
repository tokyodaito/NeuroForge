package kimi

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// copyStubTo copies the built kimistub binary to dir under name, so it can be
// found on PATH. Explicit Chmod guarantees the exec bit even when umask would
// otherwise strip it (non-executable stubs fall through LookPath to a real
// user-installed kimi when PATH is only prepended).
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
	if err := os.Chmod(dst, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", dst, err)
	}
	return dst
}

// isolatePath replaces PATH with only dir for the duration of the test.
// Process-global PATH must never be merely prepended: a non-executable stub
// (or any LookPath miss) would fall through to a real installed kimi and make
// version probing load-sensitive (158MB+ binaries under full-suite pressure).
func isolatePath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

func kimiExeName() string {
	if runtime.GOOS == "windows" {
		return "kimi.exe"
	}
	return "kimi"
}

// T1 — Found via isolated PATH: controlled fake only, exact canonical path,
// stub default version; no real user PATH dependency.
func TestDetectViaPATH(t *testing.T) {
	dir := t.TempDir()
	dst := copyStubTo(t, dir, kimiExeName())
	isolatePath(t, dir)

	a := New(Options{BinaryName: "kimi"})
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf("PATH detection failed: %+v", d)
	}
	if filepath.Clean(d.Path) != filepath.Clean(dst) {
		t.Fatalf("path = %q, want controlled stub %q (real kimi contamination?)", d.Path, dst)
	}
	if !strings.Contains(d.Version, "1.4.0") {
		t.Errorf("version = %q, want the stub default 1.4.0; detail=%q", d.Version, d.Detail)
	}
}

// T2 — Not found on an isolated empty PATH: deterministic not-found.
func TestDetectNotOnPath(t *testing.T) {
	isolatePath(t, t.TempDir())
	a := New(Options{BinaryName: "kimi"})
	if d := a.Detect(testContext()); d.Installed {
		t.Errorf("should not be installed on empty PATH: %+v", d)
	}
}

// T2b — unknown name even when PATH still has host entries.
func TestDetectUnknownBinaryName(t *testing.T) {
	a := New(Options{BinaryName: "kimi-not-real-zzz-999"})
	if d := a.Detect(testContext()); d.Installed {
		t.Errorf("should not be installed: %+v", d)
	}
}

// T3 — Unix non-executable file is not accepted by LookPath.
func TestDetectNonExecutableRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit semantics are Unix-specific")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "kimi")
	if err := os.WriteFile(dst, []byte("#!/bin/sh\necho no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	isolatePath(t, dir)

	a := New(Options{BinaryName: "kimi"})
	if d := a.Detect(testContext()); d.Installed {
		t.Errorf("non-executable must not resolve: %+v", d)
	}
}

// T4 — Symlink to the stub is accepted; Detect reports the path LookPath returns.
func TestDetectSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture is Unix-oriented")
	}
	dir := t.TempDir()
	target := copyStubTo(t, dir, "kimi-target")
	link := filepath.Join(dir, "kimi")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	isolatePath(t, dir)

	a := New(Options{BinaryName: "kimi"})
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf("symlink detection failed: %+v", d)
	}
	// LookPath may return the symlink path or the resolved target depending on
	// platform; either is acceptable as long as the stub version is obtained.
	if !strings.Contains(d.Version, "1.4.0") {
		t.Errorf("version = %q, want stub 1.4.0 via symlink; path=%q detail=%q", d.Version, d.Path, d.Detail)
	}
}

// T5 — Paths with spaces and Unicode resolve to the controlled stub.
func TestDetectSpacesInPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dir with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := copyStubTo(t, dir, kimiExeName())
	isolatePath(t, dir)

	a := New(Options{BinaryName: "kimi", ExtraEnv: []string{"KIMI_STUB_VERSION=2.0.0"}})
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf("spaces-in-path detection failed: %+v", d)
	}
	if filepath.Clean(d.Path) != filepath.Clean(dst) {
		t.Errorf("path = %q, want %q", d.Path, dst)
	}
	if !strings.Contains(d.Version, "2.0.0") {
		t.Errorf("version = %q, want ExtraEnv override 2.0.0; detail=%q", d.Version, d.Detail)
	}
}

func TestDetectUnicodePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "café-測試-Λ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := copyStubTo(t, dir, kimiExeName())
	isolatePath(t, dir)

	a := New(Options{BinaryName: "kimi"})
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf("unicode-path detection failed: %+v", d)
	}
	if filepath.Clean(d.Path) != filepath.Clean(dst) {
		t.Errorf("path = %q, want %q", d.Path, dst)
	}
	if !strings.Contains(d.Version, "1.4.0") {
		t.Errorf("version = %q, want stub 1.4.0; detail=%q", d.Version, d.Detail)
	}
}

// T6 — LookPath injection: detection must not require mutating process-global
// PATH (safe under parallel package load and sibling adapter tests).
func TestDetectLookPathInjection(t *testing.T) {
	dir := t.TempDir()
	dst := copyStubTo(t, dir, kimiExeName())
	// Deliberately leave PATH pointing somewhere that has no kimi.
	isolatePath(t, t.TempDir())

	a := New(Options{
		BinaryName: "kimi",
		LookPath: func(file string) (string, error) {
			if file == "kimi" || file == kimiExeName() {
				return dst, nil
			}
			return "", errors.New("not found: " + file)
		},
	})
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf("injected LookPath detection failed: %+v", d)
	}
	if filepath.Clean(d.Path) != filepath.Clean(dst) {
		t.Errorf("path = %q, want %q", d.Path, dst)
	}
	if !strings.Contains(d.Version, "1.4.0") {
		t.Errorf("version = %q, want stub 1.4.0; detail=%q", d.Version, d.Detail)
	}
}

// T7 — Real binary contamination: even with a real kimi on the host PATH, an
// isolated controlled fake must win and report the stub version (never the
// host engine).
func TestDetectIgnoresRealKimiOnHostPATH(t *testing.T) {
	hostKimi, hostErr := exec.LookPath("kimi")
	dir := t.TempDir()
	dst := copyStubTo(t, dir, kimiExeName())
	// Isolated PATH: only the stub dir. Host kimi must not be visible.
	isolatePath(t, dir)

	a := New(Options{BinaryName: "kimi"})
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf("controlled stub not detected: %+v", d)
	}
	if filepath.Clean(d.Path) != filepath.Clean(dst) {
		t.Fatalf("path = %q, want stub %q", d.Path, dst)
	}
	if hostErr == nil && filepath.Clean(d.Path) == filepath.Clean(hostKimi) {
		t.Fatalf("resolved host kimi %q instead of stub", hostKimi)
	}
	if !strings.Contains(d.Version, "1.4.0") {
		t.Errorf("version = %q, want stub 1.4.0 (not host engine); detail=%q hostKimi=%q hostErr=%v",
			d.Version, d.Detail, hostKimi, hostErr)
	}
}

// TestDetectNonExecutableDoesNotFallThrough documents the prepend hazard: a
// non-executable name earlier on PATH must not be "fixed" by falling through
// to a later real kimi when PATH is isolated.
func TestDetectNonExecutableDoesNotFallThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit fallthrough is Unix-specific")
	}
	// Prepend-style PATH used to allow fallthrough to ~/.kimi-code/bin/kimi.
	// With isolation, non-exec → not installed.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kimi"), []byte("not-exec"), 0o644); err != nil {
		t.Fatal(err)
	}
	isolatePath(t, dir)
	a := New(Options{BinaryName: "kimi"})
	if d := a.Detect(testContext()); d.Installed {
		t.Errorf("isolated non-exec must not fall through: %+v", d)
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
	isolatePath(t, dir)

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
	isolatePath(t, dir)

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
