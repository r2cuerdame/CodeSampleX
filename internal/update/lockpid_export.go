package update

// LockPidAlive reports whether pid names a live process, conservatively: it
// answers "alive" whenever it cannot prove otherwise, because every caller
// uses it to decide whether to take somebody else's single-instance lock
// away, and only a positive proof of death may do that.
//
// Exported so the daemon can stop carrying its own version. That version read
// os.FindProcess succeeding as proof of life on Windows, which it is not --
// FindProcess fails only for an INVALID pid, never for a dead one -- so no
// stale daemon lock was ever cleared there. Measured 2026-09-01 on a machine
// whose daemon had been unable to start for two days behind a lock naming a
// process that had exited.
func LockPidAlive(pid int) bool { return lockPidAlive(pid) }
