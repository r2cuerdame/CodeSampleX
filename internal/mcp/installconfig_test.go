package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A checked-in MCP client config is read by more than the repository it sits
// in. Directory indexers scrape it, agents copy it, and README readers paste
// it — and the one this repository carried registered the server as
// `{"command": "csx"}`, resolved from PATH.
//
// That entry is wrong everywhere except a shell-launched agent inside this
// checkout. llms-install.md measures the failure in a clean container
// (`env -i /bin/sh -c "csx mcp"` → `csx: not found`), because an MCP client
// inherits its editor's environment rather than a login shell's, and the
// POSIX installer only PRINTS how to put ~/.local/bin on PATH. So the repo
// published, as machine-readable fact, the exact install recipe its own
// documentation spends a section telling people not to use.
//
// It was also redundant. `csx init` writes the correct entry — with the
// absolute path of the installed binary — into ~/.claude.json, and
// `csx mcp-config` prints it for every other client. The file is gone, and
// this test is what keeps a well-meaning re-add from reintroducing the
// contradiction.
//
// The rule is not "no MCP config may exist". It is that a config which does
// exist must name the binary in a way that works outside a shell: an absolute
// path, or a client-expanded placeholder such as ${__dirname} (what the MCPB
// bundle's own mcp_config uses) or ${CLAUDE_PLUGIN_ROOT}.
func TestNoCheckedInMCPConfigRegistersThisServerFromPATH(t *testing.T) {
	root := docsRepoRoot(t)
	candidates := []string{
		".mcp.json",
		"mcp.json",
		filepath.Join(".claude-plugin", ".mcp.json"),
		filepath.Join(".claude-plugin", "mcp.json"),
		filepath.Join(".cursor-plugin", "mcp.json"),
	}
	for _, rel := range candidates {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue // absent is the current answer, and a fine one
		}
		var doc struct {
			MCPServers map[string]struct {
				Command string `json:"command"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s is not valid JSON: %v", rel, err)
			continue
		}
		for name, entry := range doc.MCPServers {
			if runnableOutsideAShell(entry.Command) {
				continue
			}
			t.Errorf("%s registers %q as %q. An MCP client inherits its editor's environment, "+
				"not a login shell's, so a PATH-resolved command fails on every GUI and editor "+
				"client — see llms-install.md. Use the absolute path `csx mcp-config --path` "+
				"prints, or a client-expanded placeholder.", rel, name, entry.Command)
		}
	}
}

// runnableOutsideAShell reports whether command names the binary in a form a
// client can start without a PATH lookup.
func runnableOutsideAShell(command string) bool {
	if strings.HasPrefix(command, "${") {
		return true // ${__dirname}, ${CLAUDE_PLUGIN_ROOT}: the client expands it
	}
	if strings.HasPrefix(command, "/") || strings.HasPrefix(command, "~/") {
		return true
	}
	// Windows: C:\... or C:/...
	if len(command) >= 3 && command[1] == ':' && (command[2] == '\\' || command[2] == '/') {
		return true
	}
	return false
}

// The absolute-path rule is only useful if the README keeps pointing at the
// command that produces one. (llms-install.md, the agent-directed guide every
// directory links, is held to the same thing by
// internal/cli/mcpjsonrepo_test.go.)
func TestTheREADMEStillPointsAtMCPConfigForTheAbsolutePath(t *testing.T) {
	if !strings.Contains(readDoc(t, "README.md"), "mcp-config") {
		t.Error("README.md no longer mentions `csx mcp-config`, which is the only thing that prints " +
			"the absolute path an editor-launched client needs")
	}
}
