package cli

import (
	"bytes"
	"strings"
	"testing"
)

func newTestApp() (*App, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return &App{Name: Name, Out: out, Err: errOut}, out, errOut
}

func TestRun_VersionCommand(t *testing.T) {
	a, out, _ := newTestApp()
	if code := a.Run([]string{"version"}); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	if !strings.HasPrefix(out.String(), "forge ") {
		t.Errorf("version output should start with 'forge '; got %q", out.String())
	}
}

func TestRun_VersionAliases(t *testing.T) {
	for _, arg := range []string{"-v", "-version", "--version"} {
		a, out, _ := newTestApp()
		if code := a.Run([]string{arg}); code != ExitOK {
			t.Fatalf("%q: code = %d, want %d", arg, code, ExitOK)
		}
		if out.Len() == 0 {
			t.Errorf("%q: expected version output, got empty", arg)
		}
	}
}

func TestRun_HelpCommand(t *testing.T) {
	a, out, _ := newTestApp()
	if code := a.Run([]string{"help"}); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	got := out.String()
	for _, want := range []string{"Usage", "version", "help", "Implemented commands"} {
		if !strings.Contains(got, want) {
			t.Errorf("help output missing %q; got:\n%s", want, got)
		}
	}
}

func TestRun_HelpAliases(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		a, out, _ := newTestApp()
		if code := a.Run([]string{arg}); code != ExitOK {
			t.Fatalf("%q: code = %d, want %d", arg, code, ExitOK)
		}
		if !strings.Contains(out.String(), "Usage") {
			t.Errorf("%q: missing Usage", arg)
		}
	}
}

func TestRun_NoArgs_PrintsTuiNotImplemented(t *testing.T) {
	a, _, errOut := newTestApp()
	if code := a.Run(nil); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(errOut.String(), "TUI") || !strings.Contains(errOut.String(), "not implemented") {
		t.Errorf("no-args should print TUI not-implemented notice; got %q", errOut.String())
	}
}

func TestRun_UnknownCommand_ReturnsError(t *testing.T) {
	a, _, errOut := newTestApp()
	if code := a.Run([]string{"bogus-command"}); code != ExitErr {
		t.Fatalf("code = %d, want %d", code, ExitErr)
	}
	got := errOut.String()
	if !strings.Contains(got, "unknown command") {
		t.Errorf("expected 'unknown command' error; got %q", got)
	}
	if !strings.Contains(got, "bogus-command") {
		t.Errorf("error should echo the bad command; got %q", got)
	}
	if !strings.Contains(got, "Usage") {
		t.Errorf("error should include help/Usage; got %q", got)
	}
}

func TestNew_DefaultsToOsStreams(t *testing.T) {
	a := New()
	if a.Name != Name {
		t.Fatalf("Name = %q, want %q", a.Name, Name)
	}
	if a.Out == nil || a.Err == nil {
		t.Fatal("New() must default Out/Err to os streams")
	}
}
