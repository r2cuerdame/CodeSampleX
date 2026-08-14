package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"codesamplex.dev/sample/gocobra/src"
)

func main() {
	// A subcommand runs, and everything it prints through cmd.OutOrStdout()
	// lands in the root's buffer. Nothing reaches the real stdout, so the
	// assertion is on a string and not on a captured file descriptor.
	t := cli.New()
	err := t.Run([]string{"greet", "--name", "ada"})
	check(err == nil, "greet: %v", err)
	check(t.Out.String() == "hello ada (config=csx.yaml)\n", "out=%q", t.Out.String())
	check(t.Err.Len() == 0, "err=%q", t.Err.String())

	// RunE's error comes back out of Execute wrapped exactly as RunE returned
	// it, so errors.Is reaches the sentinel. This is the whole reason to use
	// RunE over Run: Run has nowhere to put a failure except os.Exit.
	t = cli.New()
	err = t.Run([]string{"greet", "--name", "boom"})
	check(errors.Is(err, cli.ErrGreetRefused), "expected ErrGreetRefused, got %v", err)
	check(err.Error() == `greet "boom": greeting refused`, "message=%q", err.Error())
	// Execute both returns the error AND prints it. The error line goes to
	// SetErr, the usage dump goes to SetOut — cobra prints usage with
	// Command.Println, whose destination is OutOrStderr, so installing an out
	// writer moves usage off stderr with it. A test that reads only one
	// buffer sees half of what the user sees.
	check(strings.Contains(t.Err.String(), `Error: greet "boom": greeting refused`), "err=%q", t.Err.String())
	check(strings.Contains(t.Out.String(), "Usage:"), "out=%q", t.Out.String())

	// The trap. SetArgs replaces os.Args[1:], so the program name must not be
	// in it. Include it and it becomes the first positional of the root, which
	// a root with subcommands rejects as an unknown command.
	t = cli.New()
	err = t.Run([]string{"csxtool", "greet", "--name", "ada"})
	check(err != nil, "program name in SetArgs should fail")
	check(strings.HasPrefix(err.Error(), `unknown command "csxtool" for "csxtool"`),
		"expected the program name to be read as a command, got %v", err)

	// Where the fallback bites: with no SetArgs at all, cobra parses the real
	// os.Args[1:]. Under `go test` those are the -test.* flags, which is why
	// an untouched command tree fails inside a test with a flag error nobody
	// wrote.
	saved := os.Args
	os.Args = []string{"csxtool", "--nope"}
	err = cli.New().Root.Execute()
	check(err != nil && strings.Contains(err.Error(), "unknown flag: --nope"),
		"expected the os.Args fallback to be parsed, got %v", err)
	// The guard cobra uses is `c.args == nil`, so SetArgs(nil) does not mean
	// "no arguments" — it reopens the fallback. A variadic test helper called
	// with no arguments hands cobra exactly that nil, which is why Run takes a
	// slice. An empty but non-nil slice is how you say "no arguments", and it
	// runs the root with none.
	nilArgs := cli.New()
	err = nilArgs.Run(nil)
	check(err != nil && strings.Contains(err.Error(), "unknown flag: --nope"),
		"SetArgs(nil) should fall back to os.Args as well, got %v", err)
	empty := cli.New()
	err = empty.Run([]string{})
	check(err == nil && strings.Contains(empty.Out.String(), "args=[]"),
		"an empty non-nil slice means no arguments: %v / %q", err, empty.Out.String())
	os.Args = saved

	// cobra carries a workaround for exactly that: it skips the fallback when
	// the program name is "cobra.test". It keys on the name, so it saves the
	// package's own tests and nobody else's.
	saved = os.Args
	os.Args = []string{"cobra.test", "-test.v"}
	tf := cli.New()
	err = tf.Root.Execute()
	os.Args = saved
	check(err == nil, "expected the cobra.test escape hatch to skip the fallback, got %v", err)
	check(strings.Contains(tf.Out.String(), "args=[]"), "out=%q", tf.Out.String())

	// A missing required flag is an ordinary returned error with a fixed
	// message, and it arrives as a return value: nothing panics and no process
	// exits. With one name missing the quoting looks unremarkable; the
	// persistent-flag block below runs the two-name case, which is where the
	// message turns out to be shaped oddly.
	t = cli.New()
	err = t.Run([]string{"greet"})
	check(err != nil && err.Error() == `required flag(s) "name" not set`,
		"required flag message=%v", err)
	check(strings.Contains(t.Err.String(), `Error: required flag(s) "name" not set`), "err=%q", t.Err.String())

	// A parent's persistent flag reaches the child; a parent's local flag does
	// not. Before anything parses, the child's own Flags() does not contain
	// the inherited flag either — the merge happens in ParseFlags, so an init()
	// that looks up a parent flag on a child finds nil.
	t = cli.New()
	check(t.Greet.Flags().Lookup("config") == nil, "config is not merged into the child before parsing")
	err = t.Run([]string{"greet", "--config", "other.yaml", "--name", "ada"})
	check(err == nil, "inherited flag: %v", err)
	check(t.Out.String() == "hello ada (config=other.yaml)\n", "out=%q", t.Out.String())
	check(t.Greet.Flags().Lookup("config") != nil, "after parsing, Flags() carries the inherited flag")
	check(t.Greet.InheritedFlags().Lookup("config") != nil, "config should be inherited")
	check(t.Greet.LocalFlags().Lookup("config") == nil, "config is not local to the child")
	check(t.Greet.LocalFlags().Lookup("name") != nil, "name is local to the child")

	// The same flag declared with Flags() on the root is invisible one level
	// down.
	t = cli.New()
	err = t.Run([]string{"greet", "--local-only", "--name", "ada"})
	check(err != nil && err.Error() == "unknown flag: --local-only",
		"a root-local flag must not reach the child, got %v", err)
	// Moving it in front of the subcommand does not help. cobra finds the
	// subcommand by removing only the subcommand word, so every flag on the
	// line — before or after — is parsed by the command that was found.
	t = cli.New()
	err = t.Run([]string{"--local-only", "greet", "--name", "ada"})
	check(err != nil && err.Error() == "unknown flag: --local-only",
		"position does not change which command parses a flag, got %v", err)
	// It works on the root itself, which is what makes the failure confusing.
	t = cli.New()
	err = t.Run([]string{"--local-only"})
	check(err == nil && strings.Contains(t.Out.String(), "local-only=true"),
		"root should accept its own flag: %v / %q", err, t.Out.String())

	// The split is visible in the child's own help: an inherited flag is
	// listed under "Global Flags", a parent's local flag is not listed at all.
	t = cli.New()
	err = t.Run([]string{"greet", "--help"})
	check(err == nil, "%v", err)
	check(strings.Contains(t.Out.String(), "Global Flags:") && strings.Contains(t.Out.String(), "--config"),
		"expected the inherited flag under Global Flags, out=%q", t.Out.String())
	check(!strings.Contains(t.Out.String(), "local-only"), "out=%q", t.Out.String())

	// MarkFlagRequired annotates the command's own FlagSet, so marking a
	// parent's persistent flag from the child fails outright — at declaration
	// time that flag is not in the child's set yet. The error is pflag's, and
	// it prints the long name with a single dash.
	t = cli.New()
	err = t.Greet.MarkFlagRequired("config")
	check(err != nil && err.Error() == "no such flag -config",
		"marking a parent flag from the child should fail, got %v", err)
	// MarkPersistentFlagRequired on the command that owns the flag is the one
	// that works, and it makes the flag required for every subcommand that
	// inherits it. A default value does not satisfy it: the check is on
	// pflag's Changed, so only an explicit --config counts.
	t = cli.New()
	check(t.Root.MarkPersistentFlagRequired("config") == nil, "marking on the owner should succeed")
	err = t.Run([]string{"greet", "--name", "ada"})
	check(err != nil && err.Error() == `required flag(s) "config" not set`,
		"a required persistent flag with a default is still required, got %v", err)
	t = cli.New()
	check(t.Root.MarkPersistentFlagRequired("config") == nil, "marking on the owner should succeed")
	check(t.Run([]string{"greet", "--name", "ada", "--config", "other.yaml"}) == nil, "passing it explicitly satisfies it")
	// Two missing at once is one error, not two, and the quoting is the trap
	// for anyone matching on the message: the names are joined with `", "`
	// INSIDE a single pair of quotes, so the string contains one quoted run
	// with a comma in it rather than two quoted names. The order is pflag's
	// sorted VisitAll, not declaration order — config was declared after name.
	t = cli.New()
	check(t.Root.MarkPersistentFlagRequired("config") == nil, "marking on the owner should succeed")
	err = t.Run([]string{"greet"})
	check(err != nil && err.Error() == `required flag(s) "config", "name" not set`,
		"two missing flags share one pair of quotes, got %v", err)

	// An unknown subcommand and an unknown flag are different errors from
	// different stages, and they print differently. The unknown command is
	// found before any command runs: the message carries a suggestion, and
	// what gets printed is a one-line pointer, not the usage block.
	t = cli.New()
	err = t.Run([]string{"gret"})
	check(err != nil && strings.HasPrefix(err.Error(), `unknown command "gret" for "csxtool"`),
		"expected unknown command, got %v", err)
	check(strings.Contains(err.Error(), "Did you mean this?") && strings.Contains(err.Error(), "greet"),
		"expected a suggestion in %q", err.Error())
	check(strings.Contains(t.Err.String(), "Run 'csxtool --help' for usage."), "err=%q", t.Err.String())
	check(!strings.Contains(t.Out.String(), "Usage:") && !strings.Contains(t.Err.String(), "Usage:"),
		"an unknown command prints no usage block: out=%q err=%q", t.Out.String(), t.Err.String())

	// The unknown flag is found later, inside the subcommand's own flag
	// parsing. No suggestion, no pointer line, and the full usage of the
	// subcommand that failed.
	t = cli.New()
	err = t.Run([]string{"greet", "--name", "ada", "--nope"})
	check(err != nil && err.Error() == "unknown flag: --nope", "expected unknown flag, got %v", err)
	check(strings.Contains(t.Err.String(), "Error: unknown flag: --nope"), "err=%q", t.Err.String())
	check(!strings.Contains(t.Err.String(), "Run 'csxtool"), "err=%q", t.Err.String())
	check(strings.Contains(t.Out.String(), "Usage:") && strings.Contains(t.Out.String(), "csxtool greet"),
		"expected the subcommand's usage, out=%q", t.Out.String())

	// The suggestion above is not a feature of cobra so much as a side effect
	// of leaving Args nil on the root: Find runs the legacy unknown-command
	// check only when the command it lands on has Args == nil. Give the root
	// ArbitraryArgs and that check is skipped, and a typo silently becomes a
	// positional argument to the root.
	ta := cli.NewArbitraryArgs()
	err = ta.Run([]string{"gret"})
	check(err == nil, "ArbitraryArgs on the root should swallow the typo, got %v", err)
	check(strings.Contains(ta.Out.String(), "args=[gret]"), "out=%q", ta.Out.String())
	// Real subcommands still route, which is what makes the change easy to
	// ship without noticing.
	ta = cli.NewArbitraryArgs()
	check(ta.Run([]string{"greet", "--name", "ada"}) == nil, "a validator on the root must not break routing")
	check(ta.Out.String() == "hello ada (config=csx.yaml)\n", "out=%q", ta.Out.String())
	// Measured, against the obvious reading of "setting Args disables the
	// check": what a validator disables is legacyArgs, not the error. NoArgs
	// rejects the same typo with the same wording, but it is raised later, by
	// ValidateArgs inside the command that was found, so it carries no
	// suggestion and prints like a run failure — the "Error:" line plus the
	// root's whole usage block, instead of the one-line pointer above.
	t = cli.New()
	t.Root.Args = cobra.NoArgs
	err = t.Run([]string{"gret"})
	check(err != nil && err.Error() == `unknown command "gret" for "csxtool"`,
		"NoArgs should still reject the typo, got %v", err)
	check(!strings.Contains(err.Error(), "Did you mean"), "NoArgs has no suggestion, got %q", err.Error())
	check(!strings.Contains(t.Err.String(), "Run 'csxtool"), "err=%q", t.Err.String())
	check(strings.Contains(t.Out.String(), "Available Commands:"), "out=%q", t.Out.String())

	// SilenceUsage changes only what is printed. The error returned is the
	// same value, and setting it on the root covers the subcommands.
	t = cli.New()
	t.Root.SilenceUsage = true
	err = t.Run([]string{"greet", "--name", "boom"})
	check(errors.Is(err, cli.ErrGreetRefused), "SilenceUsage must not change the error: %v", err)
	check(!strings.Contains(t.Out.String(), "Usage:"), "out=%q", t.Out.String())
	check(strings.Contains(t.Err.String(), "Error: "), "err=%q", t.Err.String())
	// Setting it on the child alone works too: cobra checks the failing
	// command and the root, so either one silences.
	t = cli.New()
	t.Greet.SilenceUsage = true
	err = t.Run([]string{"greet", "--name", "boom"})
	check(errors.Is(err, cli.ErrGreetRefused), "%v", err)
	check(!strings.Contains(t.Out.String(), "Usage:"), "out=%q", t.Out.String())
	// The cost of the root-level flag: it also silences usage for a real usage
	// error, where the usage block is the useful part.
	t = cli.New()
	t.Root.SilenceUsage = true
	err = t.Run([]string{"greet", "--nope"})
	check(err != nil && err.Error() == "unknown flag: --nope", "%v", err)
	check(!strings.Contains(t.Out.String(), "Usage:"), "out=%q", t.Out.String())
	// Which is why the field is usually set inside RunE instead: cobra reads
	// SilenceUsage after execute() returns, so an assignment made while the
	// command runs still counts.
	silenceLate := func(cmd *cobra.Command, _ []string) error {
		cmd.SilenceUsage = true
		return errors.New("failed after parsing")
	}
	t = cli.New()
	t.Greet.RunE = silenceLate
	err = t.Run([]string{"greet", "--name", "ada"})
	check(err != nil && t.Out.Len() == 0, "late SilenceUsage should drop usage: %v out=%q", err, t.Out.String())
	check(strings.Contains(t.Err.String(), "Error: failed after parsing"), "err=%q", t.Err.String())
	// And it is exactly as narrow as the mechanism implies: the same tree
	// still prints usage for a flag error, because RunE never ran to set the
	// field. That is the point of doing it there rather than on the root.
	t = cli.New()
	t.Greet.RunE = silenceLate
	err = t.Run([]string{"greet", "--nope"})
	check(err != nil && err.Error() == "unknown flag: --nope", "%v", err)
	check(strings.Contains(t.Out.String(), "Usage:"),
		"a flag error never reaches RunE, so usage survives: out=%q", t.Out.String())

	// SilenceErrors drops the "Error:" line and nothing else, so on its own it
	// prints a usage block with no explanation of what went wrong.
	t = cli.New()
	t.Root.SilenceErrors = true
	err = t.Run([]string{"greet", "--name", "boom"})
	check(errors.Is(err, cli.ErrGreetRefused), "SilenceErrors must not change the error: %v", err)
	check(t.Err.Len() == 0, "err=%q", t.Err.String())
	check(strings.Contains(t.Out.String(), "Usage:"), "out=%q", t.Out.String())

	// Both together: Execute prints nothing at all and the error is still
	// returned, which is the configuration for a program that wants to format
	// its own failures in main.
	t = cli.New()
	t.Root.SilenceErrors = true
	t.Root.SilenceUsage = true
	err = t.Run([]string{"greet", "--name", "boom"})
	check(errors.Is(err, cli.ErrGreetRefused), "%v", err)
	check(t.Out.Len() == 0 && t.Err.Len() == 0, "out=%q err=%q", t.Out.String(), t.Err.String())

	// --help is not an error. cobra intercepts flag.ErrHelp, prints help to
	// out and returns nil from Execute. The Rust counterpart does the
	// opposite: clap reports help as Err with kind DisplayHelp, so code
	// ported in either direction gets the success/failure of --help backwards.
	t = cli.New()
	err = t.Run([]string{"--help"})
	check(err == nil, "--help should not be an error, got %v", err)
	check(strings.Contains(t.Out.String(), "Available Commands:") && strings.Contains(t.Out.String(), "greet"),
		"out=%q", t.Out.String())
	check(t.Err.Len() == 0, "err=%q", t.Err.String())
	t = cli.New()
	err = t.Run([]string{"greet", "--help"})
	check(err == nil, "%v", err)
	check(strings.Contains(t.Out.String(), "--name string"), "out=%q", t.Out.String())

	// State survives Execute. The second run passes no --name, and the
	// required-flag check passes anyway, because pflag still has the first
	// run's value with Changed set. A table test that reuses one tree reports
	// a pass for a case that would fail from a shell.
	reused := cli.New()
	check(reused.Run([]string{"greet", "--name", "ada"}) == nil, "first run should pass")
	err = reused.Run([]string{"greet"})
	check(err == nil, "measured: the second run does NOT re-check the required flag, got %v", err)
	check(reused.Out.String() == "hello ada (config=csx.yaml)\n",
		"the first run's value is still there: %q", reused.Out.String())
	check(reused.Greet.Flags().Lookup("name").Changed, "Changed stays set across Execute calls")
	// A fresh tree, same arguments, is the failure the reused tree hid.
	check(cli.New().Run([]string{"greet"}) != nil, "a fresh tree must reject the missing flag")

	fmt.Println("contract ok")
}

func check(ok bool, format string, args ...any) {
	if !ok {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}
}
