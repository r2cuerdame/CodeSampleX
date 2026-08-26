//go:build !windows

package launcher

// POSIX permits unlinking and replacing a pathname while an inspected file
// descriptor remains open, so a read-then-remove takeover would not be an
// atomic identity check. The Windows launcher incident is recovered with the
// handle-pinned implementation; other platforms conservatively leave an
// existing lock alone rather than risk deleting a successor owner's lock.
func tryTakeOverRecoveryInstallLock(_ string, _ string) (unlock func(), acquired, retry bool, err error) {
	return nil, false, false, nil
}
