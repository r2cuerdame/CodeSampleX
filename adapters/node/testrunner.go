package node

import (
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// nodeTestRunner describes the invocations of a JS test runner that exit
// without running any test: informational flags (help, version, list or show
// config) and subcommands (scaffolding, cache clearing, benchmarks).
type nodeTestRunner struct {
	nonExecFlags       map[string]bool
	nonExecSubcommands map[string]bool
	// valueOptions take a separate value argument, which must not be mistaken
	// for the first positional subcommand.
	valueOptions map[string]bool
}

var nodeTestRunners = map[string]nodeTestRunner{
	"jest": {
		nonExecFlags: map[string]bool{
			"-h": true, "--help": true, "-v": true, "--version": true,
			"--listTests": true, "--showConfig": true, "--init": true, "--clearCache": true,
		},
	},
	"vitest": {
		nonExecFlags: map[string]bool{
			"-h": true, "--help": true, "-v": true, "--version": true,
		},
		nonExecSubcommands: map[string]bool{"list": true, "init": true, "bench": true},
		valueOptions: map[string]bool{
			"-c": true, "--config": true, "-r": true, "--root": true, "--dir": true,
			"--project": true, "--mode": true, "--reporter": true, "--outputFile": true,
			"-t": true, "--testNamePattern": true, "--environment": true, "--pool": true,
			"--shard": true, "--workspace": true,
		},
	},
	"mocha": {
		nonExecFlags: map[string]bool{
			"-h": true, "--help": true, "-V": true, "--version": true,
			"--list-reporters": true, "--list-interfaces": true,
		},
		nonExecSubcommands: map[string]bool{"init": true},
		valueOptions: map[string]bool{
			"-R": true, "--reporter": true, "-O": true, "--reporter-option": true,
			"-u": true, "--ui": true, "-g": true, "--grep": true, "-f": true, "--fgrep": true,
			"-r": true, "--require": true, "-t": true, "--timeout": true, "-s": true, "--slow": true,
			"--config": true, "--package": true, "--spec": true, "--extension": true,
			"--ignore": true, "-j": true, "--jobs": true, "--file": true,
		},
	},
	// AVA's -v means --verbose, not --version, so it is not listed.
	"ava": {
		nonExecFlags:       map[string]bool{"-h": true, "--help": true, "--version": true},
		nonExecSubcommands: map[string]bool{"reset-cache": true},
		valueOptions: map[string]bool{
			"-m": true, "--match": true, "-c": true, "--concurrency": true,
			"-T": true, "--timeout": true, "--config": true, "--node-arguments": true,
		},
	},
}

// classifyNodeTestRunner reports jest/vitest/mocha/ava as a project test run
// unless the invocation only prints information or performs a non-test
// action, in which case it stays unclassified (Known=false).
func classifyNodeTestRunner(tool string, args []string) scanner.CommandProfile {
	runner := nodeTestRunners[tool]
	firstPositional := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			// Arguments after the separator are passed through, not parsed
			// as runner flags or subcommands.
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if firstPositional == "" {
				firstPositional = arg
			}
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if runner.nonExecFlags[name] && (!hasValue || !strings.EqualFold(value, "false")) {
			return scanner.CommandProfile{Tool: tool}
		}
		if !hasValue && runner.valueOptions[name] && i+1 < len(args) {
			i++
		}
	}
	if runner.nonExecSubcommands[firstPositional] {
		return scanner.CommandProfile{Tool: tool}
	}
	return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
}
