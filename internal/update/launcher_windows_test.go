//go:build windows

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

func TestUpdaterAndLauncherSerializeAfterStaleInstallLockTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".update.lock")
	if err := os.WriteFile(path, []byte("deadbeef 999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type owner struct {
		name   string
		unlock func()
		err    error
	}
	acquired := make(chan owner, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for name, acquire := range map[string]func() (func(), error){
		"updater":  func() (func(), error) { return acquireNamedLock(path, 2*time.Second) },
		"launcher": func() (func(), error) { return launcher.AcquireUpdateLock(path, 2*time.Second) },
	} {
		name, acquire := name, acquire
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			unlock, err := acquire()
			acquired <- owner{name: name, unlock: unlock, err: err}
		}()
	}
	close(start)

	var first owner
	select {
	case first = <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("neither participant acquired the stale lock")
	}
	if first.err != nil {
		t.Fatalf("first participant %s failed: %v", first.name, first.err)
	}
	select {
	case second := <-acquired:
		if second.unlock != nil {
			second.unlock()
		}
		first.unlock()
		t.Fatalf("%s and %s owned the shared lock concurrently", first.name, second.name)
	case <-time.After(200 * time.Millisecond):
	}
	first.unlock()

	var second owner
	select {
	case second = <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second participant did not acquire after release")
	}
	if second.err != nil {
		t.Fatalf("second participant %s failed: %v", second.name, second.err)
	}
	second.unlock()
	workers.Wait()
}

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

func TestWindowsLauncherAutomaticUpdateNeverReinstallsRollbackHeldPayload(t *testing.T) {
	c, home, root, oldPath := launcherClientFixture(t)
	c.Automatic = true
	if res, err := c.Check(context.Background(), true); err != nil || !res.Applied {
		t.Fatalf("apply=%+v err=%v", res, err)
	}
	if _, err := Rollback(home, oldPath); err != nil {
		t.Fatal(err)
	}

	active, err := launcher.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if active.RollbackHold == nil || active.RollbackHold.Sequence == 0 {
		t.Fatalf("rollback pointer=%+v", active)
	}
	// Simulate the server reissuing the exact rejected bytes under a sequence
	// newer than the locally recorded hold. Identity, not only sequence, must
	// keep an automatic update from reactivating a rolled-back payload.
	active.RollbackHold.Sequence--
	if err := launcher.Write(root, active); err != nil {
		t.Fatal(err)
	}

	before, err := launcher.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Check(context.Background(), true)
	if err != nil || !res.RollbackHeld || res.Applied {
		t.Fatalf("held retry=%+v err=%v", res, err)
	}
	after, err := launcher.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.Current != before.Current || after.RollbackHold == nil || before.RollbackHold == nil ||
		*after.RollbackHold != *before.RollbackHold {
		t.Fatalf("held retry changed pointer: before=%+v after=%+v", before, after)
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

// launcherRootState is everything about an install that an update must not
// disturb unless it fully succeeded.
func launcherRootState(t *testing.T, root string) (launcher.Active, []string) {
	t.Helper()
	a, err := launcher.Load(root)
	if err != nil {
		t.Fatalf("install is not loadable: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "payloads"))
	if err != nil {
		t.Fatal(err)
	}
	versions := make([]string, 0, len(entries))
	for _, e := range entries {
		versions = append(versions, e.Name())
	}
	return a, versions
}

// The interrupted-update contract, on the install shape that actually broke: a
// download that dies partway, a body that does not match the signed hash, and a
// payload that fails its self-test all have to leave the running install exactly
// as it was -- same current, and no half-made payloads/<version>/ for the next
// launcher to trip over.
func TestWindowsLauncherFailedUpdateLeavesCurrentAndPayloadsUntouched(t *testing.T) {
	for name, breakUpdate := range map[string]func(t *testing.T, c *Client){
		"download dies partway": func(t *testing.T, c *Client) {
			c.HTTP = &http.Client{Transport: truncatingRoundTripper{inner: c.HTTP.Transport.(updateRoundTripper)}}
		},
		"body does not match the signed hash": func(t *testing.T, c *Client) {
			c.HTTP = &http.Client{Transport: updateRoundTripper{manifest: c.HTTP.Transport.(updateRoundTripper).manifest, binary: []byte("tampered-payload")}}
		},
		"payload fails its self-test": func(t *testing.T, c *Client) {
			c.SelfTest = func(context.Context, string, string) error {
				return errors.New("staged payload did not answer --version")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, _, root, _ := launcherClientFixture(t)
			before, beforeVersions := launcherRootState(t, root)
			breakUpdate(t, c)

			res, err := c.Check(context.Background(), true)
			if err == nil || res.Applied {
				t.Fatalf("failed update reported res=%+v err=%v", res, err)
			}
			after, afterVersions := launcherRootState(t, root)
			if after.Current != before.Current {
				t.Fatalf("current moved from %+v to %+v", before.Current, after.Current)
			}
			if strings.Join(afterVersions, ",") != strings.Join(beforeVersions, ",") {
				t.Fatalf("payload directories changed from %v to %v", beforeVersions, afterVersions)
			}
			// The staged download is scratch, never something a launcher could
			// resolve; nothing matching it may survive in the install root.
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".csx-update-") {
					t.Fatalf("failed update left staged binary %s in the install root", e.Name())
				}
			}
		})
	}
}

// truncatingRoundTripper answers with a body that stops short of the signed
// size, which is what a dropped connection looks like to the updater.
type truncatingRoundTripper struct{ inner updateRoundTripper }

func (r truncatingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.inner.RoundTrip(req)
	if err != nil || strings.Contains(req.URL.Path, "update-stable") {
		return resp, err
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(strings.NewReader(string(raw[:len(raw)/2])))
	return resp, nil
}

// The successful counterpart, on the same install: the new payload becomes
// current only once it is in its final place and hashes correctly, and the
// payload it replaced is kept as the last-known-good the launcher recovers onto.
func TestWindowsLauncherSuccessfulUpdateKeepsTheReplacedPayloadRunnable(t *testing.T) {
	c, _, root, _ := launcherClientFixture(t)
	res, err := c.Check(context.Background(), true)
	if err != nil || !res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	a, err := launcher.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Current.Version != "v1.1.0" || a.Previous == nil || a.Previous.Version != "v1.0.0" {
		t.Fatalf("active=%+v", a)
	}
	if err := launcher.Validate(root, a); err != nil {
		t.Fatalf("last-known-good is not runnable after the update: %v", err)
	}
	// And the recovery this whole issue is about now has somewhere to go.
	if err := os.Remove(mustPayload(t, root, a.Current.Version)); err != nil {
		t.Fatal(err)
	}
	got, err := launcher.Resolve(root)
	if err != nil || !got.Recovered || got.Descriptor.Version != "v1.0.0" {
		t.Fatalf("resolve=%+v err=%v", got, err)
	}
}

func mustPayload(t *testing.T, root, version string) string {
	t.Helper()
	path, err := launcher.PayloadPath(root, version)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
