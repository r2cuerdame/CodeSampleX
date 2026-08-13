package cli

import (
	"context"
	"fmt"
)

// Version is the build stamp, overridable at link time:
//
//	go build -ldflags "-X github.com/r2cuerdame/codesamplex/internal/cli.Version=v1.0.0"
var Version = "dev (git)"

func init() {
	Register(Command{
		Name:    "version",
		Summary: "print the csx version",
		Run:     versionMain,
	})
}

func versionMain(_ context.Context, _ []string) int {
	fmt.Println("csx " + Version)
	return 0
}
