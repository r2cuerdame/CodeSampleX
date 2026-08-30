package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// The bare name is the one form that does not work. An MCP client is not
// started from a login shell, and the install script puts csx in
// ~/.local/bin, so {"command": "csx"} fails for a fresh install even after
// the user fixes their own PATH. Whatever this command prints must be
// absolute.
func TestMCPConfigPrintsAnAbsolutePath(t *testing.T) {
	isolateHome(t)
	out, code := captureStdout(t, func() int { return Main([]string{"mcp-config"}) })
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out)
	}

	var doc struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	csx, ok := doc.MCPServers["csx"]
	if !ok {
		t.Fatalf("no csx entry: %s", out)
	}
	if csx.Command == "csx" {
		t.Error("printed the bare name, which is the form that fails")
	}
	if !filepath.IsAbs(csx.Command) {
		t.Errorf("command %q is not absolute", csx.Command)
	}
	if len(csx.Args) != 1 || csx.Args[0] != "mcp" {
		t.Errorf("args = %v, want [mcp]", csx.Args)
	}
}

func TestMCPConfigPathAndTOMLForms(t *testing.T) {
	isolateHome(t)
	out, code := captureStdout(t, func() int { return Main([]string{"mcp-config", "--path"}) })
	if code != 0 {
		t.Fatalf("--path exit = %d\n%s", code, out)
	}
	if path := strings.TrimSpace(out); !filepath.IsAbs(path) {
		t.Errorf("--path printed %q, want an absolute path", path)
	}

	out, code = captureStdout(t, func() int { return Main([]string{"mcp-config", "--toml"}) })
	if code != 0 {
		t.Fatalf("--toml exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "csx") {
		t.Errorf("--toml printed nothing recognisable:\n%s", out)
	}
	if strings.Contains(out, `command = "csx"`) {
		t.Error("--toml printed the bare name")
	}
}

func TestMCPConfigRejectsUnknownOption(t *testing.T) {
	isolateHome(t)
	if _, code := captureStdout(t, func() int {
		return Main([]string{"mcp-config", "--nope"})
	}); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}
