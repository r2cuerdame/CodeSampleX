// Command csx is the CodeSampleX local client: evidence-recording command
// wrapper today; daemon, MCP server, and sample tooling in later waves.
package main

import (
	"os"

	"github.com/r2cuerdame/codesamplex/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
