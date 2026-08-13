// Package cli is the csx command dispatcher (contract C9). It is
// deliberately extensible: each command lives in its own file and
// registers itself from init(), so later waves add commands without
// editing any shared file.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
)

// Command is one csx subcommand.
type Command struct {
	Name    string
	Summary string
	Run     func(ctx context.Context, args []string) int
}

var (
	mu       sync.Mutex
	commands = map[string]Command{}
)

// Register adds a command to the dispatcher. Commands call it from
// init(); duplicate or incomplete registrations are programmer errors.
func Register(cmd Command) {
	if cmd.Name == "" || cmd.Run == nil {
		panic("cli: Register requires Name and Run")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := commands[cmd.Name]; dup {
		panic("cli: duplicate command " + cmd.Name)
	}
	commands[cmd.Name] = cmd
}

// Commands returns every registered command sorted by name.
func Commands() []Command {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Command, 0, len(commands))
	for _, c := range commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Main dispatches argv to the matching command and returns the process
// exit code. No arguments or an explicit help request prints usage.
func Main(argv []string) int {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		usage(os.Stdout)
		return 0
	}
	mu.Lock()
	cmd, ok := commands[argv[0]]
	mu.Unlock()
	if !ok {
		fmt.Fprintf(os.Stderr, "csx: unknown command %q\n\n", argv[0])
		usage(os.Stderr)
		return 2
	}
	return cmd.Run(context.Background(), argv[1:])
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "csx — the community-verified code sample network")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  csx <command> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range Commands() {
		fmt.Fprintf(w, "  %-10s %s\n", c.Name, c.Summary)
	}
}
