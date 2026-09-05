// Package cli is the csx command dispatcher (contract C9). It is
// deliberately extensible: each shipped command lives in its own file and
// registers itself from init(), so the command surface can grow without
// editing a central dispatcher.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

type debugContextKey struct{}

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
	// S1 of the activation funnel (docs/activation-funnel.md §7), before argv
	// is inspected: the no-argument, help, version and unknown-command returns
	// below are real first executions, and they are the ones a stalled install
	// actually makes. Nothing in $CSX_HOME recorded that a binary had ever run
	// until this line, which is why "how many installs worked" had no answer.
	// Local-only, best effort, and never on the wire.
	stampActivation(context.Background(), localdb.StatFirstRunAt)

	debug, argv := parseGlobalDebug(argv)
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
	ctx := context.Background()
	if debug {
		ctx = context.WithValue(ctx, debugContextKey{}, true)
	}
	return cmd.Run(ctx, argv[1:])
}

func parseGlobalDebug(argv []string) (bool, []string) {
	if len(argv) > 0 && argv[0] == "--debug" {
		return true, argv[1:]
	}
	return false, argv
}

func debugEnabled(ctx context.Context) bool {
	if enabled, _ := ctx.Value(debugContextKey{}).(bool); enabled {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CSX_DEBUG"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "csx — the developer compatibility testing network")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  csx <command> [arguments]")
	fmt.Fprintln(w, "  csx --debug <command> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range Commands() {
		fmt.Fprintf(w, "  %-10s %s\n", c.Name, c.Summary)
	}
}
