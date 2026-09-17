package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/autoupdate"
	"github.com/r2cuerdame/codesamplex/internal/config"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

// stubAutomaticUpdateForTest replaces the shared autoupdate loop's network
// call with onCheck and shrinks its poll interval, so a test can observe
// whether the daemon reached the update loop at all without touching a real
// signed manifest.
//
// The returned stop func cancels ctx via cancel, then blocks on d's
// updateLoopDone before restoring the package-level vars. Without that wait
// this would race: startBackground's update goroutine can still be reading
// autoupdate.RunCheck/PollInterval for a moment after ctx is cancelled, and
// restoring them from the test goroutine at the same time is exactly the
// unsynchronized concurrent access `go test -race` exists to catch.
func stubAutomaticUpdateForTest(t *testing.T, d *Daemon, interval time.Duration, onCheck func()) (stop func(cancel context.CancelFunc)) {
	t.Helper()
	oldInterval, oldCheck := autoupdate.PollInterval, autoupdate.RunCheck
	autoupdate.PollInterval = interval
	autoupdate.RunCheck = func(context.Context, *csxupdate.Client) (csxupdate.Result, error) {
		onCheck()
		return csxupdate.Result{}, nil
	}
	return func(cancel context.CancelFunc) {
		cancel()
		select {
		case <-d.updateLoopDone:
		case <-time.After(5 * time.Second):
			t.Fatal("automatic update loop did not stop after its context was cancelled")
		}
		autoupdate.PollInterval, autoupdate.RunCheck = oldInterval, oldCheck
	}
}

// newOwnedFakeExecutable writes a throwaway file and registers it as the
// standalone install this home owns, the same way the real installer's
// `csx update adopt` step does. Tests use it so the automatic-update loop's
// OwnsExecutable gate can pass without touching a real installed csx binary.
func newOwnedFakeExecutable(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "csx")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	return exe
}

// This is the exact shape of GitHub issue #457: a community install whose
// only long-running process is the background sync daemon must still reach
// the signed update endpoint on a bounded cadence. Before this change,
// startBackground never called into the updater at all — MCP and the
// contributor worker were the only production call sites, so a daemon-only
// install could stay on an old release indefinitely.
func TestDaemonOnlyInstallRunsTheAutomaticUpdateLoop(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeCommunity
		c.AutoUpdate = "auto"
	})
	exe := newOwnedFakeExecutable(t)
	if err := csxupdate.AdoptStandalone(home, exe); err != nil {
		t.Fatal(err)
	}

	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.Executable = exe

	var checks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	stop := stubAutomaticUpdateForTest(t, d, 20*time.Millisecond, func() { checks.Add(1) })
	defer stop(cancel)
	d.startBackground(ctx)

	deadline := time.After(2 * time.Second)
	for checks.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("daemon-only background loop never reached the automatic update check")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestDaemonOnlyAutoUpdateOffMakesNoCheck(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeCommunity
		c.AutoUpdate = "off"
	})
	exe := newOwnedFakeExecutable(t)
	if err := csxupdate.AdoptStandalone(home, exe); err != nil {
		t.Fatal(err)
	}
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.Executable = exe

	var checks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	stop := stubAutomaticUpdateForTest(t, d, 10*time.Millisecond, func() { checks.Add(1) })
	d.startBackground(ctx)
	time.Sleep(150 * time.Millisecond)
	stop(cancel)

	if got := checks.Load(); got != 0 {
		t.Fatalf("autoUpdate=off made %d automatic update checks", got)
	}
}

func TestDaemonOnlyLocalOnlyMakesNoCheck(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeLocalOnly
	})
	exe := newOwnedFakeExecutable(t)
	// Ownership can exist even in local-only mode (a user can flip modes
	// later); AutoEnabled must refuse on mode alone.
	if err := csxupdate.AdoptStandalone(home, exe); err != nil {
		t.Fatal(err)
	}
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.Executable = exe

	var checks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	stop := stubAutomaticUpdateForTest(t, d, 10*time.Millisecond, func() { checks.Add(1) })
	d.startBackground(ctx)
	time.Sleep(150 * time.Millisecond)
	stop(cancel)

	if got := checks.Load(); got != 0 {
		t.Fatalf("local-only made %d automatic update checks", got)
	}
}

func TestDaemonOnlyUnownedExecutableNeverSelfModifies(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeCommunity
		c.AutoUpdate = "auto"
	})
	exe := newOwnedFakeExecutable(t)
	// Deliberately never AdoptStandalone: this executable is not the one
	// the install marker owns (e.g. a `go build` dev binary, or a package-
	// manager-owned MCPB payload).
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.Executable = exe

	var checks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	stop := stubAutomaticUpdateForTest(t, d, 10*time.Millisecond, func() { checks.Add(1) })
	d.startBackground(ctx)
	time.Sleep(150 * time.Millisecond)
	stop(cancel)

	if got := checks.Load(); got != 0 {
		t.Fatalf("unowned executable made %d automatic update checks", got)
	}
	if got, err := os.ReadFile(exe); err != nil || string(got) != "old" {
		t.Fatalf("unowned executable was modified: %q, err=%v", got, err)
	}
}

func TestDaemonStatusSurfacesAPendingRestartFromASharedUpdateState(t *testing.T) {
	home := newTestHome(t, nil)
	_, c := startDaemon(t, home)
	ctx := context.Background()

	if err := csxupdate.SaveState(home, csxupdate.State{Schema: 1, PendingRestart: "v9.9.9"}); err != nil {
		t.Fatal(err)
	}

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.UpdatePendingRestart != "v9.9.9" {
		t.Fatalf("status did not surface the pending restart: %+v", st)
	}
}
