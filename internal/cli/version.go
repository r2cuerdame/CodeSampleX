package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/r2cuerdame/codesamplex/internal/buildinfo"
)

// Version is the build stamp, overridable at link time:
//
//	go build -ldflags "-X github.com/r2cuerdame/codesamplex/internal/cli.Version=v1.0.0"
var Version = "dev (git)"

var (
	versionOutput io.Writer = os.Stdout
	// versionRevision is the commit the Go toolchain stamped into this
	// binary. The release builds inside a git checkout, so a shipped csx
	// carries it without any extra -X flag.
	versionRevision = func() string { return buildinfo.FromToolchain().Revision }
)

// versionReport is the `csx version --json` contract. Every key is always
// present, so a script never has to tell a missing key from an unknown value;
// changing what a key means requires a new SchemaVersion.
type versionReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"`
	// Revision is the full commit this binary was built from, empty when the
	// toolchain stamped none (go test, a build outside a git checkout).
	Revision string `json:"revision"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

func init() {
	Register(Command{
		Name:    "version",
		Summary: "print the csx version: csx version [--json]",
		Run:     versionMain,
	})
}

// versionMain implements `csx version [--json]`. The text line is an install
// contract: install.sh, install.ps1 and the updater's staged-binary self-test
// compare it byte for byte against "csx "+version, so it never changes and
// --json is the machine-readable form. Neither touches the network.
func versionMain(_ context.Context, args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "machine-readable build metadata")
	if err := fs.Parse(args); err != nil || len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: csx version [--json]")
		return 2
	}
	if !*asJSON {
		fmt.Fprintln(versionOutput, "csx "+Version)
		return 0
	}
	report := versionReport{
		SchemaVersion: 1,
		Version:       Version,
		Revision:      versionRevision(),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
	if err := json.NewEncoder(versionOutput).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
	}
	return 0
}
