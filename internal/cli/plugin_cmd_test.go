package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	m2FakeOnce sync.Once
	m2FakeBin  string
)

// m2FakeBinary builds cmd/fake-coding-agent once for the cli package tests.
func m2FakeBinary(t *testing.T) string {
	t.Helper()
	m2FakeOnce.Do(func() {
		root := m2ModuleRoot(t)
		dir, err := os.MkdirTemp("", "forge-fake-*")
		if err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "fake-coding-agent")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/fake-coding-agent")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build fake-coding-agent: %v\n%s", err, out)
		}
		m2FakeBin = bin
	})
	if m2FakeBin == "" {
		t.Fatal("fake binary not built")
	}
	return m2FakeBin
}

func m2ModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found")
	return ""
}

func TestPluginTestCommandPassesConformance(t *testing.T) {
	bin := m2FakeBinary(t)
	app := New()
	app.Out = &strings.Builder{}
	app.Err = &strings.Builder{}

	code := app.Run([]string{"plugin", "test", bin, "--timeout", "15s"})
	outStr := app.Out.(*strings.Builder).String()
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0\noutput:\n%s\nerr:\n%s", code, outStr, app.Err.(*strings.Builder).String())
	}
	if !strings.Contains(outStr, "conformance") {
		t.Errorf("output missing 'conformance':\n%s", outStr)
	}
	if !strings.Contains(outStr, "9/9 checks passed") {
		t.Errorf("expected 9/9 checks passed:\n%s", outStr)
	}
	// Every check must be PASS.
	for _, name := range []string{
		"handshake", "version_compatibility", "event_ordering", "malformed_output",
		"cancellation", "timeout", "quota_failure", "resume", "process_crash",
	} {
		if !strings.Contains(outStr, "[PASS]  "+name) {
			t.Errorf("check %q not reported PASS in:\n%s", name, outStr)
		}
	}
}

func TestPluginTestJSONOutput(t *testing.T) {
	bin := m2FakeBinary(t)
	app := New()
	out := &strings.Builder{}
	app.Out = out
	app.Err = &strings.Builder{}

	code := app.Run([]string{"plugin", "test", bin, "--json", "--timeout", "15s"})
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), `"Passed": true`) {
		t.Errorf("JSON missing Passed: true:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"Name": "handshake"`) {
		t.Errorf("JSON missing handshake result:\n%s", out.String())
	}
}

func TestPluginTestMissingExecutable(t *testing.T) {
	app := New()
	app.Out = &strings.Builder{}
	app.Err = &strings.Builder{}
	code := app.Run([]string{"plugin", "test", "/nonexistent/binary-xyz"})
	if code != ExitErr {
		t.Fatalf("exit code = %d, want 1 for missing executable", code)
	}
}

func TestPluginUsage(t *testing.T) {
	app := New()
	app.Out = &strings.Builder{}
	app.Err = &strings.Builder{}
	code := app.Run([]string{"plugin"})
	if code != ExitErr {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if code := app.Run([]string{"plugin", "-h"}); code != ExitOK {
		t.Fatalf("plugin -h exit = %d, want 0", code)
	}
}
