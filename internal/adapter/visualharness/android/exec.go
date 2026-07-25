package android

import (
	"context"
	"fmt"
	"os/exec"
)

// runExec is split into its own file so tests can stub it via build tags if
// needed (the default uses os/exec; CI does not require real adb).
func runExec(ctx context.Context, dir string, env []string, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("android: empty command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	return cmd.CombinedOutput()
}
