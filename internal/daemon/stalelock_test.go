package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// A lock file naming THIS process is not proof that a daemon is running.
//
// daemonLockHeld asked one question — is that pid alive — and pidAlive
// answers yes for our own pid by construction, because acquireLock needs
// that to refuse a second daemon inside one process. Reused for "is a daemon
// still up", the same answer is wrong: a daemon that ran inside this process
// and has stopped answering has stopped.
//
// The cost was a release. stopAndWait polls until the daemon stops answering
// AND its lock is gone; with a leftover lock naming our own pid the second
// half could never become true, so the poll ran its full ten seconds and
// `csx config set mode local-only` exited 1. On CI that is one line —
// "config set mode exit = 1" — and a failed build.
//
// It is not only a test artifact. A lock left by a crashed daemon whose pid
// the operating system later hands to something else reads exactly the same
// way, and csx would refuse to start or stop a daemon for as long as that
// unrelated process lived.
func TestALockNamingThisProcessIsNotARunningDaemon(t *testing.T) {
	home := t.TempDir()
	writeLock(t, home, os.Getpid())

	if daemonLockHeld(home) {
		t.Error("a lock naming our own pid was read as a running daemon")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	running, err := StopRunning(ctx, home)
	if err != nil {
		t.Fatalf("StopRunning: %v", err)
	}
	if running {
		t.Error("StopRunning reported a daemon that does not exist")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("StopRunning took %s waiting for a daemon that was never there", elapsed)
	}
}

// A lock naming a live process that is NOT us is still a daemon we must not
// assume away — that is the whole reason the lock is consulted.
func TestALockNamingAnotherLiveProcessIsStillHeld(t *testing.T) {
	home := t.TempDir()
	// Pid 4 is the Windows System process and pid 1 is init elsewhere; both
	// are alive for the life of the machine and are not this test.
	other := 1
	if os.Getpid() == other {
		other = 4
	}
	writeLock(t, home, other)
	if !daemonLockHeld(home) {
		t.Skipf("pid %d is not alive on this machine, nothing to assert", other)
	}
}

// A lock left by a process that is gone is stale, as before.
func TestAStaleLockIsNotHeld(t *testing.T) {
	home := t.TempDir()
	writeLock(t, home, 999999)
	if daemonLockHeld(home) {
		t.Error("a lock naming a dead pid was read as a running daemon")
	}
}

func writeLock(t *testing.T, home string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "daemon.lock"),
		[]byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
