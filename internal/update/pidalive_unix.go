//go:build !windows

package update

import (
	"errors"
	"os"
	"syscall"
)

// lockPidAlive reports whether pid names a live process.
//
// It answers "alive" whenever it cannot prove otherwise: the caller uses it
// to decide whether to take somebody else's update lock away, and only a
// positive proof of death may do that. Signal 0 to a process owned by
// another user returns EPERM, which proves the process exists.
func lockPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == currentPid() {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true // cannot tell; do not take the lock
	}
	defer p.Release() //nolint:errcheck
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH)
}
