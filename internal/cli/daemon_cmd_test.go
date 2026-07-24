package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonStatus_NotRunning_ExitErr(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run([]string{"daemon", "status"}); code != ExitErr {
		t.Fatalf("code = %d, want %d (not running)", code, ExitErr)
	}
	if !strings.Contains(out.String(), "absent") {
		t.Errorf("expected absent status; got %q", out.String())
	}
}

func TestDaemonStatus_JSON(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run([]string{"daemon", "status", "--json"}); code != ExitErr {
		t.Fatalf("code = %d, want %d", code, ExitErr)
	}
	if !strings.Contains(out.String(), `"state":"absent"`) {
		t.Errorf("expected json absent; got %q", out.String())
	}
}

func TestDaemonStop_NotRunning_Idempotent(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run([]string{"daemon", "stop"}); code != ExitOK {
		t.Fatalf("code = %d, want %d (idempotent)", code, ExitOK)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Errorf("expected 'not running'; got %q", out.String())
	}
}

func TestDaemonLogs_NoLogFile_EmptyOK(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run([]string{"daemon", "logs"}); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	if out.Len() != 0 {
		t.Errorf("expected empty logs, got %q", out.String())
	}
}

func TestDaemonLogs_PrintsFile(t *testing.T) {
	a, out, _ := newTestApp(t)
	// Resolve the dirs to write a fake log file.
	dirs, _ := a.resolveDirs()
	if err := os.MkdirAll(filepath.Dir(dirs.LogFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dirs.LogFile, []byte("line one\nline two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := a.Run([]string{"daemon", "logs"}); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(out.String(), "line two") {
		t.Errorf("expected log contents; got %q", out.String())
	}
}
