package update

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeUpdateLock(t *testing.T, path, token string, pid int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%s %d\n", token, pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

// A lock file left by a process that is gone blocked every future update,
// permanently and silently.
//
// acquireNamedLock created the file exclusively and removed it on release,
// and had no answer for a release that never came: a crash, a kill, a reboot
// mid-update. From then on every `csx update` returned "another update is
// still in progress" and auto-update simply stopped, with the machine
// sitting on whatever version it had.
//
// Found on a real install: update.lock naming pid 4680, dead, while the
// daemon ran a build fifteen releases old.
func TestAnUpdateLockFromADeadProcessIsTakenOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	writeUpdateLock(t, path, "deadbeef", 999999, 0)

	start := time.Now()
	release, err := acquireNamedLock(path, 2*time.Second)
	if err != nil {
		t.Fatalf("a lock held by a dead process was not taken over: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s to take over a dead lock", elapsed)
	}
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("release did not remove the lock it took over")
	}
}

// A lock held by a process that IS alive is still respected — that is what
// the lock is for.
func TestAnUpdateLockHeldByALiveProcessIsRespected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	writeUpdateLock(t, path, "abcdef", os.Getpid(), 0)

	if _, err := acquireNamedLock(path, 300*time.Millisecond); err == nil {
		t.Error("took a lock that a live process is holding")
	}
}

// Content nobody can be identified from is a lock nobody can release. It is
// left alone while it could still belong to an update in flight, and taken
// over once it plainly cannot: no update runs for an hour.
func TestAnUnreadableUpdateLockIsTakenOverOnlyWhenOld(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "update.lock")
	if err := os.WriteFile(fresh, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireNamedLock(fresh, 300*time.Millisecond); err == nil {
		t.Error("took over an unreadable lock that had only just appeared")
	}

	old := filepath.Join(t.TempDir(), "update.lock")
	if err := os.WriteFile(old, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, when, when); err != nil {
		t.Fatal(err)
	}
	release, err := acquireNamedLock(old, 2*time.Second)
	if err != nil {
		t.Fatalf("an unreadable two-day-old lock was not taken over: %v", err)
	}
	release()
}

// A live owner is never overruled, however old its lock looks. An update on
// a slow link can hold this for a long time, and age alone proves nothing
// about whether somebody is still working — trampling it would corrupt the
// thing the lock exists to protect.
func TestAnOldLockWithALiveOwnerIsStillRespected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	writeUpdateLock(t, path, "abcdef", os.Getpid(), 72*time.Hour)

	if _, err := acquireNamedLock(path, 300*time.Millisecond); err == nil {
		t.Error("a three-day-old lock was taken from an owner that is still alive")
	}
}
