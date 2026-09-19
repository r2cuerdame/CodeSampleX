package domain

import (
	"regexp"
	"strings"
)

// A CLI coordinate the farm is asked to fill has one axis more than a
// package coordinate: the operating system the command ran on. The authoring
// queue keys its assignments and its attempt ledger by (ecosystem, name,
// version, symbol) and nothing else, so for CLI work the OS travels inside
// the symbol. This file is the one place that encoding is written and read.
//
// The form is "[<os>] <command>", or "[<os>]" alone for the tool-level
// probe that establishes which version of a tool the farm actually has on
// that OS. It is deliberately unlike the "farm:"/"field:" provenance prefix
// that evidence symbols carry (EncodeCLISymbol): a work symbol names what to
// run, an evidence symbol records what ran, and neither parses as the other.

var cliWorkSymbolRe = regexp.MustCompile(`^\[([a-z]+)\](?: (.+))?$`)

// EncodeCLIWorkSymbol renders the OS and the command pattern of one CLI work
// coordinate as the queue's symbol. The command is whitespace-normalized so
// the same command spelled twice lands on one assignment.
func EncodeCLIWorkSymbol(os, command string) string {
	os = strings.ToLower(strings.TrimSpace(os))
	command = strings.Join(strings.Fields(command), " ")
	if command == "" {
		return "[" + os + "]"
	}
	return "[" + os + "] " + command
}

// DecodeCLIWorkSymbol reads a symbol written by EncodeCLIWorkSymbol. ok is
// false for every other symbol, including evidence symbols and package
// symbols, so a caller can tell CLI work apart by its symbol alone.
func DecodeCLIWorkSymbol(symbol string) (os, command string, ok bool) {
	m := cliWorkSymbolRe.FindStringSubmatch(symbol)
	if m == nil {
		return "", "", false
	}
	command = strings.Join(strings.Fields(m[2]), " ")
	if m[2] != "" && command != m[2] {
		return "", "", false
	}
	return m[1], command, true
}

// CLIWorkHasPlaceholders reports whether a command pattern still carries a
// sanitized placeholder such as <path> or <arg>, which a worker has to
// replace with a concrete value before the command can run.
func CLIWorkHasPlaceholders(command string) bool {
	return strings.Contains(command, "<") && strings.Contains(command, ">")
}

// CLIToolFromTargetName maps a public generic target name ("cli/git") back
// to the tool it names. It is false for every name outside the fixed CLI
// vocabulary, so an arbitrary "cli/<anything>" cannot become farm work.
func CLIToolFromTargetName(name string) (string, bool) {
	tool, found := strings.CutPrefix(name, "cli/")
	if !found || tool == "" || !IsRecognizedCLITool(tool) || wantedTargetNames[tool] != name {
		return "", false
	}
	return tool, true
}

// CLISeedTools is the order the farm fills CLI coverage in before any other
// tool: the tools the network's own agents run most, so the first evidence
// answers the questions most often asked. The position is the priority.
var CLISeedTools = []string{"gh", "git", "docker", "powershell", "bash", "go", "npm", "pnpm", "cargo"}

// CLISeedRank returns a seed tool's position in CLISeedTools, lower first.
func CLISeedRank(tool string) (int, bool) {
	for i, seed := range CLISeedTools {
		if seed == tool {
			return i, true
		}
	}
	return 0, false
}
