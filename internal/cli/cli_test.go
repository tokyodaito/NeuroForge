package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"neuroforge/internal/daemon"
)

func newTestApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	a := &App{Name: Name, Out: out, Err: errOut, Stdin: bytes.NewReader(nil)}
	dir := daemon.WithRoot(t.TempDir())
	a.dirs = func() (daemon.Dirs, error) { return dir, nil }
	a.stderrIsTTY = func() bool { return false }
	return a, out, errOut
}

func TestRun_VersionCommand(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run([]string{"version"}); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	if !strings.HasPrefix(out.String(), "forge ") {
		t.Errorf("version output should start with 'forge '; got %q", out.String())
	}
}

func TestRun_VersionAliases(t *testing.T) {
	for _, arg := range []string{"-v", "-version", "--version"} {
		a, out, _ := newTestApp(t)
		if code := a.Run([]string{arg}); code != ExitOK {
			t.Fatalf("%q: code = %d, want %d", arg, code, ExitOK)
		}
		if out.Len() == 0 {
			t.Errorf("%q: expected version output, got empty", arg)
		}
	}
}

func TestRun_HelpCommand(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run([]string{"help"}); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	got := out.String()
	for _, want := range []string{"Usage", "version", "help", "daemon start", "doctor", "Implemented"} {
		if !strings.Contains(got, want) {
			t.Errorf("help output missing %q; got:\n%s", want, got)
		}
	}
}

func TestRun_HelpAliases(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		a, out, _ := newTestApp(t)
		if code := a.Run([]string{arg}); code != ExitOK {
			t.Fatalf("%q: code = %d, want %d", arg, code, ExitOK)
		}
		if !strings.Contains(out.String(), "Usage") {
			t.Errorf("%q: missing Usage", arg)
		}
	}
}

func TestRun_NoArgs_NonTTY_DegradesGracefully(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run(nil); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	got := out.String()
	if !strings.Contains(got, "TUI requires an interactive terminal") {
		t.Errorf("expected TUI degradation notice; got %q", got)
	}
}

func TestRun_NoArgs_TTY_RendersAltScreen(t *testing.T) {
	a, out, _ := newTestApp(t)
	a.stderrIsTTY = func() bool { return true }
	a.Stdin = bytes.NewReader([]byte("q\n")) // quit immediately
	if code := a.Run(nil); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[?1049h") {
		t.Errorf("expected alternate-screen enter sequence; got %q", got)
	}
	if !strings.Contains(got, "NeuroForge") {
		t.Errorf("expected banner; got %q", got)
	}
	if !strings.Contains(got, "\x1b[?1049l") {
		t.Errorf("expected alternate-screen leave sequence on exit; got %q", got)
	}
}

func TestRun_UnknownCommand_ReturnsError(t *testing.T) {
	a, _, errOut := newTestApp(t)
	if code := a.Run([]string{"bogus-command"}); code != ExitErr {
		t.Fatalf("code = %d, want %d", code, ExitErr)
	}
	got := errOut.String()
	for _, want := range []string{"unknown command", "bogus-command", "Usage"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in error; got %q", want, got)
		}
	}
}

func TestRun_DaemonNoSubcommand_ReturnsError(t *testing.T) {
	a, _, errOut := newTestApp(t)
	if code := a.Run([]string{"daemon"}); code != ExitErr {
		t.Fatalf("code = %d, want %d", code, ExitErr)
	}
	if !strings.Contains(errOut.String(), "Usage") {
		t.Errorf("expected usage; got %q", errOut.String())
	}
}

func TestRun_DaemonUnknownSubcommand_ReturnsError(t *testing.T) {
	a, _, errOut := newTestApp(t)
	if code := a.Run([]string{"daemon", "frobnicate"}); code != ExitErr {
		t.Fatalf("code = %d, want %d", code, ExitErr)
	}
	if !strings.Contains(errOut.String(), "unknown daemon subcommand") {
		t.Errorf("got %q", errOut.String())
	}
}

func TestRun_NewDefaultsToOsStreams(t *testing.T) {
	a := New()
	if a.Name != Name {
		t.Fatalf("Name = %q, want %q", a.Name, Name)
	}
	if a.Out == nil || a.Err == nil {
		t.Fatal("New() must default Out/Err to os streams")
	}
	if a.Stdin != os.Stdin {
		t.Fatal("New() must default Stdin to os.Stdin")
	}
}
