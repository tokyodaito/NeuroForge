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
	"neuroforge/internal/transport"

	"golang.org/x/term"
)

// Options configures a TUI run.
type Options struct {
	In    io.Reader
	Out   io.Writer
	IsTTY bool
	Dirs  daemon.Dirs
}

// Run opens the full-screen TUI and blocks until the user quits or ctx is
// cancelled. On a non-terminal output it degrades to a plain notice.
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
		return nil
	}

	fd := getFD(in)

	// Double-check that the input is actually a terminal (opts.IsTTY is a hint
	// from the CLI; the real check uses term.IsTerminal to handle tests/pipes).
	if !term.IsTerminal(fd) {
		fmt.Fprintln(out, "NeuroForge TUI requires an interactive terminal.")
		fmt.Fprintln(out, "Run 'forge help' for the list of CLI commands.")
		return nil
	}

	// Save and restore terminal state.
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("tui: enter raw mode: %w", err)
	}
	defer func() {
		_ = term.Restore(fd, oldState)
	}()

	// Enable mouse tracking (SGR mode + button events).
	fmt.Fprint(out, "\x1b[?1049h\x1b[?1006h\x1b[?1000h\x1b[2J\x1b[H")
	defer fmt.Fprint(out, "\x1b[?1000l\x1b[?1006l\x1b[?1049l")

	// Catch Ctrl-C at the OS level as a fallback.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	m := InitialModel()

	// Try to connect to the daemon for live data.
	client, daemonErr := daemon.Connect(sigCtx, opts.Dirs)
	if daemonErr == nil {
		m.DaemonRunning = true
	}

	// Initial refresh.
	m, _ = Update(m, Msg{Type: MsgInit})
	if client != nil {
		m = fetchAndApply(sigCtx, client, m)
	}

	render(out, m)

	// SSE subscription for live refresh.
	eventCh := subscribeEvents(sigCtx, client)

	// Keyboard input goroutine.
	keyCh := readKeys(in)

	// Periodic refresh ticker.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sigCtx.Done():
			return nil
		case key := <-keyCh:
			if key == "" {
				continue
			}
			var effect Effect
			m, effect = Update(m, Msg{Type: MsgKey, Key: key})
			if effect == EffectQuit {
				return nil
			}
			m = executeEffect(sigCtx, client, m, effect, out)
			render(out, m)

		case evt, ok := <-eventCh:
			if !ok {
				eventCh = nil
				continue
			}
			var effect Effect
			m, effect = Update(m, Msg{Type: MsgDaemonEvent, Event: evt})
			m = executeEffect(sigCtx, client, m, effect, out)
			render(out, m)

		case <-ticker.C:
			if client != nil {
				m = fetchAndApply(sigCtx, client, m)
				render(out, m)
			}
		}
	}
}

// getFD extracts the file descriptor from an io.Reader (must be *os.File).
func getFD(in io.Reader) int {
	if f, ok := in.(*os.File); ok {
		return int(f.Fd())
	}
	return int(os.Stdin.Fd())
}

// render draws one frame.
func render(w io.Writer, m Model) {
	_, _ = io.WriteString(w, View(m))
}

// fetchAndApply loads projects and tasks from the daemon and applies them to
// the model.
func fetchAndApply(ctx context.Context, client *transport.Client, m Model) Model {
	if client == nil {
		return m
	}
	projects, err := client.ListProjects(ctx)
	if err == nil {
		m, _ = Update(m, Msg{Type: MsgProjectsLoaded, Projects: projects})
	} else {
		m, _ = Update(m, Msg{Type: MsgStatus, StatusMsg: "fetch projects: " + err.Error()})
	}

	if m.ActiveProjectID != "" {
		tasks, err := client.ListTasks(ctx, m.ActiveProjectID)
		if err == nil {
			m, _ = Update(m, Msg{Type: MsgTasksLoaded, Tasks: tasks})
		}
	}
	return m
}

// executeEffect performs side effects requested by Update.
func executeEffect(ctx context.Context, client *transport.Client, m Model, effect Effect, out io.Writer) Model {
	if effect == EffectNone {
		return m
	}
	if client == nil {
		m, _ = Update(m, Msg{Type: MsgStatus, StatusMsg: "daemon not connected"})
		return m
	}

	switch effect {
	case EffectRefreshProjects:
		projects, err := client.ListProjects(ctx)
		if err != nil {
			m, _ = Update(m, Msg{Type: MsgError, Err: err})
		} else {
			m, _ = Update(m, Msg{Type: MsgProjectsLoaded, Projects: projects})
		}

	case EffectRefreshTasks:
		pid := m.ActiveProjectID
		if m.Screen == ScreenProjects && len(m.Projects) > 0 {
			pid = m.Projects[m.SelectedProject].ID
		}
		tasks, err := client.ListTasks(ctx, pid)
		if err != nil {
			m, _ = Update(m, Msg{Type: MsgError, Err: err})
		} else {
			m.ActiveProjectID = pid
			m, _ = Update(m, Msg{Type: MsgTasksLoaded, Tasks: tasks})
		}

	case EffectStartProject:
		if len(m.Projects) > 0 {
			id := m.Projects[m.SelectedProject].ID
			p, err := client.StartProject(ctx, id)
			if err != nil {
				m, _ = Update(m, Msg{Type: MsgError, Err: err})
			} else {
				m.Projects[m.SelectedProject] = p
				m, _ = Update(m, Msg{Type: MsgStatus, StatusMsg: "Project " + id + " started"})
			}
		}

	case EffectPauseProject:
		if len(m.Projects) > 0 {
			id := m.Projects[m.SelectedProject].ID
			p, err := client.PauseProject(ctx, id)
			if err != nil {
				m, _ = Update(m, Msg{Type: MsgError, Err: err})
			} else {
				m.Projects[m.SelectedProject] = p
				m, _ = Update(m, Msg{Type: MsgStatus, StatusMsg: "Project " + id + " paused"})
			}
		}

	case EffectStopProject:
		if len(m.Projects) > 0 {
			id := m.Projects[m.SelectedProject].ID
			p, err := client.StopProject(ctx, id)
			if err != nil {
				m, _ = Update(m, Msg{Type: MsgError, Err: err})
			} else {
				m.Projects[m.SelectedProject] = p
				m, _ = Update(m, Msg{Type: MsgStatus, StatusMsg: "Project " + id + " stopped"})
			}
		}

	case EffectPauseTask:
		if len(m.Tasks) > 0 {
			id := m.Tasks[m.SelectedTask].ID
			t, err := client.PauseTask(ctx, id)
			if err != nil {
				m, _ = Update(m, Msg{Type: MsgError, Err: err})
			} else {
				m.Tasks[m.SelectedTask] = t
				m, _ = Update(m, Msg{Type: MsgStatus, StatusMsg: "Task " + id + " paused"})
			}
		}

	case EffectCancelTask:
		if len(m.Tasks) > 0 {
			id := m.Tasks[m.SelectedTask].ID
			t, err := client.CancelTask(ctx, id)
			if err != nil {
				m, _ = Update(m, Msg{Type: MsgError, Err: err})
			} else {
				m.Tasks[m.SelectedTask] = t
				m, _ = Update(m, Msg{Type: MsgStatus, StatusMsg: "Task " + id + " cancelled"})
			}
		}

	case EffectShowProjectDetail:
		if len(m.Projects) > 0 {
			m.DetailID = m.Projects[m.SelectedProject].ID
		}

	case EffectShowTaskDetail:
		if len(m.Tasks) > 0 {
			m.DetailID = m.Tasks[m.SelectedTask].ID
		}

	case EffectStartProjectAdd:
		m, _ = Update(m, Msg{Type: MsgStatus, StatusMsg: "Use 'forge project add <path>' to add a project"})
	}

	return m
}

// subscribeEvents opens the SSE stream and returns a channel of events. Returns
// nil if the client is nil.
func subscribeEvents(ctx context.Context, client *transport.Client) <-chan transport.Event {
	if client == nil {
		return nil
	}
	ch, err := client.Stream(ctx)
	if err != nil {
		return nil
	}
	return ch
}

// ---- keyboard input parsing ----

// readKeys reads raw bytes from r and produces human-readable key names on a
// channel. Handles escape sequences for arrow keys, function keys, etc.
func readKeys(r io.Reader) <-chan string {
	out := make(chan string, 16)
	go func() {
		defer close(out)
		reader := bufio.NewReader(r)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}

			key := parseKey(b, reader)
			if key != "" {
				select {
				case out <- key:
				default:
				}
			}
		}
	}()
	return out
}

// parseKey interprets a byte (possibly followed by more bytes) as a key name.
func parseKey(b byte, reader *bufio.Reader) string {
	switch {
	case b == 0x1b: // ESC — could be Esc alone or an escape sequence
		// Try to peek at the next byte (non-blocking).
		next, err := reader.Peek(1)
		if err != nil || len(next) == 0 {
			return "esc"
		}
		c := next[0]
		if c == '[' {
			// CSI sequence: read until we get a final byte.
			reader.ReadByte() // consume '['
			return parseCSI(reader)
		}
		if c == 'O' {
			// SS3 sequence (some function keys).
			reader.ReadByte() // consume 'O'
			fb, err := reader.ReadByte()
			if err != nil {
				return "esc"
			}
			return parseSS3(fb)
		}
		// Esc followed by something else: treat as Alt+key.
		return "esc"

	case b == 0x0d || b == 0x0a: // Enter
		return "enter"

	case b == 0x09: // Tab
		return "tab"

	case b == 0x7f || b == 0x08: // Backspace
		return "backspace"

	case b == 0x03: // Ctrl-C
		return "ctrl+c"

	case b >= 0x01 && b <= 0x1a: // Ctrl+A to Ctrl+Z
		return "ctrl+" + string('a'+b-1)

	case b >= 0x20 && b <= 0x7e: // Printable ASCII
		return string(b)
	}

	return ""
}

// parseCSI reads a CSI escape sequence (after \x1b[) and returns the key name.
func parseCSI(reader *bufio.Reader) string {
	var params strings.Builder
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return "esc"
		}
		if b >= '@' && b <= '~' {
			return interpretCSI(params.String(), b)
		}
		params.WriteByte(b)
	}
}

func interpretCSI(params string, final byte) string {
	switch final {
	case 'A':
		return "up"
	case 'B':
		return "down"
	case 'C':
		return "right"
	case 'D':
		return "left"
	case 'Z':
		return "shift+tab"
	case 'M':
		// Mouse event (SGGR format handled in parseMouse)
		return ""
	case '~':
		switch params {
		case "1", "7":
			return "home"
		case "4", "8":
			return "end"
		case "3":
			return "delete"
		case "5":
			return "pageup"
		case "6":
			return "pagedown"
		}
	}
	return ""
}

// parseSS3 interprets an SS3 escape sequence (after \x1b0).
func parseSS3(b byte) string {
	switch b {
	case 'P':
		return "f1"
	case 'Q':
		return "f2"
	case 'R':
		return "f3"
	case 'S':
		return "f4"
	case 'A':
		return "up"
	case 'B':
		return "down"
	case 'C':
		return "right"
	case 'D':
		return "left"
	}
	return ""
}
