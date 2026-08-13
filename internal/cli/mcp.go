package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/mcp"
)

func init() {
	Register(Command{
		Name:    "mcp",
		Summary: "run the MCP stdio server (invoked by agent configs)",
		Run:     mcpMain,
	})
}

// mcpMain implements `csx mcp`: the MCP stdio server agent configs invoke
// (contract C8). stdin/stdout belong to the JSON-RPC protocol, so before
// serving, os.Stdin/os.Stdout are re-pointed away: wrapped commands from
// run_observed_command (and any stray print) inherit them and must never
// consume protocol input or corrupt protocol output.
func mcpMain(ctx context.Context, _ []string) int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx mcp: %v\n", err)
		return 1
	}
	deps, closeDB, err := mcp.NewDeps(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx mcp: %v\n", err)
		return 1
	}
	defer closeDB() //nolint:errcheck // process is exiting

	in, out := os.Stdin, os.Stdout
	os.Stdout = os.Stderr
	if devNull, derr := os.Open(os.DevNull); derr == nil {
		defer devNull.Close()
		os.Stdin = devNull
	}
	defer func() { os.Stdin, os.Stdout = in, out }()

	srv := &mcp.Server{In: in, Out: out, Deps: deps, Version: Version}
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "csx mcp: %v\n", err)
		return 1
	}
	return 0
}
