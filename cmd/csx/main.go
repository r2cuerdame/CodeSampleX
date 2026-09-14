// Command csx is the CodeSampleX local client: CLI dispatcher, evidence wrapper,
// daemon controller, MCP server, sample workflow, updater, and verifier worker.
package main

import (
	"net/http"
	"os"

	"github.com/r2cuerdame/codesamplex/internal/anonymousclient"
	"github.com/r2cuerdame/codesamplex/internal/cli"
	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/identity"
)

func main() {
	if home, err := config.Home(); err == nil {
		// First execution needs no signup or network; subsequent processes use
		// the same race-safe persisted identity, including CLI, MCP and daemon.
		_, _ = identity.LoadOrCreate(home)
		http.DefaultTransport = anonymousclient.Transport{Home: home, Base: http.DefaultTransport}
	}
	os.Exit(cli.Main(os.Args[1:]))
}
