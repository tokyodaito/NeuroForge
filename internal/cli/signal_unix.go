//go:build unix

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// withTerminationSignals returns ctx cancelled on SIGINT or SIGTERM (unix).
func withTerminationSignals(ctx context.Context) context.Context {
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	// stop is intentionally never called: the process is short-lived (CLI) and
	// restoring default signal handling after first signal is not needed.
	_ = stop
	return sigCtx
}
