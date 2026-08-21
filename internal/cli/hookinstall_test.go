package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// claudeFailureHook digs out the one hook entry this installer owns, so a test
// asserts the shape the agent actually reads rather than the shape we meant.
func claudeFailureHook(t *testing.T, home string) map[string]any {
	t.Helper()
	m := parseJSONFile(t, filepath.Join(home, ".claude", "settings.json"))
	hooks, _ := m["hooks"].(map[string]any)
	events, _ := hooks["PostToolUseFailure"].([]any)
	for _, e := range events {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); cmd == mcpCommand() {
				return map[string]any{"entry": entry, "hook": hm}
			}
		}
	}
	return nil
}

// The lookup has to arrive without being asked for. An agent that must be
// told to enable it is an agent that never enables it, and the whole problem
// this hook exists for is that nobody remembers to ask.
func TestInstallingClaudeRegistersTheFailureHook(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".claude")

	if _, err := installClaude(home); err != nil {
		t.Fatal(err)
	}

	got := claudeFailureHook(t, home)
	if got == nil {
		t.Fatal("csx init installed no build-failure hook")
	}
	entry := got["entry"].(map[string]any)
	hook := got["hook"].(map[string]any)

	// Measured against a live session: a Bash command exiting non-zero fires
	// PostToolUseFailure and does NOT fire PostToolUse.
	if m, _ := entry["matcher"].(string); m != "Bash" {
		t.Errorf("matcher = %q, want Bash", m)
	}
	if ty, _ := hook["type"].(string); ty != "command" {
		t.Errorf("type = %q, want command", ty)
	}
	// Exec form. The Windows install path is
	// C:\Users\<name>\AppData\Local\csx\csx.exe, and a name with a space in
	// it breaks any shell string we could write here.
	args, _ := hook["args"].([]any)
	if len(args) != 2 || args[0] != "hook" || args[1] != "agent" {
		t.Errorf("args = %v, want [hook agent]", args)
	}
	if _, ok := hook["timeout"]; !ok {
		t.Error("no timeout: a hook that hangs holds up somebody's session")
	}
}

// Somebody else's settings are not ours to lose.
func TestInstallingClaudeKeepsTheirOwnHooks(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".claude")
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
  "theme": "dark",
  "hooks": {
    "PostToolUseFailure": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "their-script"}]}
    ],
    "SessionStart": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "theirs-too"}]}
    ]
  }
}`)

	if _, err := installClaude(home); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"their-script", "theirs-too", "dark"} {
		if !hookFileHas(string(raw), keep) {
			t.Errorf("install dropped %q from their settings:\n%s", keep, raw)
		}
	}
	if claudeFailureHook(t, home) == nil {
		t.Error("our hook was not added alongside theirs")
	}
}

// Re-running the installer is the normal case — every update does it.
func TestInstallingClaudeTwiceRegistersOneHook(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".claude")

	if _, err := installClaude(home); err != nil {
		t.Fatal(err)
	}
	if _, err := installClaude(home); err != nil {
		t.Fatal(err)
	}

	m := parseJSONFile(t, filepath.Join(home, ".claude", "settings.json"))
	hooks, _ := m["hooks"].(map[string]any)
	events, _ := hooks["PostToolUseFailure"].([]any)
	count := 0
	for _, e := range events {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); cmd == mcpCommand() {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("registered %d copies of the hook, want 1", count)
	}
}

func hookFileHas(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && jsonIndex(haystack, needle) >= 0
}

func jsonIndex(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

var _ = json.Marshal
