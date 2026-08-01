package cli

import (
	"fmt"
	"io"
	"os"

	"neuroforge/internal/daemon"
	"neuroforge/internal/version"
)

const (
	ExitOK  = 0
	ExitErr = 1

	Name = "forge"
)

// App is the CLI application. It is split from main so tests can drive it with
// in-memory streams.
type App struct {
	Name  string
	Out   io.Writer
	Err   io.Writer
	Stdin io.Reader

	// dirs resolves the runtime directory layout. Defaults to
	// daemon.DefaultDirs (honours NEUROFORGE_HOME). Injectable for tests.
	dirs func() (daemon.Dirs, error)

	// stderrIsTTY is used to decide whether the TUI can take over the screen.
	stderrIsTTY func() bool

	// initDeps optionally overrides the bootstrap components so `forge init` is
	// testable offline (rule §33). nil → real production components.
	initDeps *initDependencies
}

// New returns an App wired to os streams.
func New() *App {
	a := &App{Name: Name, Out: os.Stdout, Err: os.Stderr, Stdin: os.Stdin}
	a.dirs = daemon.DefaultDirs
	a.stderrIsTTY = func() bool { return isTerminal(os.Stderr) }
	return a
}

func (a *App) resolveDirs() (daemon.Dirs, error) {
	if a.dirs != nil {
		return a.dirs()
	}
	return daemon.DefaultDirs()
}

// Run dispatches the CLI. It returns the process exit code.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		return a.runTUI()
	}
	switch args[0] {
	case "version", "-v", "-version", "--version":
		return a.runVersion()
	case "help", "-h", "--help":
		return a.runHelp()
	case "doctor":
		return a.runDoctor(args[1:])
	case "daemon":
		return a.runDaemon(args[1:])
	case "dashboard":
		return a.runTUI()
	case "project":
		return a.runProject(args[1:])
	case "task":
		return a.runTask(args[1:])
	case "spec":
		return a.runSpec(args[1:])
	case "workgraph":
		return a.runWorkGraph(args[1:])
	case "workspace":
		return a.runWorkspace(args[1:])
	case "run":
		return a.runRunCmd(args[1:])
	case "pipeline":
		return a.runPipelineCmd(args[1:])
	case "estop":
		return a.runEstopCmd(args[1:])
	case "plugin":
		return a.runPlugin(args[1:])
	case "quota":
		return a.runQuota(args[1:])
	case "usage":
		return a.runUsage(args[1:])
	case "cost":
		return a.runCost(args[1:])
	case "route":
		return a.runRoute(args[1:])
	case "image-provider":
		return a.runImageProvider(args[1:])
	case "memory":
		return a.runMemory(args[1:])
	case "quality":
		return a.runQuality(args[1:])
	case "init":
		return a.runInit(args[1:])
	case "update":
		return a.runUpdate(args[1:])
	case "gate":
		return a.runGate(args[1:])
	default:
		fmt.Fprintf(a.Err, "%s: unknown command %q\n\n", a.Name, args[0])
		writeHelp(a.Err)
		return ExitErr
	}
}

func (a *App) runVersion() int {
	fmt.Fprint(a.Out, version.Current().String())
	return ExitOK
}

func (a *App) runTUI() int {
	// AC-1: `forge` with no args opens the interactive TUI. In M0 the TUI is a
	// minimal full-screen shell (see internal/tui). It degrades gracefully on a
	// non-interactive terminal.
	return runTUIShell(a)
}

func (a *App) errf(format string, args ...any) {
	fmt.Fprintf(a.Err, a.Name+": "+format+"\n", args...)
}
