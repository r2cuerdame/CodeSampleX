//go:build !windows

package update

import "context"

// replaceLauncherIfStale is Windows-only: the launcher is a Windows concept,
// and the rename-a-running-executable dance it depends on is too.
//
// A stub rather than a runtime check inside the real implementation, because
// that implementation calls retryRename -- which exists only in
// replace_windows.go. A cross-platform file that references it compiles on
// the developer's Windows machine and fails in CI on Linux, which is exactly
// how this file came to exist.
func (c *Client) replaceLauncherIfStale(context.Context, string, Asset) (bool, error) {
	return false, nil
}
