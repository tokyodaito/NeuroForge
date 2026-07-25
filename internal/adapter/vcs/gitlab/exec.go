package gitlab

import (
	"context"
	"os/exec"
)

// execCmd builds a git command. Indirected so tests can stub it.
var execCmd = func(ctx context.Context, gitBinary string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, gitBinary, args...)
}
