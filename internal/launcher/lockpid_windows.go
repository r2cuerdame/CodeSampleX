//go:build windows

package launcher

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// recoveryLockPidAlive is deliberately conservative: taking the updater's
// install lock away is safe only when Windows positively proves its owner is
// gone. Access denial and other unknown errors therefore mean "alive".
func recoveryLockPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == nil {
		defer windows.CloseHandle(h) //nolint:errcheck
		var code uint32
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			return true
		}
		return code == 259 // STILL_ACTIVE
	}
	return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
}
