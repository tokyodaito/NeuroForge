package bootstrap

import "os"

// isElevated reports whether the current process has elevated privileges.
// Unix: effective UID == 0. Windows: reported via a separate build-tagged file.
func isElevated() bool {
	return os.Geteuid() == 0
}
