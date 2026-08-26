//go:build windows

package launcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestDeletePendingStaleLockRetriesWithinTheWaitBudget(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".update.lock")
	if err := os.WriteFile(path, []byte("deadbeef 999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	held, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	heldOpen := true
	defer func() {
		if heldOpen {
			_ = windows.CloseHandle(held)
		}
	}()

	originalHook := recoveryLockBeforeDisposition
	disposing := make(chan struct{})
	recoveryLockBeforeDisposition = func() { close(disposing) }
	defer func() { recoveryLockBeforeDisposition = originalHook }()

	type outcome struct {
		unlock func()
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		unlock, err := AcquireUpdateLock(path, 2*time.Second)
		done <- outcome{unlock: unlock, err: err}
	}()
	select {
	case <-disposing:
	case <-time.After(time.Second):
		t.Fatal("stale takeover never reached disposition")
	}
	// Let FileDispositionInfo run while the share-delete handle keeps the old
	// identity delete-pending. Acquisition must wait, not fail early.
	time.Sleep(100 * time.Millisecond)
	select {
	case got := <-done:
		if got.unlock != nil {
			got.unlock()
		}
		t.Fatalf("acquisition returned before delete-pending cleared: %v", got.err)
	default:
	}
	if err := windows.CloseHandle(held); err != nil {
		t.Fatal(err)
	}
	heldOpen = false

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		got.unlock()
	case <-time.After(2 * time.Second):
		t.Fatal("acquisition did not resume after delete-pending cleared")
	}
}
