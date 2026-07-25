package github

import (
	"context"
	"os/exec"
)

// execCmd builds a git command. Indirected as a package var so unit tests can
// replace it and assert the push URL without spawning git.
var execCmd = func(ctx context.Context, gitBinary string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, gitBinary, args...)
}
