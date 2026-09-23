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

func isReadOnlyDoctor(args []string) bool {
	for i, arg := range args {
		if arg == "--debug" {
			continue
		}
		if arg != "doctor" {
			return false
		}
		for _, rest := range args[i+1:] {
			if rest == "--fix" {
				return false
			}
		}
		return true
	}
	return false
}

func main() {
	if purplepulse.IsHelperInvocation(os.Args[1:]) {
		purplepulse.RunHelperFromEnv()
		return
	}
	readOnlyDoctor := isReadOnlyDoctor(os.Args[1:])
	if home, err := config.Home(); err == nil {
		if !readOnlyDoctor {
			_, _ = identity.LoadOrCreate(home)
			http.DefaultTransport = anonymousclient.Transport{Home: home, Base: http.DefaultTransport, Surface: "cli", Version: cli.Version}

			networkAllowed := false
			if cfg, err := config.Load(home); err == nil {
				class := cfg.EffectiveClientClass()
				networkAllowed = cfg.Mode == config.ModeCommunity && (class == "ordinary" || class == "external")
			}
			purplepulse.Track(home, cli.Version, purplepulse.PlatformForArgs(os.Args[1:]), networkAllowed)
		}
	}
	os.Exit(cli.Main(os.Args[1:]))
}
