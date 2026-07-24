//go:build !unix

package cli

import "context"

// withTerminationSignals is a no-op on non-unix (SIGTERM unavailable).
func withTerminationSignals(ctx context.Context) context.Context {
	return ctx
}
