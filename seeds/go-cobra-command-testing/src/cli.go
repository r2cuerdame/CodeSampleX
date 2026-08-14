// Package cli builds a cobra command tree and drives it entirely in memory,
// which is how you test a cobra CLI without spawning a process.
//
// The trap that costs the most time is SetArgs. Execute() falls back to
// os.Args[1:] when nothing has been set, so the program name is already gone
// by the time cobra looks at anything. SetArgs replaces that slice, not
// os.Args, so it takes the arguments WITHOUT the program name. clap's
// try_parse_from, the Rust equivalent of this same technique, takes the whole
// argv including the program name and skips element 0 itself; copying that
// shape here makes the program name the first positional, and cobra answers
// `unknown command "csxtool" for "csxtool"`, which reads like nonsense until
// you know where the extra word came from.
//
// The second trap is that a *cobra.Command is not a value. It keeps the flag
// values from the last Execute, and pflag keeps Changed set, so a second
// Execute on the same tree is not an independent run: a required flag that
// was never passed the second time still validates. Every case in the
// contract builds a fresh tree, and the last one measures what happens if you
// do not.
//
// The third is output. Anything printed with fmt.Println goes to the real
// stdout and cannot be asserted on. cmd.OutOrStdout() and cmd.ErrOrStderr()
// go to whatever SetOut/SetErr installed, and a command with no writer of its
// own walks up to its parent, so setting the two writers on the root is
// enough for the whole tree.
package cli

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// ErrGreetRefused is returned wrapped by greet's RunE. Execute returns that
// error unchanged, so errors.Is still reaches the sentinel through the wrap:
// the exit path of a cobra program is an ordinary Go error, not a status code
// you have to recover from a process.
var ErrGreetRefused = errors.New("greeting refused")

// Tree is one command tree plus the buffers it writes to. Greet is exported
// because the contract inspects its FlagSets, which is where the difference
// between a local, an inherited and a persistent flag becomes visible.
type Tree struct {
	Root  *cobra.Command
	Greet *cobra.Command
	Out   *bytes.Buffer
	Err   *bytes.Buffer
}

// New builds the default tree: root.Args is left nil, which is what enables
// cobra's unknown-command check.
func New() *Tree { return build(nil) }

// NewArbitraryArgs is the same tree with a positional-args validator on the
// root. It exists to measure the cost of that one line.
func NewArbitraryArgs() *Tree { return build(cobra.ArbitraryArgs) }

func build(rootArgs cobra.PositionalArgs) *Tree {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}

	root := &cobra.Command{
		Use:   "csxtool",
		Short: "A tree that exists to be driven from a test",
		Args:  rootArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			localOnly, err := cmd.Flags().GetBool("local-only")
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "root local-only=%v args=%v\n", localOnly, args)
			return nil
		},
	}
	// One SetOut/SetErr pair on the root covers every child, and it also
	// redirects what cobra itself prints on failure. Which buffer each half
	// lands in is asserted in the contract, because the two do not match the
	// obvious guess.
	root.SetOut(out)
	root.SetErr(errOut)

	// PersistentFlags is the only way a child sees a parent's flag.
	root.PersistentFlags().String("config", "csx.yaml", "config file, inherited by every subcommand")
	// Flags() on a command that has subcommands is a local flag: usable when
	// the root itself runs, invisible to every child.
	root.Flags().Bool("local-only", false, "belongs to the root alone")

	greet := &cobra.Command{
		Use:   "greet",
		Short: "Greet somebody",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := cmd.Flags().GetString("name")
			if err != nil {
				return err
			}
			// The inherited flag is read from the child's own Flags(), not
			// from InheritedFlags(): ParseFlags merges the parents' persistent
			// sets into Flags() before RunE is reached. Before that merge the
			// same lookup returns nil, which the contract measures.
			config, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			if name == "boom" {
				return fmt.Errorf("greet %q: %w", name, ErrGreetRefused)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "hello %s (config=%s)\n", name, config)
			return nil
		},
	}
	greet.Flags().String("name", "", "who to greet")
	// MarkFlagRequired annotates the flag on this command's own FlagSet. It
	// returns an error for a name that does not exist there, which is the
	// signal you get if you mark a parent's persistent flag from the child.
	if err := greet.MarkFlagRequired("name"); err != nil {
		panic(err)
	}

	version := &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "csxtool 1.2.3")
		},
	}

	root.AddCommand(greet, version)
	return &Tree{Root: root, Greet: greet, Out: out, Err: errOut}
}

// Run clears the buffers and executes with args, which must NOT include the
// program name.
//
// It deliberately takes a []string rather than being variadic: a variadic
// Run() with no arguments passes a nil slice, and a nil slice is exactly the
// value that makes cobra fall back to os.Args[1:]. A test helper that looks
// like it runs the command with no arguments would instead run it with the
// test binary's own arguments. An empty but non-nil slice is what actually
// means "no arguments"; the contract measures both.
func (t *Tree) Run(args []string) error {
	t.Out.Reset()
	t.Err.Reset()
	t.Root.SetArgs(args)
	return t.Root.Execute()
}
