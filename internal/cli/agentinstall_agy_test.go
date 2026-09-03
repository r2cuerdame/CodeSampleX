package cli

// `csx init` / `csx agent install` registers the MCP server with every agent
// it can find. agy was not on the list.
//
// Reported 2026-09-02 as "installing does not connect the MCP in agy". The
// immediate cause that day was Windows Defender quarantining the csx
// launcher itself; but once the launcher was back, the entry agy had for
// csx turned out to have been added by hand, and a fresh machine gets none:
// the installer knows Claude Code, Codex, Gemini CLI and OpenCode, and agy
// is the agent the farm's authoring workers actually run.
//
// agy keeps its MCP configuration in a store this installer cannot write
// directly -- no JSON file was found under %APPDATA%, %LOCALAPPDATA% or the
// home directory -- but it exposes `agy mcp add <name> <command> [args...]`,
// documented as "add or update", so registration goes through that command
// and is idempotent. The command is the launcher's absolute path, as it is
// for every other agent: a bare `csx` resolves only if the agent's own
// environment happens to have the install directory on PATH.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingExec captures the command the installer would run instead of
// running it, and answers as agy does on success.
type recordingExec struct {
	calls [][]string
	err   error
}

func (r *recordingExec) run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.err != nil {
		return nil, r.err
	}
	return []byte("Added MCP server csx\n"), nil
}

func TestAgentInstallRegistersAgyThroughItsOwnCommand(t *testing.T) {
	home := t.TempDir()
	agyExe := filepath.Join(home, "AppData", "Local", "agy", "bin", "agy.exe")
	if err := os.MkdirAll(filepath.Dir(agyExe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agyExe, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recordingExec{}
	restore := useAgentExec(rec.run, func(string) string { return agyExe })
	defer restore()

	actions, err := installAgy(home)
	if err != nil {
		t.Fatalf("installAgy: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("agy was invoked %d times, want exactly one `mcp add`: %v", len(rec.calls), rec.calls)
	}
	got := rec.calls[0]
	want := []string{agyExe, "mcp", "add", "csx", mcpCommand(), "mcp"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("agy invocation\n got %q\nwant %q", got, want)
	}
	if !filepath.IsAbs(got[4]) {
		t.Fatalf("registered command %q is not absolute; agy's environment may not have csx on PATH", got[4])
	}
	if len(actions) == 0 || !strings.Contains(strings.Join(actions, "\n"), "MCP server") {
		t.Fatalf("no action reported for the agy registration: %v", actions)
	}
}

// A machine without agy is skipped, not failed, like every other absent
// agent.
func TestAgentInstallSkipsAgyWhenAbsent(t *testing.T) {
	home := t.TempDir()
	rec := &recordingExec{}
	restore := useAgentExec(rec.run, func(string) string { return "" })
	defer restore()

	results := installAgents(home, nil)
	for _, r := range results {
		if r.Agent != "agy" {
			continue
		}
		if !r.Skipped || r.Err != nil {
			t.Fatalf("agy absent but result = %+v", r)
		}
		if len(rec.calls) != 0 {
			t.Fatalf("agy absent but the installer invoked %v", rec.calls)
		}
		return
	}
	t.Fatal("installAgents has no agy entry")
}

// agy refusing the registration is reported, not hidden: the user was told
// the agent is set up when it is not otherwise.
func TestAgentInstallReportsAgyRefusal(t *testing.T) {
	home := t.TempDir()
	rec := &recordingExec{err: os.ErrPermission}
	restore := useAgentExec(rec.run, func(string) string { return filepath.Join(home, "agy.exe") })
	defer restore()

	if _, err := installAgy(home); err == nil {
		t.Fatal("agy mcp add failed but installAgy returned nil")
	}
}

// useAgentExec swaps the installer's exec and locate seams for a test and
// returns the restore.
func useAgentExec(run func(string, ...string) ([]byte, error), locate func(string) string) func() {
	prevExec, prevLocate := agentExec, locateAgy
	agentExec, locateAgy = run, locate
	return func() { agentExec, locateAgy = prevExec, prevLocate }
}
