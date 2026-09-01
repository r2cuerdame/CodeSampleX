package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// A lock naming a process that has exited must be taken.
//
// Measured on this workstation 2026-09-01: the daemon had been unable to
// start since 2026-08-30 because $HOME/.csx/daemon.lock named pid 3664, which
// no longer existed. `csx daemon start` reported "spawned but not ready
// within 10s" -- the symptom -- while the child was refusing with
// ErrAlreadyRunning against a two-day-old corpse.
//
// The cause was pidAlive. It read os.FindProcess succeeding as proof of life
// on Windows, and that is not what it proves: FindProcess fails only for an
// INVALID pid, not a dead one. Measured on the very pid in that lock:
//
//	os.FindProcess(3664)      err=nil                      -> "alive"
//	GetExitCodeProcess(3664)  exitCode=0, not STILL_ACTIVE  -> dead
//
// So on Windows no stale lock was ever cleared, and a daemon that died
// without releasing its lock blocked every later daemon for that home
// permanently. Two other packages in this repository already had the correct
// check; only this one did not.
func TestALockLeftByADeadProcessIsTaken(t *testing.T) {
	home := t.TempDir()

	// A real process, really exited -- not a pid picked for being unused,
	// which on Windows is the case that accidentally worked before.
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", "exit", "0")
	} else {
		cmd = exec.Command("true")
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("probe process: %v", err)
	}
	dead := cmd.Process.Pid

	lock := filepath.Join(home, "daemon.lock")
	if err := os.WriteFile(lock, []byte(itoa(dead)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	release, err := acquireLock(home)
	if err != nil {
		t.Fatalf("a lock held by pid %d, which has exited, was treated as live: %v", dead, err)
	}
	defer release()

	raw, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got == itoa(dead)+"\n" {
		t.Error("the stale lock was kept rather than retaken")
	}
}

// And a lock held by a process that IS running is left alone: taking it would
// put two daemons on one home, which is the thing the lock exists to prevent.
func TestALockHeldByALiveProcessIsRefused(t *testing.T) {
	home := t.TempDir()
	lock := filepath.Join(home, "daemon.lock")
	// This test process is unquestionably alive.
	if err := os.WriteFile(lock, []byte(itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(home); err == nil {
		t.Fatal("a lock held by a live process was taken")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
