package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func plantDir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func parseJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func TestAgentInstallNothingDetected(t *testing.T) {
	home := t.TempDir()
	results := installAgents(home, nil)
	if len(results) != 4 {
		t.Fatalf("got %d results, want 4", len(results))
	}
	for _, r := range results {
		if !r.Skipped {
			t.Errorf("%s: not skipped despite absent agent dir", r.Agent)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("install created files in an agent-free home: %v", entries)
	}
}

func TestAgentInstallClaudePreservesExistingJSON(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".claude")
	origJSON := `{"otherKey":true,"mcpServers":{"foo":{"command":"foo-bin"}}}`
	writeFile(t, filepath.Join(home, ".claude.json"), origJSON)
	origMD := "# My rules\n\nBe nice.\n"
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), origMD)

	results := installAgents(home, nil)
	var claude *agentInstallResult
	for i := range results {
		if results[i].Agent == "Claude Code" {
			claude = &results[i]
		}
	}
	if claude == nil || claude.Skipped || claude.Err != nil {
		t.Fatalf("claude result = %+v", claude)
	}

	m := parseJSONFile(t, filepath.Join(home, ".claude.json"))
	if m["otherKey"] != true {
		t.Errorf("otherKey lost: %v", m)
	}
	servers, _ := m["mcpServers"].(map[string]any)
	if _, ok := servers["foo"]; !ok {
		t.Errorf("existing mcpServers.foo lost: %v", m)
	}
	csx, _ := servers["csx"].(map[string]any)
	// Absolute path, not a bare name: agents inherit an older PATH.
	if csx["command"] != mcpCommand() {
		t.Errorf("mcpServers.csx = %v, want command %q", servers["csx"], mcpCommand())
	}
	if !filepath.IsAbs(mcpCommand()) {
		t.Errorf("mcpCommand() = %q, want an absolute path", mcpCommand())
	}
	args, _ := csx["args"].([]any)
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("csx args = %v", args)
	}

	md := readFile(t, filepath.Join(home, ".claude", "CLAUDE.md"))
	if !strings.Contains(md, origMD[:len(origMD)-1]) {
		t.Errorf("original CLAUDE.md content lost:\n%s", md)
	}
	if strings.Count(md, "<!-- csx:begin -->") != 1 || strings.Count(md, "<!-- csx:end -->") != 1 {
		t.Errorf("expected exactly one fenced block:\n%s", md)
	}
	if !strings.Contains(md, "search_known_solution") ||
		!strings.Contains(md, "run_observed_command") ||
		!strings.Contains(md, "report_sample_adoption") {
		t.Errorf("rule text missing MCP tool guidance:\n%s", md)
	}

	// Backups hold the pre-install content.
	if got := readFile(t, filepath.Join(home, ".claude.json.csx-backup")); got != origJSON {
		t.Errorf(".claude.json backup = %q, want original", got)
	}
	if got := readFile(t, filepath.Join(home, ".claude", "CLAUDE.md.csx-backup")); got != origMD {
		t.Errorf("CLAUDE.md backup = %q, want original", got)
	}
}

func TestAgentInstallClaudeIdempotentAndBackupOnce(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".claude")
	origJSON := `{"otherKey":1}`
	writeFile(t, filepath.Join(home, ".claude.json"), origJSON)

	installAgents(home, nil)
	firstJSON := readFile(t, filepath.Join(home, ".claude.json"))
	firstMD := readFile(t, filepath.Join(home, ".claude", "CLAUDE.md"))

	installAgents(home, nil) // second run must change nothing

	if got := readFile(t, filepath.Join(home, ".claude.json")); got != firstJSON {
		t.Errorf(".claude.json changed on second run:\n%s", got)
	}
	md := readFile(t, filepath.Join(home, ".claude", "CLAUDE.md"))
	if md != firstMD {
		t.Errorf("CLAUDE.md changed on second run:\n%s", md)
	}
	if strings.Count(md, "<!-- csx:begin -->") != 1 {
		t.Errorf("duplicate fenced blocks after double run:\n%s", md)
	}
	// The backup still holds the ORIGINAL content: never overwritten.
	if got := readFile(t, filepath.Join(home, ".claude.json.csx-backup")); got != origJSON {
		t.Errorf("backup overwritten on second run: %q", got)
	}
	// CLAUDE.md was created by us; no backup should ever appear for it.
	if _, err := os.Stat(filepath.Join(home, ".claude", "CLAUDE.md.csx-backup")); !os.IsNotExist(err) {
		t.Errorf("backup created for a file csx itself created, stat err=%v", err)
	}
}

func TestAgentInstallCodex(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".codex")
	orig := "model = \"o4-mini\"\n"
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), orig)

	installAgents(home, nil)
	installAgents(home, nil) // idempotent

	toml := readFile(t, filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(toml, "model = \"o4-mini\"") {
		t.Errorf("existing toml content lost:\n%s", toml)
	}
	if strings.Count(toml, "# csx:begin") != 1 || strings.Count(toml, "# csx:end") != 1 {
		t.Errorf("expected exactly one marker block:\n%s", toml)
	}
	quoted, _ := json.Marshal(mcpCommand())
	if !strings.Contains(toml, "[mcp_servers.csx]") ||
		!strings.Contains(toml, "command = "+string(quoted)) ||
		!strings.Contains(toml, `args = ["mcp"]`) {
		t.Errorf("MCP registration missing:\n%s", toml)
	}
	if got := readFile(t, filepath.Join(home, ".codex", "config.toml.csx-backup")); got != orig {
		t.Errorf("backup = %q, want original", got)
	}
	rules := readFile(t, filepath.Join(home, ".codex", "AGENTS.md"))
	if strings.Count(rules, "<!-- csx:begin -->") != 1 || !strings.Contains(rules, "search_known_solution") {
		t.Errorf("codex rule block wrong:\n%s", rules)
	}
}

func TestAgentInstallGeminiMerge(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".gemini")
	writeFile(t, filepath.Join(home, ".gemini", "settings.json"), `{"theme":"dark"}`)

	installAgents(home, nil)

	m := parseJSONFile(t, filepath.Join(home, ".gemini", "settings.json"))
	if m["theme"] != "dark" {
		t.Errorf("existing settings lost: %v", m)
	}
	servers, _ := m["mcpServers"].(map[string]any)
	csx, _ := servers["csx"].(map[string]any)
	if csx == nil || csx["command"] != mcpCommand() {
		t.Errorf("mcpServers.csx = %v, want command %q", m, mcpCommand())
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "GEMINI.md")); err != nil {
		t.Errorf("gemini rule file missing: %v", err)
	}
}

func TestAgentInstallOpenCodeMerge(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".config", "opencode")
	writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"),
		`{"mcp":{"other":{"type":"remote","url":"http://x"}},"theme":"t"}`)

	installAgents(home, nil)

	m := parseJSONFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"))
	if m["theme"] != "t" {
		t.Errorf("existing keys lost: %v", m)
	}
	mcp, _ := m["mcp"].(map[string]any)
	if _, ok := mcp["other"]; !ok {
		t.Errorf("existing mcp.other lost: %v", m)
	}
	csx, _ := mcp["csx"].(map[string]any)
	if csx == nil || csx["type"] != "local" {
		t.Fatalf("mcp.csx = %v", m)
	}
	cmd, _ := csx["command"].([]any)
	if len(cmd) != 2 || cmd[0] != mcpCommand() || cmd[1] != "mcp" {
		t.Errorf("mcp.csx.command = %v", cmd)
	}
}

func TestAgentInstallConfirmDeclined(t *testing.T) {
	home := t.TempDir()
	plantDir(t, home, ".claude")
	plantDir(t, home, ".gemini")

	var asked []string
	results := installAgents(home, func(agent string) bool {
		asked = append(asked, agent)
		return agent == "Gemini CLI"
	})

	if len(asked) != 2 {
		t.Fatalf("confirm asked for %v, want the 2 detected agents", asked)
	}
	for _, r := range results {
		switch r.Agent {
		case "Claude Code":
			if !r.Skipped {
				t.Errorf("declined claude was installed")
			}
		case "Gemini CLI":
			if r.Skipped || r.Err != nil {
				t.Errorf("accepted gemini not installed: %+v", r)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf(".claude.json written despite decline")
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "settings.json")); err != nil {
		t.Errorf("gemini settings missing: %v", err)
	}
}

// CSX_AGENT_HOME is the explicit escape hatch that keeps automated runs
// off the real user home: when set it wins over the OS-home seam.
func TestResolveAgentHome(t *testing.T) {
	seam := t.TempDir()
	seamFn := func() (string, error) { return seam, nil }

	t.Setenv("CSX_AGENT_HOME", "")
	got, overridden, err := resolveAgentHome(seamFn)
	if err != nil || overridden || got != seam {
		t.Fatalf("unset: got (%q, %v, %v), want (%q, false, nil)", got, overridden, err, seam)
	}

	override := t.TempDir()
	t.Setenv("CSX_AGENT_HOME", override)
	got, overridden, err = resolveAgentHome(seamFn)
	if err != nil || !overridden || got != override {
		t.Fatalf("set: got (%q, %v, %v), want (%q, true, nil)", got, overridden, err, override)
	}

	// A blank value is treated as unset rather than as the process CWD.
	t.Setenv("CSX_AGENT_HOME", "   ")
	got, overridden, err = resolveAgentHome(seamFn)
	if err != nil || overridden || got != seam {
		t.Fatalf("blank: got (%q, %v, %v), want (%q, false, nil)", got, overridden, err, seam)
	}
}

func TestUpsertMarkerBlock(t *testing.T) {
	got := upsertMarkerBlock("", "<!-- b -->", "<!-- e -->", "inner")
	want := "<!-- b -->\ninner\n<!-- e -->\n"
	if got != want {
		t.Fatalf("empty: %q", got)
	}
	// Append keeps existing content and separates with a blank line.
	got = upsertMarkerBlock("existing\n", "<!-- b -->", "<!-- e -->", "inner")
	if !strings.HasPrefix(got, "existing\n") || !strings.Contains(got, want) {
		t.Fatalf("append: %q", got)
	}
	// Replacing is idempotent.
	if again := upsertMarkerBlock(got, "<!-- b -->", "<!-- e -->", "inner"); again != got {
		t.Fatalf("not idempotent:\n%q\nvs\n%q", got, again)
	}
	// Replace swaps inner content in place.
	swapped := upsertMarkerBlock(got, "<!-- b -->", "<!-- e -->", "other")
	if strings.Contains(swapped, "inner") || !strings.Contains(swapped, "other") {
		t.Fatalf("replace: %q", swapped)
	}
}
