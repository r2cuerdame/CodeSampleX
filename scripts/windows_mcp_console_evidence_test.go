//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestWindowsMCPConsoleEvidenceScriptParses keeps the #90 evidence harness
// runnable. The harness itself needs a desktop session, an installed release
// and Windows Terminal on the desk, so CI cannot run it; what CI can hold is
// that the script still parses and still documents every mode it accepts.
func TestWindowsMCPConsoleEvidenceScriptParses(t *testing.T) {
	src, err := os.ReadFile("windows-mcp-console-evidence.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"host-consoleless", "host-console", "control", "watch"} {
		if !strings.Contains(string(src), "#   "+mode) {
			t.Errorf("mode %q is accepted by ValidateSet but not described in the header comment", mode)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Parse only: no Add-Type, no process spawn, no console changes.
	check := `$t = $null; $e = $null
[System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path 'windows-mcp-console-evidence.ps1').Path, [ref]$t, [ref]$e) | Out-Null
if ($e.Count) { $e | ForEach-Object { Write-Output ("{0}: {1}" -f $_.Extent.StartLineNumber, $_.Message) }; exit 1 }
Write-Output 'parse ok'`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", check)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("windows-mcp-console-evidence.ps1 does not parse: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "parse ok") {
		t.Fatalf("unexpected parser output:\n%s", out)
	}
}
