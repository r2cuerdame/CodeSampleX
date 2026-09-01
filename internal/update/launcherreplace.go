//go:build windows

package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// replaceLauncherIfStale installs the launcher this release publishes, when
// the one on disk is not it.
//
// `csx update` replaced the payload and nothing else, so a machine kept
// whatever launcher it was installed with forever: the only code that
// downloads one lives in install.ps1. Measured on a workstation running a
// current payload -- its launcher was 26 releases old, missing both the
// quarantine rehydrate and the console-subsystem build. Launcher-side fixes
// were shipping to a release page and stopping there.
//
// Best effort by construction. The payload update has already committed by
// the time this runs, and a launcher that cannot be fetched, verified or
// swapped must not turn a successful update into a failure -- the machine is
// left exactly as it was, running the launcher it already had, and the next
// update tries again.
func (c *Client) replaceLauncherIfStale(ctx context.Context, root string, asset Asset) (bool, error) {
	if asset.LauncherSHA256 == "" {
		return false, nil
	}
	exe := filepath.Join(root, "csx.exe")
	current, err := fileSHA256(exe)
	if err != nil {
		return false, fmt.Errorf("update: read installed launcher: %w", err)
	}
	if strings.EqualFold(current, asset.LauncherSHA256) {
		return false, nil
	}

	staged, err := c.downloadAsset(ctx, exe, Asset{
		OS: asset.OS, Arch: asset.Arch,
		URL: asset.LauncherURL, Size: asset.LauncherSize, SHA256: asset.LauncherSHA256,
	})
	if err != nil {
		return false, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(staged)
		}
	}()

	if err := launcherSelfTest(ctx, staged); err != nil {
		return false, err
	}

	if err := c.installLauncher(exe, staged); err != nil {
		return false, err
	}
	keep = true
	return true, nil
}

// installLauncher puts staged in place of exe, keeping the displaced binary.
//
// Windows will not let anything WRITE a running executable, and after
// `csx init` this launcher is exactly that. It does allow a RENAME, so the
// old binary moves aside and the new one takes its place; anything already
// running keeps the file it opened.
//
// The aside name is unique per swap, and that is not tidiness. A constant
// name worked once and failed every time after: measured on this workstation
// 2026-09-01, the second update reported "move the installed launcher aside:
// ... Access is denied" because the previous aside was still being EXECUTED
// by the MCP servers started before the last update, and Windows will not
// overwrite that either. On a machine with long-lived MCP servers that is not
// an edge case, it is every update after the first until all of them exit.
// install.ps1 has always used a unique name for exactly this reason.
func (c *Client) installLauncher(exe, staged string) error {
	aside := fmt.Sprintf("%s.previous-%d", exe, time.Now().UnixNano())
	if err := retryRename(exe, aside); err != nil {
		return fmt.Errorf("update: move the installed launcher aside: %w", err)
	}
	if err := retryRename(staged, exe); err != nil {
		// Put back exactly what was there. A machine with no launcher at all
		// is worse than one with an old launcher.
		if back := retryRename(aside, exe); back != nil {
			return fmt.Errorf("update: launcher swap failed (%v) and the previous launcher could not be restored: %w", err, back)
		}
		return fmt.Errorf("update: install the new launcher: %w", err)
	}
	// Sweep the ones nothing is running any more. Best effort by
	// construction: a file still held is a launcher still in use, and
	// failing an update over housekeeping would be absurd.
	removeUnusedAsides(filepath.Dir(exe), filepath.Base(exe), aside)
	return nil
}

// removeUnusedAsides deletes displaced launchers no process still holds open,
// except the one just created. A locked one is left for the next update.
func removeUnusedAsides(dir, base, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, base+".previous-") {
			continue
		}
		path := filepath.Join(dir, name)
		if path == keep {
			continue
		}
		_ = os.Remove(path)
	}
}

// launcherSelfTest runs the staged launcher's own version probe.
//
// The same shape as the payload self-test and for the same reason: a binary
// that will not start must not become the thing every csx invocation goes
// through. It shares the payload's timeout because it has the same cause --
// the first execution of a freshly written PE can be held by real-time
// inspection before its main function gets CPU.
func launcherSelfTest(ctx context.Context, path string) error {
	tctx, cancel := context.WithTimeout(ctx, stagedBinarySelfTestTimeout)
	defer cancel()
	out, err := exec.CommandContext(tctx, path, "--launcher-version").CombinedOutput()
	if err != nil {
		if contextErr := tctx.Err(); contextErr != nil {
			return fmt.Errorf("update: staged launcher self-test did not complete: %w", contextErr)
		}
		return fmt.Errorf("update: staged launcher self-test failed: %w", err)
	}
	got := strings.TrimSpace(string(out))
	if got == "" {
		return errors.New("update: staged launcher printed no version")
	}
	if !strings.HasPrefix(got, "csx-launcher ") {
		return fmt.Errorf("update: staged launcher self-test printed %q", got)
	}
	return nil
}
