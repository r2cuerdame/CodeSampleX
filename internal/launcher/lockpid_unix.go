//go:build !windows

package launcher

import (
	"errors"
	"os"
	"syscall"
)

// recoveryLockPidAlive answers alive whenever it cannot prove death. EPERM
// from signal 0 proves that another account's process still exists.
func recoveryLockPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	defer p.Release() //nolint:errcheck
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH)
}
