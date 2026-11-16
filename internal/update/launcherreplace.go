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

func (c *Client) replaceLauncherIfStale(ctx context.Context, root string, asset Asset) (bool, error) {
	if asset.LauncherSHA256 == "" {
		return false, nil
	}
	exe := filepath.Join(root, "csx.exe")
	current, err := fileSHA256(exe)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("update: read installed launcher: %w", err)
	}
	if err == nil && strings.EqualFold(current, asset.LauncherSHA256) {
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

func (c *Client) installLauncher(exe, staged string) error {
	if _, err := os.Stat(exe); err == nil {
		aside := fmt.Sprintf("%s.previous-%d", exe, time.Now().UnixNano())
		if err := retryRename(exe, aside); err != nil {
			return fmt.Errorf("update: move the installed launcher aside: %w", err)
		}
		if err := retryRename(staged, exe); err != nil {
			if back := retryRename(aside, exe); back != nil {
				return fmt.Errorf("update: launcher swap failed (%v) and the previous launcher could not be restored: %w", err, back)
			}
			return fmt.Errorf("update: install the new launcher: %w", err)
		}
		removeUnusedAsides(filepath.Dir(exe), filepath.Base(exe), aside)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("update: inspect installed launcher: %w", err)
	}

	if err := retryRename(staged, exe); err != nil {
		return fmt.Errorf("update: install the new launcher: %w", err)
	}
	return nil
}

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

func launcherSelfTest(ctx context.Context, path string) error {
	tctx, cancel := context.WithTimeout(ctx, stagedBinarySelfTestTimeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, path, "--launcher-version")
	prepareCommand(cmd)
	out, err := cmd.CombinedOutput()
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
