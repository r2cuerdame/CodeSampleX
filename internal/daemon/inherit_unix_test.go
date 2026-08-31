//go:build !windows

package daemon

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// A descriptor this process holds must not survive into a spawned daemon.
//
// On the farm the daemon came up holding the agent's stdout pipe on fd 10, so
// the tee reading that pipe never saw EOF, the agent's pipeline never
// completed, and the worker script never looped to the next assignment.
func TestInheritedDescriptorsAreSealedBeforeSpawn(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	// os.Pipe sets CLOEXEC; clear it to model a descriptor inherited from a
	// parent that did not, which is how the farm's pipe arrived.
	fd := int(w.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	if got, _ := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); got&unix.FD_CLOEXEC != 0 {
		t.Fatal("could not clear CLOEXEC for the fixture")
	}

	sealInheritedDescriptors()

	got, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got&unix.FD_CLOEXEC == 0 {
		t.Error("an inherited descriptor would still reach the spawned daemon")
	}
	// The descriptor stays usable here: CLOEXEC changes nothing until exec.
	if _, err := w.Write([]byte("x")); err != nil {
		t.Errorf("sealing broke the descriptor for this process: %v", err)
	}
}
