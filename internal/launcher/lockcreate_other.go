//go:build !windows

package launcher

func recoveryLockCreateErrorIsTransient(error) bool { return false }
