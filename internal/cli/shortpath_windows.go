//go:build windows

package cli

import (
	"strings"
	"syscall"
)

// spaceFreePath returns a form of p that contains no space, or reports that
// there is none.
//
// Codex runs a hook's command as one shell-ish string and offers no argument
// array, so a path with a space in it cannot be passed at all: measured
// against Codex 0.149.0, a quoted path containing a space silently never ran,
// while the same file reached through its 8.3 short name ran and received its
// argument. The ordinary Windows install lives under
// C:\Users\<name>\AppData\Local, so any user whose name has a space in it is
// the case this exists for.
//
// Short names can be disabled per volume (fsutil 8dot3name), in which case
// there is no space-free form and this says so rather than inventing one.
func spaceFreePath(p string) (string, bool) {
	if !strings.Contains(p, " ") {
		return p, true
	}
	long, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return "", false
	}
	n, err := syscall.GetShortPathName(long, nil, 0)
	if err != nil || n == 0 {
		return "", false
	}
	buf := make([]uint16, n)
	n, err = syscall.GetShortPathName(long, &buf[0], n)
	if err != nil || n == 0 {
		return "", false
	}
	short := syscall.UTF16ToString(buf[:n])
	if short == "" || strings.Contains(short, " ") {
		return "", false
	}
	return short, true
}
