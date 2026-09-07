//go:build windows

package main

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestWindowsBootstrapRegistryIsolation(t *testing.T) {
	// The native Windows job runs the whole repository concurrently. Even with
	// module-free comparisons, hosted process startup can be starved behind the
	// long sandbox suites. Keep a hard bound without turning scheduler pressure
	// into a false release-gate failure.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-File", "windows-registry-state-test.ps1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("registry preservation regression: %v (context: %v, elapsed: %v)\n%s", err, ctx.Err(), time.Since(started), out)
	} else {
		t.Logf("registry preservation completed in %v\n%s", time.Since(started), out)
	}
}
