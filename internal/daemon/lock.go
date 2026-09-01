package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

// acquireLock takes the single-instance lock $home/daemon.lock via
// create-exclusive, writing this process's pid. A lock held by a live
// process refuses with ErrAlreadyRunning; a stale lock (dead pid,
// unreadable, or garbage content) is removed and retaken.
func acquireLock(home string) (release func(), err error) {
	path := filepath.Join(home, "daemon.lock")
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := fmt.Fprintf(f, "%d\n", os.Getpid())
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				os.Remove(path)
				return nil, werr
			}
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("daemon: lock: %w", err)
		}
		raw, rerr := os.ReadFile(path)
		if rerr == nil {
			pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if perr == nil && pidAlive(pid) {
				return nil, fmt.Errorf("%w (pid %d, lock %s)", ErrAlreadyRunning, pid, path)
			}
		}
		// Stale leftover from a crashed daemon: clear it and try again.
		os.Remove(path)
	}
	return nil, fmt.Errorf("daemon: could not acquire lock %s", path)
}

// pidAlive reports whether pid names a live process.
//
// Delegated rather than implemented here. The version that lived at this spot
// read os.FindProcess succeeding as proof of life on Windows -- its comment
// said "on Windows os.FindProcess fails for dead pids", and that is simply
// not so: it fails for an INVALID pid, never for a dead one. Measured on the
// pid from a real stale lock:
//
//	os.FindProcess(3664)     err=nil                       -> "alive"
//	GetExitCodeProcess(3664) exitCode 0, not STILL_ACTIVE  -> dead
//
// So on Windows a stale lock was never cleared, and a daemon that died
// without releasing its own blocked every later daemon for that home
// permanently. This machine's had been blocked for two days, reported as
// "spawned but not ready within 10s" -- the symptom, never the cause.
//
// internal/update already had the correct check, with the ACCESS_DENIED and
// ERROR_INVALID_PARAMETER cases reasoned through. A third copy of that
// reasoning is how a fourth one goes wrong.
func pidAlive(pid int) bool { return csxupdate.LockPidAlive(pid) }
