//go:build !windows

package daemon

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// sealInheritedDescriptors marks every descriptor above stderr close-on-exec,
// so a daemon this process is about to spawn inherits nothing.
//
// It is not hypothetical. On the farm, `csx mcp` runs as a child of the agent,
// whose stdout is a pipe into `tee`, and the daemon it spawned came up holding
// that pipe on fd 10:
//
//	0  -> /dev/null
//	1  -> logs/daemon.log
//	2  -> logs/daemon.log
//	10 -> pipe:[50380004]      <- the agent's stdout, still open
//
// The daemon outlives the agent by design, so tee never saw EOF, the
// `agy | tee` pipeline never completed, and the worker script sat on that line
// forever instead of looping to the next assignment. Throughput fell from six
// or seven assignments an hour per slot to one, and the cause looked like the
// worker exiting after one job.
//
// Before v0.1.70 this was invisible: only one daemon could bind the shared
// port, so on a node running several homes the spawn simply never happened.
// Home isolation made every home spawn its own, and the leak came with it.
//
// Setting FD_CLOEXEC rather than closing: this process is still using those
// descriptors. The flag changes nothing until exec, which is exactly the
// boundary that matters. Everything here is best effort — a descriptor that
// cannot be flagged is left alone rather than costing the spawn.
func sealInheritedDescriptors() {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd <= 2 {
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC)
	}
}
