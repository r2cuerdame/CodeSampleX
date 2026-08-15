package cli

import (
	"strings"
	"testing"
)

// Piped into sh, stdin is the pipe and the per-agent prompt is never seen.
// EOF was reported as `skipped (declined)` — a statement about a user who
// was never asked — and init then finished with "csx is ready". The
// consequence is the whole product: an unregistered MCP server is one no
// agent is ever told to call, and nothing in the output said the
// registration had not happened.
func TestEOFIsNotADecline(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".claude")
	plantDir(t, home, ".gemini")

	// confirm behaves as the real one does at EOF: not ok, not asked.
	var prompted int
	results := installAgents(home, func(agent string) (bool, bool) {
		prompted++
		return false, false
	})

	for _, r := range results {
		if r.Reason == "declined" {
			t.Errorf("%s reported as declined for a question nobody was asked", r.Agent)
		}
		if r.Skipped && r.Reason != "not detected" && !strings.Contains(r.Reason, "not asked") {
			t.Errorf("%s skipped with reason %q", r.Agent, r.Reason)
		}
	}
	if prompted == 0 {
		t.Fatal("no agent was offered at all")
	}
}

// An answered "n" is still a decline, and still says so.
func TestAnActualNoIsStillADecline(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".claude")
	results := installAgents(home, func(agent string) (bool, bool) { return false, true })
	var saw bool
	for _, r := range results {
		if r.Skipped && r.Reason == "declined" {
			saw = true
		}
	}
	if !saw {
		t.Error("an explicit no was not recorded as a decline")
	}
}
