//go:build windows

package launcher

import (
	"errors"

	"golang.org/x/sys/windows"
)

// A stale file marked for deletion can remain delete-pending while a scanner
// that shared delete still has it open. The pathname is unavailable only
// transiently; honor the caller's wait budget instead of abandoning healing.
func recoveryLockCreateErrorIsTransient(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_DELETE_PENDING)
}
