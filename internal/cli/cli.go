package cli

import (
	"fmt"
	"io"
	"os"

	"neuroforge/internal/version"
)

const (
	ExitOK  = 0
	ExitErr = 1

	Name = "forge"
)

type App struct {
	Name string
	Out  io.Writer
	Err  io.Writer
}

func New() *App {
	return &App{Name: Name, Out: os.Stdout, Err: os.Stderr}
}

func (a *App) Run(args []string) int {
	if len(args) == 0 {
		return a.noArgs()
	}
	switch args[0] {
	case "version", "-v", "-version", "--version":
		return a.runVersion()
	case "help", "-h", "--help":
		return a.runHelp()
	default:
		fmt.Fprintf(a.Err, "%s: unknown command %q\n\n", a.Name, args[0])
		writeHelp(a.Err)
		return ExitErr
	}
}

func (a *App) noArgs() int {
	fmt.Fprintln(a.Err, Name+": interactive TUI is not implemented yet (planned for milestone M0).")
	fmt.Fprintln(a.Err, "Run '"+Name+" help' for the list of implemented commands.")
	return ExitOK
}

func (a *App) runVersion() int {
	fmt.Fprint(a.Out, version.Current().String())
	return ExitOK
}
