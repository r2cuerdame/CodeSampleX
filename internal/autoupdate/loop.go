// Package autoupdate holds the automatic signed-update polling loop shared
// by every long-running csx process: the stdio MCP server, the contributor
// worker, and the background sync daemon. It is deliberately its own
// package rather than living in internal/cli or internal/daemon: it needs
// both internal/config and internal/update, and internal/config already
// imports internal/update, so it cannot live in either without a cycle —
// and internal/daemon must be able to run it without importing
// internal/cli, which imports internal/daemon.
package autoupdate

import (
	"context"
	"errors"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

// Outcome is one result the loop reports: either an update.Result or an
// error from the check itself.
type Outcome struct {
	Result csxupdate.Result
	Err    error
}

// PollInterval is how often the loop wakes to ask client.Due(). A var so
// tests can shrink it; production leaves it at the default.
var PollInterval = 10 * time.Minute

// RunCheck performs one update check. A var so a test can replace the
// network call with a stub while still exercising the loop's control flow
// (consent reload, stop-on-Applied, stop-on-ErrPolicyDisabled).
var RunCheck = func(ctx context.Context, client *csxupdate.Client) (csxupdate.Result, error) {
	return client.Check(ctx, true)
}

// Loop contacts the release endpoint only with explicit community consent
// (or autoUpdate=on) and only for a first-party standalone install that
// owns exe. It reloads config on every iteration so a consent revocation
// mid-loop is honored before the next network request, and it stops after
// an applied update — what "restart required" means is left to the caller,
// since a stdio MCP server, a contributor worker and a background daemon
// each activate a verified update differently.
func Loop(ctx context.Context, home string, cfg *config.Config, exe, currentVersion string) <-chan Outcome {
	out := make(chan Outcome, 1)
	go func() {
		defer close(out)
		_ = csxupdate.AcknowledgeActivation(home, currentVersion)
		if cfg == nil || !csxupdate.AutoEnabled(cfg.Mode, cfg.AutoUpdate) {
			return
		}
		owned, err := csxupdate.OwnsExecutable(home, exe)
		if err != nil || !owned {
			return
		}
		client := &csxupdate.Client{Home: home, CurrentVersion: currentVersion, Executable: exe, Channel: cfg.UpdateChannel, Automatic: true}
		client.Preflight = func() error {
			currentCfg, err := config.Load(home)
			if err != nil {
				return err
			}
			if !csxupdate.AutoEnabled(currentCfg.Mode, currentCfg.AutoUpdate) {
				return csxupdate.ErrPolicyDisabled
			}
			client.Channel = currentCfg.UpdateChannel
			return nil
		}
		for {
			currentCfg, loadErr := config.Load(home)
			if loadErr != nil {
				out <- Outcome{Err: loadErr}
				return
			}
			if !csxupdate.AutoEnabled(currentCfg.Mode, currentCfg.AutoUpdate) {
				return
			}
			client.Channel = currentCfg.UpdateChannel
			if client.Due() {
				res, err := RunCheck(ctx, client)
				if errors.Is(err, csxupdate.ErrPolicyDisabled) {
					return
				}
				out <- Outcome{Result: res, Err: err}
				if res.Applied {
					return
				}
			}
			t := time.NewTimer(PollInterval)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}()
	return out
}
