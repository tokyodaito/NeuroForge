//go:build unix

package daemon

import (
	"syscall"
)

// flockTry acquires a non-blocking exclusive lock on fd. Returns syscall.EAGAIN
// (or EWOULDBLOCK) when the lock is held by another process.
func flockTry(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
}

// flockUnlock releases the lock held on fd.
func flockUnlock(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_UN)
}
