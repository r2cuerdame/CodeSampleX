//go:build windows

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

func launcherClientFixture(t *testing.T) (*Client, string, string, string) {
	t.Helper()
	c, _, _ := clientFixture(t, []byte("new-payload"), 7)
	home, local := t.TempDir(), t.TempDir()
	root := filepath.Join(local, "csx")
	t.Setenv("LOCALAPPDATA", local)
	oldPath, err := launcher.PayloadPath(root, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old-payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("old-payload"))
	old := launcher.Descriptor{Version: "v1.0.0", SHA256: hex.EncodeToString(sum[:]), Sequence: 6}
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: old}); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(stable, []byte("launcher"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CSX_LAUNCHER_ROOT", root)
	t.Setenv("CSX_LAUNCHER_PATH", stable)
	t.Setenv("CSX_PAYLOAD_VERSION", old.Version)
	t.Setenv("CSX_ACTIVE_SEQUENCE", "6")
	t.Setenv("CSX_ACTIVE_SHA256", old.SHA256)
	if err := AdoptStandalone(home, oldPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CSX_LAUNCHER_VERSION", launcher.ProtocolVersion)
	c.Home, c.Executable, c.directApplyForTests = home, oldPath, false
	return c, home, root, oldPath
}

func TestWindowsLauncherApplyRetryAndOldPayloadOwnership(t *testing.T) {
	c, home, root, oldPath := launcherClientFixture(t)
	c.SaveState = func(string, State) error { return errors.New("injected state failure") }
	res, err := c.Check(context.Background(), true)
	if err == nil || !res.Applied {
		t.Fatalf("commit res=%+v err=%v", res, err)
	}
	a, err := launcher.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Current.Version != "v1.1.0" || a.Previous == nil || a.Previous.Version != "v1.0.0" {
		t.Fatalf("active=%+v", a)
	}
	if owned, err := OwnsExecutable(home, oldPath); err != nil || !owned {
		t.Fatalf("old running payload ownership=%t err=%v", owned, err)
	}
	c.SaveState = nil
	res, err = c.Check(context.Background(), true)
	if err != nil || !res.Applied {
		t.Fatalf("retry res=%+v err=%v", res, err)
	}
	a, _ = launcher.Load(root)
	if a.Previous == nil || a.Previous.Version != "v1.0.0" {
		t.Fatalf("retry lost previous: %+v", a)
	}
	wantStable, err := resolveExistingPath(filepath.Join(root, "csx.exe"))
	if err != nil {
		t.Fatal(err)
	}
	stable, err := StableExecutable(home, oldPath)
	if err != nil || stable != wantStable {
		t.Fatalf("stable=%q err=%v", stable, err)
	}
	customHome := t.TempDir()
	if err := writeJSONAtomic(InstallPath(customHome), Install{Schema: 1, Kind: "standalone", ExecutablePath: filepath.Join(customHome, "stale.exe")}); err != nil {
		t.Fatal(err)
	}
	if owned, err := OwnsExecutable(customHome, oldPath); err != nil || !owned {
		t.Fatalf("custom-home ownership=%t err=%v", owned, err)
	}
	stable, err = StableExecutable(customHome, oldPath)
	if err != nil || stable != wantStable {
		t.Fatalf("custom-home stable=%q err=%v", stable, err)
	}
	c.Home = customHome
	res, err = c.Check(context.Background(), true)
	if err != nil || !res.Applied {
		t.Fatalf("custom-home update res=%+v err=%v", res, err)
	}
}

func TestWindowsLauncherRollbackStateFailureDoesNotRetoggle(t *testing.T) {
	c, home, root, oldPath := launcherClientFixture(t)
	if res, err := c.Check(context.Background(), true); err != nil || !res.Applied {
		t.Fatalf("apply=%+v err=%v", res, err)
	}
	oldSave := rollbackSaveState
	rollbackSaveState = func(string, State) error { return errors.New("injected state failure") }
	t.Cleanup(func() { rollbackSaveState = oldSave })
	if _, err := Rollback(home, oldPath); err == nil || !strings.Contains(err.Error(), "state save failed") {
		t.Fatalf("rollback error=%v", err)
	}
	a, err := launcher.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Current.Version != "v1.0.0" || a.RollbackHold == nil {
		t.Fatalf("rollback pointer=%+v", a)
	}
	if _, err := Rollback(home, oldPath); err == nil {
		t.Fatal("retry toggled rollback back to rejected version")
	}
	a, _ = launcher.Load(root)
	if a.Current.Version != "v1.0.0" {
		t.Fatalf("retry changed pointer=%+v", a)
	}
}

func TestInstallRootLockSerializesDifferentHomes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".update.lock")
	unlock, err := acquireNamedLock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		second, err := acquireNamedLock(path, time.Second)
		if err == nil {
			second()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("second home bypassed install lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
