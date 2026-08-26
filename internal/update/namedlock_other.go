//go:build !windows

package update

import "time"

func acquireNamedLock(path string, wait time.Duration) (func(), error) {
	return acquireNamedLockLegacy(path, wait)
}
