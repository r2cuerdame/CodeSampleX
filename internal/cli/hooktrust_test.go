package cli

import (
	"strings"
	"testing"
)

// Codex will not run a hook it has not been told to trust, and trust is
// recorded against the hook's exact definition. A third party cannot grant it:
// only managed/MDM sources are trusted by policy.
//
// So this is the one place the "install it and it is already connected" story
// is not true, and the installer has to say so. An install that registers
// something which silently never fires is worse than one that registers
// nothing, because nobody goes looking for a feature they were told they had.
func TestCodexInstallSaysTheHookNeedsTrusting(t *testing.T) {
	notice := agentFollowUp([]agentInstallResult{
		{Agent: "Claude Code", Actions: []string{"installed MCP server → x"}},
		{Agent: "Codex", Actions: []string{"installed MCP server and build-failure lookup → y"}},
	})
	if notice == "" {
		t.Fatal("Codex was set up and nothing said the hook must be trusted")
	}
	for _, want := range []string{"Codex", "/hooks", "trust"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice = %q, want it to mention %q", notice, want)
		}
	}
}

// Nothing to say when Codex was not part of this install.
func TestNoTrustNoticeWithoutCodex(t *testing.T) {
	for _, results := range [][]agentInstallResult{
		nil,
		{{Agent: "Claude Code", Actions: []string{"installed MCP server → x"}}},
		{{Agent: "Codex", Skipped: true, Reason: "not detected"}},
		{{Agent: "Codex", Skipped: true, Reason: "declined"}},
	} {
		if got := agentFollowUp(results); got != "" {
			t.Errorf("results %v produced a Codex notice: %q", results, got)
		}
	}
}
