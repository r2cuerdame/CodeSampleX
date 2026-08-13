package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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

// pidAlive reports whether pid names a live process, best-effort. Our own
// pid is always alive (a second daemon in the same process must refuse).
// On Windows os.FindProcess fails for dead pids; on Unix it always
// succeeds, so signal 0 probes for liveness there.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer p.Release() //nolint:errcheck
	if runtime.GOOS == "windows" {
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}
