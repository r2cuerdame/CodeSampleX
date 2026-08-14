package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

func init() {
	Register(Command{
		Name:    "mcp-config",
		Summary: "print the MCP client configuration for this install",
		Run:     runMCPConfig,
	})
}

// runMCPConfig prints the stdio MCP server entry with the ABSOLUTE path of
// this binary.
//
// It exists because the obvious form is the broken one. The install script
// puts csx in ~/.local/bin, which is not on the default PATH, and an MCP
// client is not started from a login shell — it inherits whatever
// environment its GUI or editor had, so a bare {"command": "csx"} fails
// even after the user has fixed their own shell. `csx init` already
// registers the agents it detects by absolute path for exactly this
// reason; every other client was left to copy the bare name out of the
// README, which is the one thing that does not work.
//
// Printing it rather than documenting it also means the answer cannot go
// stale: it is read from the running binary, so it is correct wherever the
// user actually installed it.
func runMCPConfig(_ context.Context, args []string) int {
	format := "json"
	for _, a := range args {
		switch a {
		case "--toml":
			format = "toml"
		case "--path":
			format = "path"
		case "-h", "--help":
			fmt.Fprintln(os.Stdout, "usage: csx mcp-config [--toml|--path]")
			fmt.Fprintln(os.Stdout, "  (default) JSON for Cursor, Cline, Windsurf, Zed, VS Code")
			fmt.Fprintln(os.Stdout, "  --toml    TOML for Codex")
			fmt.Fprintln(os.Stdout, "  --path    just the absolute path of this binary")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "csx mcp-config: unknown option %q\n", a)
			return 2
		}
	}

	switch format {
	case "path":
		fmt.Fprintln(os.Stdout, mcpCommand())
		return 0
	case "toml":
		fmt.Fprintln(os.Stdout, codexMCPBlock())
		return 0
	}

	doc := map[string]any{
		"mcpServers": map[string]any{
			"csx": map[string]any{
				"command": mcpCommand(),
				"args":    []any{"mcp"},
			},
		},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx mcp-config: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, string(out))
	return 0
}
