// Command csx is the CodeSampleX local client: CLI dispatcher, evidence wrapper,
// daemon controller, MCP server, sample workflow, updater, and verifier worker.
package main

import (
	"os"

	"github.com/r2cuerdame/codesamplex/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
