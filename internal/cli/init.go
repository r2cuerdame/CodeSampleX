package cli

import (
	"bufio"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/identity"
)

// contractText is the goal.md §5.4 contract screen, shown VERBATIM. It
// is the single question csx init ever asks (plan: "Ease of install is
// paramount") — everything else has a working default.
//
//go:embed agentassets/contract.txt
var contractText string

func init() {
	Register(Command{
		Name:    "init",
		Summary: "set up csx: one question (community/local-only), everything else automatic",
		Run: func(ctx context.Context, args []string) int {
			return initMain(ctx, args, &initEnv{
				stdin:    os.Stdin,
				stdout:   os.Stdout,
				stderr:   os.Stderr,
				userHome: os.UserHomeDir,
			})
		},
	})
}

// initEnv injects every host dependency so tests can run init against a
// fake HOME (planted agent dirs) and scripted stdin.
type initEnv struct {
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	userHome func() (string, error) // root for agent config detection
}

func initMain(_ context.Context, args []string, env *initEnv) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	var (
		community = fs.Bool("community", false, "join the community without asking")
		localOnly = fs.Bool("local-only", false, "local-only mode without asking")
		yes       = fs.Bool("yes", false, "non-interactive: community mode (unless --local-only) and auto-accept agent installs")
		noAgents  = fs.Bool("no-agents", false, "config + identity only: skip ALL agent integration (no MCP registration, no agent rule files, nothing written outside CSX_HOME)")
		server    = fs.String("server", "", "override the server URL")
	)
	fs.Usage = func() {
		fmt.Fprintln(env.stderr, "Usage: csx init [--community|--local-only] [--yes] [--no-agents] [--server URL]")
		fmt.Fprintln(env.stderr)
		fmt.Fprintln(env.stderr, "Sets up config + identity and, unless --no-agents is given, registers the")
		fmt.Fprintf(env.stderr, "csx MCP server and usage rules for every detected agent under %s\n", agentHomeEnv)
		fmt.Fprintln(env.stderr, "(when set) or your OS user home.")
		fmt.Fprintln(env.stderr)
		fmt.Fprintln(env.stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *community && *localOnly {
		fmt.Fprintln(env.stderr, "csx init: --community and --local-only are mutually exclusive")
		return 2
	}

	in := bufio.NewReader(env.stdin)
	out := env.stdout

	// The ONE question — skipped entirely by --community/--local-only/--yes.
	var mode string
	switch {
	case *localOnly:
		mode = config.ModeLocalOnly
	case *community || *yes:
		mode = config.ModeCommunity
	default:
		m, err := askContract(in, out)
		if err != nil {
			fmt.Fprintf(env.stderr, "csx init: %v\n", err)
			return 2
		}
		mode = m
	}

	// Everything below is automatic.
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(env.stderr, "csx init: %v\n", err)
		return 1
	}
	if err := config.EnsureHome(home); err != nil {
		fmt.Fprintf(env.stderr, "csx init: %v\n", err)
		return 1
	}
	cfg, err := config.Load(home)
	if err != nil {
		fmt.Fprintf(env.stderr, "csx init: %v\n", err)
		return 1
	}
	cfg.Mode = mode
	if *server != "" {
		cfg.ServerURL = *server
	}
	if err := cfg.Save(home); err != nil {
		fmt.Fprintf(env.stderr, "csx init: %v\n", err)
		return 1
	}
	ident, err := identity.LoadOrCreate(home)
	if err != nil {
		fmt.Fprintf(env.stderr, "csx init: %v\n", err)
		return 1
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Mode:    %s\n", cfg.Mode)
	fmt.Fprintf(out, "Home:    %s\n", home)
	fmt.Fprintf(out, "Peer ID: %s\n", ident.PeerID())

	// Agent integration (mode-independent: local-only users still get
	// the MCP server — everything it does stays local in that mode).
	// This is the ONLY part of init that writes outside CSX_HOME, so it
	// is both skippable (--no-agents) and redirectable (CSX_AGENT_HOME).
	var (
		results   []agentInstallResult
		agentHome string
		overrode  bool
	)
	if !*noAgents {
		var uhErr error
		agentHome, overrode, uhErr = resolveAgentHome(env.userHome)
		if uhErr != nil {
			fmt.Fprintf(env.stderr, "csx init: skipping agent integration: %v\n", uhErr)
		} else {
			var confirm func(string) bool
			if !*yes {
				confirm = func(agent string) bool {
					fmt.Fprintf(out, "Install CSX integration for %s? [y/N]: ", agent)
					line, err := in.ReadString('\n')
					if err != nil && line == "" {
						return false // EOF: safe default is no
					}
					ans := strings.ToLower(strings.TrimSpace(line))
					return ans == "y" || ans == "yes"
				}
			}
			results = installAgents(agentHome, confirm)
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Agent integrations:")
	switch {
	case *noAgents:
		fmt.Fprintln(out, "  SKIPPED (--no-agents): MCP registration was NOT performed and no agent")
		fmt.Fprintln(out, "  rule files were written — nothing outside the csx home was touched.")
		fmt.Fprintln(out, "  To install them later, re-run init without the flag:")
		fmt.Fprintf(out, "    csx init --%s --yes\n", cfg.Mode)
	case len(results) == 0:
		fmt.Fprintln(out, "  (none attempted)")
	default:
		if overrode {
			fmt.Fprintf(out, "  (agent home overridden by %s: %s)\n", agentHomeEnv, agentHome)
		}
		for _, r := range results {
			switch {
			case r.Skipped:
				fmt.Fprintf(out, "  %-12s skipped (%s)\n", r.Agent, r.Reason)
			case r.Err != nil:
				fmt.Fprintf(out, "  %-12s error: %v\n", r.Agent, r.Err)
				for _, a := range r.Actions {
					fmt.Fprintf(out, "  %-12s %s\n", "", a)
				}
			default:
				for _, a := range r.Actions {
					fmt.Fprintf(out, "  %-12s %s\n", r.Agent, a)
				}
			}
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "csx is ready. To set up another machine:")
	fmt.Fprintln(out, "  irm https://codesamplex.dev/install.ps1 | iex")
	fmt.Fprintln(out, "  curl -fsSL https://codesamplex.dev/install.sh | sh")
	return 0
}

// askContract prints the §5.4 contract screen verbatim and reads the
// single answer, re-prompting on anything unrecognized.
func askContract(in *bufio.Reader, out io.Writer) (string, error) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, strings.TrimRight(contractText, "\n"))
	fmt.Fprintln(out)
	for {
		fmt.Fprint(out, "Choose [community/local-only]: ")
		line, err := in.ReadString('\n')
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "community", "c", "join", "join community":
			return config.ModeCommunity, nil
		case "local-only", "local", "l", "local only":
			return config.ModeLocalOnly, nil
		}
		if err != nil {
			return "", fmt.Errorf("no mode chosen (answer community or local-only, or pass --community/--local-only/--yes): %w", err)
		}
		fmt.Fprintln(out, `Please answer "community" or "local-only".`)
	}
}
