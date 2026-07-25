//go:build !unix

package daemon

import "errors"

// flockTry is a no-op on non-unix platforms (Windows has no flock(2); the
// autostart lock is a cooperative guard, not a correctness gate). The
// post-spawn health re-check inside ensureDaemonDirs still prevents dual
// daemons.
func flockTry(fd uintptr) error {
	return errors.New("flock: not supported on this platform")
}

// flockUnlock is a no-op on non-unix platforms.
func flockUnlock(fd uintptr) error {
	return nil
}
