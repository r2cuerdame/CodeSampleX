package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The repository used to carry a .mcp.json at its root, and external MCP
// directories read a file like that as though it were the installation
// instructions.
//
// It was not. It configured an agent launched from a shell inside this repo,
// where PATH is inherited and a bare "csx" resolves. R2C-61 fixed its
// _comment, which told a human not to copy it and told a scraper nothing —
// so the repository still published, as machine-readable fact, the exact
// entry llms-install.md opens by measuring as broken.
//
// R2C-62 deleted the file instead. `csx init` already writes the correct
// entry, with the absolute path, into the agent config files listed in
// llms-install.md, and `csx mcp-config` prints it for every other client, so
// nothing was lost but the contradiction.
//
// The guard that replaced the ones here lives in
// internal/mcp/installconfig_test.go: a checked-in MCP config may exist, but
// the command it names has to work outside a shell. What stays in this file
// is the half of the old contract that was never about that file — the
// subcommand, and the document that explains why the absolute path matters.

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

// The subcommand is the contract: the MCP server is `csx mcp`, and
// llms-install.md tells every client to write args ["mcp"]. A drift here
// breaks every hand-written client config and every entry `csx init` wrote.
func TestTheCLIStillRegistersTheMCPSubcommand(t *testing.T) {
	for _, c := range Commands() {
		if c.Name == "mcp" {
			return
		}
	}
	t.Error("the csx CLI no longer registers an `mcp` command; every MCP client config writes args [\"mcp\"]")
}

// llms-install.md is the document agents are pointed at, and its whole
// premise is that a GUI-launched client does not inherit a shell PATH. If
// that stops being stated, a bare-command config becomes copyable again.
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
