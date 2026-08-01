package bootstrap

import "os"

// isElevated reports whether the current process has elevated privileges
// (effective UID == 0).
func isElevated() bool {
	return os.Geteuid() == 0
}
