package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
	"github.com/r2cuerdame/codesamplex/internal/update"
)

// nativeDoctor is used only when no payload can run the full CLI doctor. A
// read-only invocation never passes through launcher's automatic recovery.
func nativeDoctor(root string, active *launcher.Active, cause error) int {
	fix, asJSON := false, false
	for _, arg := range os.Args[2:] {
		switch arg {
		case "--fix":
			fix = true
		case "--json":
			asJSON = true
		case "--verbose":
		default:
			fmt.Fprintln(os.Stderr, "usage: csx doctor [--fix] [--verbose] [--json]")
			return 2
		}
	}
	code, detail := launcher.Reason(cause), "Active payload cannot be verified"
	var startFailure *childStartError
	if errors.As(cause, &startFailure) {
		code, detail = launcher.ReasonPayloadStartFailed, "Active payload could not start"
	}
	if active == nil {
		code, detail = "pointer-invalid", "Launcher pointer cannot be verified"
	}
	if fix && active != nil {
		local := os.Getenv("LOCALAPPDATA")
		if runtime.GOOS != "windows" || local == "" || !strings.EqualFold(filepath.Clean(root), filepath.Clean(filepath.Join(local, "csx"))) || update.SafeLauncherRepairTree(root, active.Current.Version) != nil {
			code, detail = "install-root-untrusted", "Launcher is outside the CSX-owned install root"
			fix = false
		}
	}
	if fix && active != nil {
		ctx, cancel := context.WithTimeout(context.Background(), repairBudget)
		defer cancel()
		m, err := update.VerifyInstalledStableRelease(ctx, active.Current.Version, nil)
		if err == nil {
			var asset update.Asset
			asset, err = update.CurrentAsset(m)
			if err == nil && (asset.SHA256 != active.Current.SHA256 || m.Sequence != active.Current.Sequence) {
				err = fmt.Errorf("signed release binding mismatch")
			}
		}
		if err == nil {
			_, err = update.RehydrateInstall(ctx, root, update.RehydrateOptions{Force: true, StartFailed: startFailure != nil})
		}
		if err == nil {
			var res launcher.Resolution
			res, err = launcher.Resolve(root)
			if err == nil {
				var exit int
				exit, err = runResolution(res, filepath.Join(root, "csx.exe"), root)
				if err == nil {
					finish(exit)
					return 0
				}
			}
		}
		code, detail = "repair-failed", "Payload repair failed or failed re-verification"
	}
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"schemaVersion": 1, "health": "UNHEALTHY",
			"checks": []map[string]string{{"id": "payload", "status": "FAIL", "code": code, "detail": detail,
				"action": "Run csx doctor --fix; if it fails, run the official installer"}},
		})
	} else {
		fmt.Fprintf(os.Stdout, "FAIL payload %s (%s)\n", detail, code)
		fmt.Fprintln(os.Stdout, "  action: run csx doctor --fix; if it fails, run the official installer")
		fmt.Fprintln(os.Stdout, "UNHEALTHY")
	}
	return 1
}
