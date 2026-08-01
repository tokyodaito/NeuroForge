package grok

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// stubBin builds the test-only grokstub binary once per package and returns its
// absolute path. The stub emulates the headless grok streaming-json wire format
// driven by the GROK_STUB_SCENARIO env var (rule §36.5: no real/paid calls).
var (
	stubBuildOnce sync.Once
	stubBinPath   string
)

func stubBin(t *testing.T) string {
	t.Helper()
	stubBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "grok-stub-*")
		if err != nil {
			t.Fatalf("mktemp: %v", err)
		}
		bin := filepath.Join(dir, "grokstub")
		cmd := exec.Command("go", "build", "-o", bin, "./internal/stub")
		cmd.Dir = packageDir(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build grokstub: %v\n%s", err, out)
		}
		stubBinPath = bin
	})
	if stubBinPath == "" {
		t.Fatal("grokstub binary was not built")
	}
	return stubBinPath
}

// packageDir returns the directory of this test package (the grok adapter root).
func packageDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// stubOptions returns Options wired to the built stub for a given scenario.
// The scenario is communicated to the stub via ExtraEnv (test-only hook).
func stubOptions(t *testing.T, scenario string) Options {
	t.Helper()
	return Options{
		Binary:   stubBin(t),
		ExtraEnv: []string{"GROK_STUB_SCENARIO=" + scenario},
	}
}
