package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/r2cuerdame/codesamplex/internal/doctor"
)

func init() {
	Register(Command{
		Name:    "doctor",
		Summary: "run environment, payload, storage, and agent diagnostics: csx doctor [--fix] [--verbose] [--json]",
		Run:     doctorMain,
	})
}

var doctorContextFactory = doctor.DefaultCheckContext

// doctorMain implements the `csx doctor` command.
func doctorMain(ctx context.Context, args []string) int {
	var fix, verbose, jsonOut bool

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--fix":
			fix = true
		case "--verbose":
			verbose = true
		case "--json":
			jsonOut = true
		case "-h", "--help":
			fmt.Fprintln(os.Stdout, "usage: csx doctor [--fix] [--verbose] [--json]")
			fmt.Fprintln(os.Stdout)
			fmt.Fprintln(os.Stdout, "Run comprehensive operational diagnostics and safe self-healing.")
			fmt.Fprintln(os.Stdout)
			fmt.Fprintln(os.Stdout, "Flags:")
			fmt.Fprintln(os.Stdout, "  --fix      automatically repair detected fixable issues")
			fmt.Fprintln(os.Stdout, "  --verbose  show detailed context and remediation steps")
			fmt.Fprintln(os.Stdout, "  --json     output results as sanitized JSON")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "csx doctor: unknown option %q\n", a)
			fmt.Fprintln(os.Stderr, "usage: csx doctor [--fix] [--verbose] [--json]")
			return 2
		}
	}

	cctx, err := doctorContextFactory(fix, verbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx doctor: initialize context: %v\n", err)
		return 1
	}

	engine := doctor.DefaultEngine()
	res := engine.Run(ctx, cctx)

	if jsonOut {
		if err := doctor.FormatJSON(os.Stdout, res); err != nil {
			fmt.Fprintf(os.Stderr, "csx doctor: format JSON: %v\n", err)
			return 1
		}
	} else {
		if err := doctor.FormatTable(os.Stdout, res, verbose); err != nil {
			fmt.Fprintf(os.Stderr, "csx doctor: format output: %v\n", err)
			return 1
		}
	}

	if res.Healthy {
		return 0
	}
	return 1
}
