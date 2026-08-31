package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/launcher"
	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

type automaticUpdateResult struct {
	Result csxupdate.Result
	Err    error
}

var automaticUpdatePollInterval = 10 * time.Minute

var runAutomaticUpdateCheck = func(ctx context.Context, client *csxupdate.Client) (csxupdate.Result, error) {
	return client.Check(ctx, true)
}

// automaticUpdates is a test seam shared by the worker and stdio MCP. The
// production loop contacts the release endpoint only with explicit community
// consent (or autoUpdate=on) and only for a first-party standalone install.
var automaticUpdates = func(ctx context.Context, home string, cfg *config.Config, exe string) <-chan automaticUpdateResult {
	out := make(chan automaticUpdateResult, 1)
	go func() {
		defer close(out)
		_ = csxupdate.AcknowledgeActivation(home, Version)
		if cfg == nil || !csxupdate.AutoEnabled(cfg.Mode, cfg.AutoUpdate) {
			return
		}
		owned, err := csxupdate.OwnsExecutable(home, exe)
		if err != nil || !owned {
			return
		}
		client := &csxupdate.Client{Home: home, CurrentVersion: Version, Executable: exe, Channel: cfg.UpdateChannel, Automatic: true}
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
				out <- automaticUpdateResult{Err: loadErr}
				return
			}
			if !csxupdate.AutoEnabled(currentCfg.Mode, currentCfg.AutoUpdate) {
				return
			}
			client.Channel = currentCfg.UpdateChannel
			if client.Due() {
				res, err := runAutomaticUpdateCheck(ctx, client)
				if errors.Is(err, csxupdate.ErrPolicyDisabled) {
					return
				}
				out <- automaticUpdateResult{Result: res, Err: err}
				if res.Applied {
					return
				}
			}
			t := time.NewTimer(automaticUpdatePollInterval)
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

func init() {
	Register(Command{
		Name:    "update",
		Summary: "securely check, install, inspect, or roll back csx updates",
		Run:     updateMain,
	})
}

// reportPayloadHealth says whether this install lost a payload and had to
// repair itself.
//
// Both commands that report on the install call it. `csx update status` is
// where an operator goes deliberately; `csx stats` is the local dashboard a
// user opens when something feels wrong, and until now it was the one place
// that could not tell them their payload had been destroyed and replaced.
//
// Silent on every install that has nothing to report, which is almost all of
// them: a dashboard that always mentions recovery teaches people to skip the
// line that matters.
func reportPayloadHealth(home, exe string) {
	reportLauncherRecovery(home, exe)
	reportLauncherRehydrate(home, exe)
}

// reportLauncherRecovery surfaces the one thing a healthy-looking Windows// reportLauncherRecovery surfaces the one thing a healthy-looking Windows
// install will not otherwise tell anybody: that the launcher had to fall back
// to a last-known-good payload because the current one was destroyed after it
// was verified.
//
// The recovery is designed to succeed, so the command that triggered it exits
// 0 and the pointer is repaired; from the next run on, nothing anywhere says a
// released payload was lost. Every occurrence on this project's own Windows
// workstation so far was Microsoft Defender quarantining `csx-payload.exe` as
// a false positive, which is a release-quality fact, not a launcher fact.
//
// It writes to stdout because `csx update status` is a report a person asked
// for, and it stays silent when there is nothing to report — including on
// every non-launcher install, which has no such record and never will.
func reportLauncherRecovery(home, exe string) {
	in, err := csxupdate.ResolveInstall(home, exe)
	if err != nil || in.Kind != "launcher" || in.InstallRoot == "" {
		return
	}
	rec, ok, err := launcher.ReadRecoveryRecord(in.InstallRoot)
	if err != nil {
		// A record that cannot be read is still evidence that one exists.
		fmt.Fprintf(os.Stderr, "csx update: payload recovery record is unreadable: %v\n", err)
		return
	}
	if !ok {
		return
	}
	fmt.Printf("payload recovery: %s\n", rec.Summary())
	fmt.Printf("payload recovery last seen: %s\n", rec.LastObservedAt.Local().Format("2006-01-02 15:04:05"))
}

// reportLauncherRehydrate surfaces the stronger fact: this install ran out of
// payloads entirely and had to refetch one from the release it came from.
//
// A local recovery says one payload was destroyed and a spare was still there.
// This says there was no spare — the whole recovery set on the machine was
// gone — and it stays worth reporting long after the repair succeeded, because
// nothing else on a repaired install remembers that it happened.
func reportLauncherRehydrate(home, exe string) {
	in, err := csxupdate.ResolveInstall(home, exe)
	if err != nil || in.Kind != "launcher" || in.InstallRoot == "" {
		return
	}
	rec, ok, err := launcher.ReadRehydrateRecord(in.InstallRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx update: payload repair record is unreadable: %v\n", err)
		return
	}
	if !ok {
		return
	}
	fmt.Printf("payload repair: %s\n", rec.Summary())
	fmt.Printf("payload repair last attempted: %s\n", rec.AttemptedAt.Local().Format("2006-01-02 15:04:05"))
}

func updateMain(ctx context.Context, args []string) int {
	sub := "apply"
	if len(args) > 0 {
		sub = args[0]
	}
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx update: %v\n", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx update: locate executable: %v\n", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	_ = csxupdate.AcknowledgeActivation(home, Version)

	switch sub {
	case "bootstrap-launcher":
		if len(args) < 3 || len(args) > 4 {
			fmt.Fprintln(os.Stderr, "csx update: invalid installer bootstrap arguments")
			return 2
		}
		legacy := ""
		if len(args) == 4 {
			legacy = args[3]
		}
		if _, err := csxupdate.BootstrapLauncher(ctx, args[1], args[2], legacy, Version); err != nil {
			fmt.Fprintf(os.Stderr, "csx update: bootstrap launcher: %v\n", err)
			return 1
		}
		return 0
	case "adopt":
		// Called by the first-party installers only. A package-manager-owned
		// MCPB must never call this or self-modify its managed directory.
		if err := csxupdate.AdoptStandalone(home, exe); err != nil {
			fmt.Fprintf(os.Stderr, "csx update: adopt standalone install: %v\n", err)
			return 1
		}
		fmt.Printf("csx update: standalone install registered at %s\n", exe)
		return 0
	case "status":
		st, err := csxupdate.LoadState(home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx update: %v\n", err)
			return 1
		}
		owned, _ := csxupdate.OwnsExecutable(home, exe)
		fmt.Printf("current: %s\n", Version)
		fmt.Printf("standalone owned: %t\n", owned)
		if st.HighestVersion != "" {
			fmt.Printf("highest trusted: %s (sequence %d)\n", st.HighestVersion, st.HighestSequence)
		}
		if st.PendingRestart != "" {
			fmt.Printf("restart required: %s\n", st.PendingRestart)
		}
		if !st.LastCheck.IsZero() {
			fmt.Printf("last check: %s\n", st.LastCheck.Local().Format("2006-01-02 15:04:05"))
		}
		if !st.NextCheck.IsZero() {
			fmt.Printf("next automatic check: %s\n", st.NextCheck.Local().Format("2006-01-02 15:04:05"))
		}
		if st.LastError != "" {
			fmt.Printf("last error: %s\n", st.LastError)
		}
		reportPayloadHealth(home, exe)
		return 0
	case "rollback":
		path, err := csxupdate.Rollback(home, exe)
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx update: %v\n", err)
			return 1
		}
		fmt.Printf("Previous csx restored at %s. Restart the worker, daemon, and MCP client.\n", path)
		return 0
	case "check", "apply":
		cfg, err := config.Load(home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx update: %v\n", err)
			return 1
		}
		client := &csxupdate.Client{Home: home, CurrentVersion: Version, Executable: exe, Channel: cfg.UpdateChannel}
		res, err := client.Check(ctx, sub == "apply")
		if err != nil && !res.Applied {
			fmt.Fprintf(os.Stderr, "csx update: %v\n", err)
			return 1
		}
		if !res.Available {
			fmt.Printf("csx %s is current on the %s channel.\n", Version, cfg.UpdateChannel)
			return 0
		}
		if res.ManualInstallRequired {
			fmt.Printf("csx %s is available, but this Windows install needs the stable launcher migration or a newer launcher protocol. Rerun the official installer.\n", res.LatestVersion)
			if sub == "apply" {
				return 1
			}
			return 0
		}
		if !res.Applied {
			fmt.Printf("csx %s is available (current %s).\n", res.LatestVersion, Version)
			return 0
		}
		fmt.Printf("Installed csx %s; previous binary kept at %s.\n", res.LatestVersion, res.PreviousPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx update: installed, but post-install durability reporting failed: %v\n", err)
		}
		fmt.Println("Restart the MCP client to activate the update. Native worker services restart automatically.")
		return 0
	case "help", "--help", "-h":
		fmt.Println("usage: csx update [check|status|rollback]")
		fmt.Println("  csx update         verify and install the latest signed stable release")
		fmt.Println("  csx update check   check without installing")
		fmt.Println("  csx update status  show local update state")
		fmt.Println("  csx update rollback restore the preserved previous binary")
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: csx update [check|status|rollback]")
		return 2
	}
}
