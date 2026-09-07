//go:build windows

package main

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestWindowsBootstrapRegistryIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-File", "windows-registry-state-test.ps1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("registry preservation regression: %v (context: %v, started: %s, elapsed: %v)\n%s", err, ctx.Err(), started.UTC().Format(time.RFC3339Nano), time.Since(started), out)
	} else {
		t.Logf("registry preservation completed in %v\n%s", time.Since(started), out)
	}
}
