package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
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
	in, out := os.Stdin, os.Stdout
	os.Stdout = os.Stderr
	if devNull, derr := os.Open(os.DevNull); derr == nil {
		defer devNull.Close()
		os.Stdin = devNull
	}
	defer func() { os.Stdin, os.Stdout = in, out }()
	return serveMCP(ctx, home, in, out, os.Stderr, mcp.NewDeps, daemon.EnsureRunning)
}

type mcpDepsFactory func(string) (*mcp.Deps, func() error, error)
type ensureMCPDaemon func(context.Context, string, ...string) (*daemon.Client, error)

// serveMCP contains the startup ordering shared by the command and its
// end-to-end regression. In/out remain the MCP transport; background daemon
// startup is deliberately launched before Serve but never awaited by it.
func serveMCP(ctx context.Context, home string, in io.Reader, out, errOut io.Writer,
	newDeps mcpDepsFactory, ensureRunning ensureMCPDaemon) int {
	deps, closeDB, err := newDeps(home)
	if err != nil {
		fmt.Fprintf(errOut, "csx mcp: %v\n", err)
		return 1
	}
	defer closeDB() //nolint:errcheck // process is exiting

	// Agent integrations launch `csx mcp` directly. Until this point that
	// path never started the daemon, so evidence and wanted/adoption reports
	// accumulated forever unless somebody happened to type `csx sync`.
	// Start it asynchronously: MCP protocol startup stays immediate and the
	// detached daemon owns bounded first drains plus retries/offline handling.
	if deps.Mode != nil && deps.Mode() == config.ModeCommunity {
		go func() {
			dctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := ensureRunning(dctx, home, Version); err != nil {
				fmt.Fprintf(errOut, "csx mcp: background sync unavailable: %v\n", err)
			}
		}()
	}

	srv := &mcp.Server{In: in, Out: out, Deps: deps, Version: Version}
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintf(errOut, "csx mcp: %v\n", err)
		return 1
	}
	return 0
}
