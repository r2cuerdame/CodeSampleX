package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The repository root carries a .mcp.json, and external MCP directories read
// it as though it were the installation instructions.
//
// It is not. It configures an agent launched from a shell inside this repo,
// where PATH is inherited and a bare "csx" resolves. Its comment used to say
// the installer puts csx on PATH — which the Windows installer does and the
// macOS/Linux installer explicitly does not: install.sh prints
// "NOTE: … is not on your PATH" and leaves it to the user. A directory that
// republished that config handed every GUI-launched client a spawn error,
// which is exactly the failure `csx mcp-config` exists to prevent by
// printing the absolute path.
//
// So the file stays as it is, and this test keeps its comment from claiming
// otherwise again.

func repoRootFrom(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

type repoMCPConfig struct {
	MCPServers map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Comment string   `json:"_comment"`
	} `json:"mcpServers"`
}

func readRepoMCPConfig(t *testing.T) repoMCPConfig {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRootFrom(t), ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg repoMCPConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRepoMCPConfigDoesNotClaimTheInstallerSetsPATH(t *testing.T) {
	cfg := readRepoMCPConfig(t)
	entry, ok := cfg.MCPServers["csx"]
	if !ok {
		t.Fatal(".mcp.json has no csx server entry")
	}

	comment := strings.ToLower(entry.Comment)
	if comment == "" {
		t.Fatal(".mcp.json carries no _comment; a bare command with no warning is what directories republish")
	}
	// The claim itself, in the forms it took.
	for _, claim := range []string{
		"installer puts it on path",
		"installer puts it there",
		"the installer adds it to path",
	} {
		if strings.Contains(comment, claim) {
			t.Errorf(".mcp.json still claims %q; install.sh only prints how to add ~/.local/bin to PATH", claim)
		}
	}
	// And the pointer at the thing that is actually correct per machine.
	if !strings.Contains(entry.Comment, "csx mcp-config") {
		t.Error(".mcp.json does not point at `csx mcp-config`, which prints the absolute path a GUI client needs")
	}
}

// Whatever the command resolves to, the subcommand is the contract: the MCP
// server is `csx mcp`, and llms-install.md tells every client to write
// args ["mcp"]. A drift here breaks every hand-written client config.
func TestRepoMCPConfigInvokesTheMCPSubcommand(t *testing.T) {
	entry := readRepoMCPConfig(t).MCPServers["csx"]
	if len(entry.Args) != 1 || entry.Args[0] != "mcp" {
		t.Errorf(".mcp.json args = %v, want [mcp]", entry.Args)
	}
	var registered bool
	for _, c := range Commands() {
		if c.Name == "mcp" {
			registered = true
		}
	}
	if !registered {
		t.Error("the csx CLI no longer registers an `mcp` command")
	}
}

// llms-install.md is the document agents are pointed at, and its whole
// premise is that a GUI-launched client does not inherit a shell PATH. If
// that stops being stated, the bare-command config becomes copyable again.
func TestTheInstallGuideStillRequiresAnAbsolutePath(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRootFrom(t), "llms-install.md"))
	if err != nil {
		t.Fatal(err)
	}
	guide := string(b)
	if !strings.Contains(guide, "by absolute path") {
		t.Error("llms-install.md no longer requires the MCP client to point at csx by absolute path")
	}
	if !strings.Contains(guide, "csx mcp-config") {
		t.Error("llms-install.md no longer names `csx mcp-config`, which is where the absolute path comes from")
	}
}
