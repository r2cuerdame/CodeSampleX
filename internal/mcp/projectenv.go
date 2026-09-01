package mcp

import (
	"context"
	"os"
	"sync"

	"github.com/r2cuerdame/codesamplex/adapters"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
)

// projectEnvironmentHints asks every adapter that recognises dir what it can
// say about the environment.
//
// It is deliberately only the hints -- no packages, no registry checks, no
// symbol pass. This runs to describe the machine a search is made from, not
// to inventory the project, and evidence.Scan is the wrong weight for that.
func projectEnvironmentHints(dir string) map[string]string {
	if dir == "" {
		return nil
	}
	hints := map[string]string{}
	ctx := context.Background()
	for _, a := range adapters.Detect(dir) {
		for k, v := range a.EnvironmentHints(ctx, dir) {
			if v == "" {
				continue
			}
			if _, seen := hints[k]; !seen {
				hints[k] = v // first adapter wins, as scanner.Scan does
			}
		}
	}
	if len(hints) == 0 {
		return nil
	}
	return hints
}

// machineEnvWithProject reports this host's environment, completed with what
// the project in the working directory says about itself.
//
// Deps.MachineEnv was declared and never assigned anywhere in production, so
// the server fell through to environment.Collect(ctx, nil) -- with nil hints.
// Every adapter's EnvironmentHints reached the scan path and nothing else.
// A registry ecosystem survived that, because the agent names its own runtime
// and package manager. A project whose only public coordinate comes from an
// adapter did not: an Unreal project's engine version is the one thing it can
// be asked about, and no search could carry it.
//
// Collected once. The project under an MCP server does not change while the
// server runs, and re-reading it per search would put a directory scan on the
// hot path of every question.
func machineEnvWithProject() func(context.Context) domain.EnvironmentFingerprint {
	var once sync.Once
	var env domain.EnvironmentFingerprint
	return func(ctx context.Context) domain.EnvironmentFingerprint {
		once.Do(func() {
			dir, err := os.Getwd()
			if err != nil {
				dir = ""
			}
			env = environment.Collect(ctx, projectEnvironmentHints(dir))
		})
		return env
	}
}
