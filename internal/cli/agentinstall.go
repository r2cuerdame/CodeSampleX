package cli

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// agentRule is the usage rule installed into each detected agent's
// instruction file (goal.md §23 mitigation: "csx init installs
// rule/skill/hook", search before coding, run through observation,
// report adoption).
//
//go:embed agentassets/rule.md
var agentRule string

// Marker fences make every install idempotent and every re-run a clean
// in-place replace instead of an append.
const (
	mdBegin   = "<!-- csx:begin -->"
	mdEnd     = "<!-- csx:end -->"
	tomlBegin = "# csx:begin"
	tomlEnd   = "# csx:end"
)

// codexMCPBlock is the TOML fragment registering the stdio MCP server
// with Codex (fenced by tomlBegin/tomlEnd on write).
const codexMCPBlock = "[mcp_servers.csx]\ncommand = \"csx\"\nargs = [\"mcp\"]"

type agentInstallResult struct {
	Agent   string
	Skipped bool
	Reason  string   // why it was skipped
	Actions []string // human-readable descriptions of what was written
	Err     error
}

// installAgents detects supported coding agents under userHome and, for
// each detected one, registers the csx MCP server ("csx mcp" stdio) and
// installs the usage rule. confirm == nil auto-accepts every agent
// (--yes); otherwise it is asked once per DETECTED agent. Installation
// is mode-independent: local-only users still get agent integration —
// search and evidence stay local, nothing here uploads anything.
func installAgents(userHome string, confirm func(agent string) bool) []agentInstallResult {
	agents := []struct {
		name      string
		detectDir string
		install   func(userHome string) ([]string, error)
	}{
		{"Claude Code", filepath.Join(userHome, ".claude"), installClaude},
		{"Codex", filepath.Join(userHome, ".codex"), installCodex},
		{"Gemini CLI", filepath.Join(userHome, ".gemini"), installGemini},
		{"OpenCode", filepath.Join(userHome, ".config", "opencode"), installOpenCode},
	}

	results := make([]agentInstallResult, 0, len(agents))
	for _, a := range agents {
		r := agentInstallResult{Agent: a.name}
		if fi, err := os.Stat(a.detectDir); err != nil || !fi.IsDir() {
			r.Skipped, r.Reason = true, "not detected"
			results = append(results, r)
			continue
		}
		if confirm != nil && !confirm(a.name) {
			r.Skipped, r.Reason = true, "declined"
			results = append(results, r)
			continue
		}
		r.Actions, r.Err = a.install(userHome)
		results = append(results, r)
	}
	return results
}

// installClaude registers the MCP server in ~/.claude.json (created if
// absent, existing keys preserved) and puts the usage rule into
// ~/.claude/CLAUDE.md as a marker-fenced block.
func installClaude(userHome string) ([]string, error) {
	var actions []string

	cfgPath := filepath.Join(userHome, ".claude.json")
	changed, err := mergeJSONFile(cfgPath, func(m map[string]any) {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers["csx"] = map[string]any{"command": "csx", "args": []any{"mcp"}}
		m["mcpServers"] = servers
	})
	if err != nil {
		return actions, err
	}
	actions = append(actions, verb(changed, "MCP server", cfgPath))

	mdPath := filepath.Join(userHome, ".claude", "CLAUDE.md")
	changed, err = upsertMarkerFile(mdPath, mdBegin, mdEnd, agentRule)
	if err != nil {
		return actions, err
	}
	actions = append(actions, verb(changed, "usage rule", mdPath))
	return actions, nil
}

// installCodex appends a marker-fenced [mcp_servers.csx] block to
// ~/.codex/config.toml and the usage rule to ~/.codex/AGENTS.md.
func installCodex(userHome string) ([]string, error) {
	var actions []string

	tomlPath := filepath.Join(userHome, ".codex", "config.toml")
	changed, err := upsertMarkerFile(tomlPath, tomlBegin, tomlEnd, codexMCPBlock)
	if err != nil {
		return actions, err
	}
	actions = append(actions, verb(changed, "MCP server", tomlPath))

	rulePath := filepath.Join(userHome, ".codex", "AGENTS.md")
	changed, err = upsertMarkerFile(rulePath, mdBegin, mdEnd, agentRule)
	if err != nil {
		return actions, err
	}
	actions = append(actions, verb(changed, "usage rule", rulePath))
	return actions, nil
}

// installGemini merges mcpServers.csx into ~/.gemini/settings.json and
// puts the usage rule into ~/.gemini/GEMINI.md.
func installGemini(userHome string) ([]string, error) {
	var actions []string

	cfgPath := filepath.Join(userHome, ".gemini", "settings.json")
	changed, err := mergeJSONFile(cfgPath, func(m map[string]any) {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers["csx"] = map[string]any{"command": "csx", "args": []any{"mcp"}}
		m["mcpServers"] = servers
	})
	if err != nil {
		return actions, err
	}
	actions = append(actions, verb(changed, "MCP server", cfgPath))

	rulePath := filepath.Join(userHome, ".gemini", "GEMINI.md")
	changed, err = upsertMarkerFile(rulePath, mdBegin, mdEnd, agentRule)
	if err != nil {
		return actions, err
	}
	actions = append(actions, verb(changed, "usage rule", rulePath))
	return actions, nil
}

// installOpenCode merges {"mcp":{"csx":{...}}} into
// ~/.config/opencode/opencode.json and installs the usage rule next to it.
func installOpenCode(userHome string) ([]string, error) {
	var actions []string

	cfgPath := filepath.Join(userHome, ".config", "opencode", "opencode.json")
	changed, err := mergeJSONFile(cfgPath, func(m map[string]any) {
		mcp, _ := m["mcp"].(map[string]any)
		if mcp == nil {
			mcp = map[string]any{}
		}
		mcp["csx"] = map[string]any{"type": "local", "command": []any{"csx", "mcp"}}
		m["mcp"] = mcp
	})
	if err != nil {
		return actions, err
	}
	actions = append(actions, verb(changed, "MCP server", cfgPath))

	rulePath := filepath.Join(userHome, ".config", "opencode", "AGENTS.md")
	changed, err = upsertMarkerFile(rulePath, mdBegin, mdEnd, agentRule)
	if err != nil {
		return actions, err
	}
	actions = append(actions, verb(changed, "usage rule", rulePath))
	return actions, nil
}

func verb(changed bool, what, path string) string {
	if changed {
		return "installed " + what + " → " + path
	}
	return what + " already installed → " + path
}

// backupOnce preserves the pre-csx content of path as path+".csx-backup"
// exactly once: an existing backup is NEVER overwritten, and a file that
// does not exist yet has nothing to preserve. Callers invoke it before
// every write, so the backup always holds the user's original file.
func backupOnce(path string) error {
	bak := path + ".csx-backup"
	if _, err := os.Lstat(bak); err == nil {
		return nil // backup already exists: keep it
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("agentinstall: stat backup %s: %w", bak, err)
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // creating a new file: nothing to back up
	}
	if err != nil {
		return fmt.Errorf("agentinstall: read %s: %w", path, err)
	}
	if err := os.WriteFile(bak, raw, 0o600); err != nil {
		return fmt.Errorf("agentinstall: write backup %s: %w", bak, err)
	}
	return nil
}

// mergeJSONFile loads path as a JSON object (missing file = empty
// object), applies mutate, and writes it back — preserving every
// existing key. It reports whether the file actually changed; unchanged
// content is not rewritten and not backed up.
func mergeJSONFile(path string, mutate func(map[string]any)) (bool, error) {
	m := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		raw = nil
	case err != nil:
		return false, fmt.Errorf("agentinstall: read %s: %w", path, err)
	default:
		if err := json.Unmarshal(raw, &m); err != nil {
			return false, fmt.Errorf("agentinstall: parse %s (left untouched): %w", path, err)
		}
	}
	mutate(m)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return false, fmt.Errorf("agentinstall: marshal %s: %w", path, err)
	}
	out = append(out, '\n')
	if raw != nil && string(out) == string(raw) {
		return false, nil // idempotent no-op
	}
	if err := backupOnce(path); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("agentinstall: mkdir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return false, fmt.Errorf("agentinstall: write %s: %w", path, err)
	}
	return true, nil
}

// upsertMarkerFile ensures path contains exactly one begin/end-fenced
// copy of inner, creating the file if needed. Unchanged content is not
// rewritten and not backed up.
func upsertMarkerFile(path, begin, end, inner string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("agentinstall: read %s: %w", path, err)
	}
	content := string(raw)
	next := upsertMarkerBlock(content, begin, end, inner)
	if next == content {
		return false, nil
	}
	if err := backupOnce(path); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("agentinstall: mkdir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return false, fmt.Errorf("agentinstall: write %s: %w", path, err)
	}
	return true, nil
}

// upsertMarkerBlock replaces the begin..end fenced region of content
// with a fresh block around inner, or appends the block (separated by a
// blank line) when no fence exists yet. Calling it twice with the same
// inner yields identical output.
func upsertMarkerBlock(content, begin, end, inner string) string {
	block := begin + "\n" + strings.TrimRight(inner, "\n") + "\n" + end + "\n"
	if i := strings.Index(content, begin); i >= 0 {
		if j := strings.Index(content[i:], end); j >= 0 {
			after := content[i+j+len(end):]
			after = strings.TrimPrefix(after, "\r\n")
			after = strings.TrimPrefix(after, "\n")
			return content[:i] + block + after
		}
	}
	if content != "" {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n"
	}
	return content + block
}
