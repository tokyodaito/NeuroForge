package cli

import (
	"context"

	"neuroforge/internal/tui"
)

// runTUIShell launches the interactive TUI (AC-1). It is invoked when `forge`
// runs with no arguments. It degrades gracefully on a non-interactive terminal.
func runTUIShell(a *App) int {
	dirs, err := a.resolveDirs()
	if err != nil {
		a.errf("resolve runtime dir: %v", err)
		return ExitErr
	}

	isTTY := false
	if a.stderrIsTTY != nil {
		isTTY = a.stderrIsTTY()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := tui.Run(ctx, tui.Options{
		In:    a.Stdin,
		Out:   a.Out,
		IsTTY: isTTY,
		Dirs:  dirs,
	}); err != nil {
		a.errf("tui: %v", err)
		return ExitErr
	}
	return ExitOK
}
