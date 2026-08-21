//go:build windows

package update

import (
	"errors"

	"golang.org/x/sys/windows"
)

// lockPidAlive reports whether pid names a live process.
//
// It answers "alive" whenever it cannot prove otherwise, because the caller
// uses it to decide whether to take somebody else's update lock away. Only a
// positive proof of death may do that.
//
// os.FindProcess is not that proof on Windows. It opens the process for
// query, terminate and synchronise, which fails with ACCESS_DENIED for a
// perfectly live process owned by another account — pid 1 and pid 4 among
// them. Reading that failure as death would trample the lock of an update
// still running.
//
// PROCESS_QUERY_LIMITED_INFORMATION is the narrowest right that still
// answers the question, and it is granted across accounts. When even that is
// refused, the process exists and something else is stopping us; only
// ERROR_INVALID_PARAMETER means there is no such process.
func lockPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == currentPid() {
		return true
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == nil {
		defer windows.CloseHandle(h) //nolint:errcheck
		// An exited process keeps a handle until every reference is closed,
		// so a successful open still has to ask whether it is running.
		var code uint32
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			return true
		}
		return code == stillActive
	}
	return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
}

// stillActive is STILL_ACTIVE (259), the exit code Windows reports for a
// process that has not exited.
const stillActive uint32 = 259
