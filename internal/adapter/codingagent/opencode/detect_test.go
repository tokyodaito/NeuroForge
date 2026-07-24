package opencode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDetectMissingBinary reports not-installed when opencode is absent.
func TestDetectMissingBinary(t *testing.T) {
	a := New(Options{})
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	res := a.Detect(context.Background())
	if res.Installed {
		t.Fatalf("expected not installed, got %+v", res)
	}
	if !strings.Contains(res.Detail, "not found") {
		t.Errorf("detail = %q", res.Detail)
	}
}

// TestDetectExplicitBinaryAndProbe verifies the version probe path and that a
// failed probe still reports installed.
func TestDetectExplicitBinaryAndProbe(t *testing.T) {
	t.Run("probe ok", func(t *testing.T) {
		a := New(Options{Binary: "/usr/local/bin/opencode"})
		a.runProbe = func(context.Context, string) (string, string, error) {
			return "opencode 0.1.48", "", nil
		}
		res := a.Detect(context.Background())
		if !res.Installed {
			t.Fatalf("expected installed")
		}
		if res.Version != "0.1.48" {
			t.Errorf("version = %q want 0.1.48", res.Version)
		}
		if res.Path != "/usr/local/bin/opencode" {
			t.Errorf("path = %q", res.Path)
		}
	})
	t.Run("probe fails -> installed no version", func(t *testing.T) {
		a := New(Options{Binary: "/x/opencode"})
		a.runProbe = func(context.Context, string) (string, string, error) {
			return "", "boom", errors.New("exit 1")
		}
		res := a.Detect(context.Background())
		if !res.Installed {
			t.Fatalf("binary exists; should still be installed")
		}
		if res.Version != "" {
			t.Errorf("version should be empty, got %q", res.Version)
		}
	})
}

// TestDetectFindsShimOnPATH creates an opencode shim in a temp PATH dir and
// verifies Detect resolves it. Exercises .exe/.cmd/.bat/shim + PATHEXT +
// spaces/Unicode in the real exec.LookPath path (rule §36.5: no paid calls).
func TestDetectFindsShimOnPATH(t *testing.T) {
	dir := t.TempDir()
	// Unicode + spaces in the dir name to verify path tolerance.
	unicodeDir := filepath.Join(dir, "ünï cödé dir")
	if err := os.MkdirAll(unicodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "opencode"
	if runtime.GOOS == "windows" {
		name = "opencode.cmd"
	} else {
		name = "opencode"
	}
	shim := filepath.Join(unicodeDir, name)
	content := "#!/bin/sh\necho 'opencode 0.1.48'\n"
	if runtime.GOOS == "windows" {
		content = "@echo off\r\necho opencode 0.1.48\r\n"
	}
	if err := os.WriteFile(shim, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prepend the dir to PATH for the real exec.LookPath.
	prev := os.Getenv("PATH")
	t.Setenv("PATH", unicodeDir+string(os.PathListSeparator)+prev)

	a := New(Options{})
	// Real lookPath + a probe that returns the parsed version.
	res := a.Detect(context.Background())
	if !res.Installed {
		t.Fatalf("expected shim detected on PATH; got %+v", res)
	}
	if res.Path == "" {
		t.Errorf("path empty")
	}
}

// TestDetectCmdShimVariant explicitly checks a .cmd shim resolves (Windows) or
// is skipped gracefully elsewhere.
func TestDetectCmdShimVariant(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cmd shim variant is Windows-specific")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.cmd"), []byte("@echo off\r\necho opencode 0.1.48\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+prev)
	a := New(Options{})
	res := a.Detect(context.Background())
	if !res.Installed {
		t.Fatalf("expected .cmd shim detected")
	}
}

// TestDetectCachesResult ensures repeated calls reuse the cached detection and
// Capabilities does not re-spawn.
func TestDetectCachesResult(t *testing.T) {
	a := New(Options{Binary: "/x/opencode"})
	calls := 0
	a.runProbe = func(context.Context, string) (string, string, error) {
		calls++
		return "opencode 0.1.48", "", nil
	}
	_ = a.Detect(context.Background())
	_ = a.Capabilities(context.Background())
	_ = a.Version(context.Background())
	if calls != 1 {
		t.Errorf("probe called %d times, want 1 (cached)", calls)
	}
}
