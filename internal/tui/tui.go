package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"neuroforge/internal/daemon"
	"neuroforge/internal/version"
)

// Options configures a TUI run.
type Options struct {
	In    io.Reader
	Out   io.Writer
	IsTTY bool
	Dirs  daemon.Dirs
}

// Run opens the full-screen TUI shell and blocks until the user quits (q, Ctrl-C
// or EOF) or ctx is cancelled. On a non-terminal output it degrades to a plain
// notice (it never corrupts a piped stream with escape codes).
func Run(ctx context.Context, opts Options) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	in := opts.In
	if in == nil {
		in = os.Stdin
	}

	if !opts.IsTTY {
		fmt.Fprintln(out, "NeuroForge TUI requires an interactive terminal.")
		fmt.Fprintln(out, "Run 'forge help' for the list of CLI commands.")
		fmt.Fprintln(out, "(this is the M0 TUI shell; full screens arrive in later milestones.)")
		return nil
	}

	// Catch Ctrl-C so we can restore the screen and exit cleanly.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	enterAlt(out)
	defer leaveAlt(out)

	render(out, opts)

	// Input loop: quit on 'q', Ctrl-D (EOF) or context cancel.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		reader := bufio.NewReader(in)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			switch b {
			case 'q', 'Q', 0x03 /* Ctrl-C */, 0x04 /* Ctrl-D */, 0x1b /* Esc */ :
				return
			case '\r', '\n':
				// re-render to refresh status on Enter
				render(out, opts)
			}
		}
	}()

	select {
	case <-doneCh:
	case <-sigCtx.Done():
	case <-ctx.Done():
	}
	return nil
}

// enterAlt/leaveAlt switch the terminal to/from the alternate screen buffer so
// the TUI does not clobber the user's scrollback.
func enterAlt(w io.Writer) {
	fmt.Fprint(w, "\x1b[?1049h\x1b[2J\x1b[H") // alt screen, clear, home
}

func leaveAlt(w io.Writer) {
	fmt.Fprint(w, "\x1b[?1049l") // restore main screen
}

// render draws a single full-screen frame.
func render(w io.Writer, opts Options) {
	v := version.Current()
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J") // home + clear
	b.WriteString(bold("NeuroForge") + "  " + dim(v.Version) + "  " + dim(time.Now().Format("15:04")) + "\n\n")
	b.WriteString("PROJECTS\n\n")
	b.WriteString(dim("  (no projects yet — use 'forge project add' in M1)") + "\n\n")
	b.WriteString("ACTIVE RUNS\n\n")
	b.WriteString(dim("  (no active runs)") + "\n\n")
	b.WriteString("DAEMON\n  ")
	b.WriteString(daemonLine(opts))
	b.WriteString("\n\n")
	b.WriteString("--------------------------------------------------------------------------------\n")
	b.WriteString(dim("M0 shell — no business functionality yet.") + "\n")
	b.WriteString("keys: " + key("q") + "/" + key("Esc") + " quit  ·  " + key("Enter") + " refresh status\n")
	_, _ = io.WriteString(w, b.String())
}

func daemonLine(opts Options) string {
	if opts.Dirs.Root == "" {
		return dim("runtime dir not configured")
	}
	st := daemon.GetStatus(context.Background(), opts.Dirs)
	switch st.State {
	case daemon.StatusRunning:
		return ok("running") + "  pid=" + fmt.Sprintf("%d", st.PID) + "  " + dim(st.Addr)
	case daemon.StatusUnhealthy:
		return warn("unhealthy") + "  " + dim(st.Note)
	case daemon.StatusStale, daemon.StatusCorrupted:
		return warn(string(st.State)) + "  " + dim(st.Note)
	default:
		return dim("not running — start with 'forge daemon start'")
	}
}

func bold(s string) string { return "\x1b[1m" + s + "\x1b[0m" }
func dim(s string) string  { return "\x1b[2m" + s + "\x1b[0m" }
func ok(s string) string   { return "\x1b[32m" + s + "\x1b[0m" }
func warn(s string) string { return "\x1b[33m" + s + "\x1b[0m" }
func key(s string) string  { return "\x1b[7m " + s + " \x1b[0m" }
