package cli

import (
	"context"
	"fmt"
	"io"

	"neuroforge/internal/daemon"
	"neuroforge/internal/transport"
)

// daemonStreamEvents attaches to the daemon's live event stream (SSE) and
// prints each event until interrupted. Used by `forge daemon logs -f`.
func daemonStreamEvents(a *App, dirs daemon.Dirs, st daemon.Status) int {
	token, err := daemon.ReadToken(dirs)
	if err != nil || token == "" {
		a.errf("read daemon token: not available")
		return ExitErr
	}
	cli := transport.NewClient(st.Addr, token)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := cli.Stream(ctx)
	if err != nil {
		a.errf("attach to event stream: %v", err)
		return ExitErr
	}
	for evt := range ch {
		fmt.Fprintf(a.Out, "event  seq=%d  type=%s  ts=%s\n", evt.Seq, evt.Type, evt.Ts)
	}
	_ = io.Discard
	return ExitOK
}
