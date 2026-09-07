//go:build windows

package main

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestWindowsBootstrapRegistryIsolation(t *testing.T) {
	// The native Windows job runs the whole repository concurrently. On the
	// hosted runner, process startup for this sub-test can be starved behind the
	// long sandbox suites even though the script itself completes in under a
	// second. Keep a hard bound, but do not turn scheduler pressure into a false
	// release-gate failure.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-File", "windows-registry-state-test.ps1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("registry preservation regression: %v (context: %v)\n%s", err, ctx.Err(), out)
	}
}
