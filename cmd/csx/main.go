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
	"github.com/r2cuerdame/codesamplex/internal/purplepulse"
)

func main() {
	if purplepulse.IsHelperInvocation(os.Args[1:]) {
		purplepulse.RunHelperFromEnv()
		return
	}
	if home, err := config.Home(); err == nil {
		_, _ = identity.LoadOrCreate(home)
		http.DefaultTransport = anonymousclient.Transport{Home: home, Base: http.DefaultTransport}

		networkAllowed := false
		if cfg, err := config.Load(home); err == nil {
			class := cfg.EffectiveClientClass()
			networkAllowed = cfg.Mode == config.ModeCommunity && (class == "ordinary" || class == "external")
		}
		purplepulse.Track(home, cli.Version, purplepulse.PlatformForArgs(os.Args[1:]), networkAllowed)
	}
	os.Exit(cli.Main(os.Args[1:]))
}
