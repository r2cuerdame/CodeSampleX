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
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
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
				warm:     warmShardCache,
				stopDaemon: func(ctx context.Context) error {
					home, err := config.Home()
					if err != nil {
						return err
					}
					dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					_, err = daemon.StopRunning(dctx, home)
					return err
				},
				startDaemon: func(ctx context.Context) error {
					home, err := config.Home()
					if err != nil {
						return err
					}
					dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					_, err = daemon.EnsureRunning(dctx, home, Version)
					return err
				},
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
	warm     func(context.Context, io.Writer)
	// nil in unit tests that do not want to spawn a process.
	stopDaemon  func(context.Context) error
	startDaemon func(context.Context) error
}

func initMain(ctx context.Context, args []string, env *initEnv) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	var (
		community = fs.Bool("community", false, "join the community without asking")
		localOnly = fs.Bool("local-only", false, "local-only mode without asking")
		yes       = fs.Bool("yes", false, "non-interactive: community mode (unless --local-only) and auto-accept agent installs")
		noAgents  = fs.Bool("no-agents", false, "config + identity only: skip ALL agent integration (no MCP registration, no agent rule files, nothing written outside CSX_HOME)")
		noDaemon  = fs.Bool("no-daemon", false, "do not run the background sync daemon (for worker-only machines)")
		server    = fs.String("server", "", "override the server URL")
	)
	fs.Usage = func() {
		fmt.Fprintln(env.stderr, "Usage: csx init [--community|--local-only] [--yes] [--no-agents] [--no-daemon] [--server URL]")
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
	// A daemon keeps the config it loaded at process start. Stop it before a
	// mode/server transition, especially before revoking community consent;
	// otherwise the old in-memory community config can continue uploading
	// after config.json says local-only.
	daemonConfigChanged := cfg.Mode != mode || (*server != "" && cfg.ServerURL != *server)
	// A worker-only machine should run one process: the contributor worker.
	// --no-daemon therefore also stops an already-running daemon even when
	// the saved mode did not change.
	if *noDaemon {
		daemonConfigChanged = true
	}
	if daemonConfigChanged && env.stopDaemon != nil {
		if err := env.stopDaemon(ctx); err != nil {
			fmt.Fprintf(env.stderr, "csx init: could not stop the existing daemon before changing privacy/network settings: %v\n", err)
			return 1
		}
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
			var confirm func(string) (bool, bool)
			if !*yes {
				noInput := false
				confirm = func(agent string) (bool, bool) {
					if noInput {
						return false, false // already learned nobody is there
					}
					fmt.Fprintf(out, "Install CSX integration for %s? [y/N]: ", agent)
					line, err := in.ReadString('\n')
					if err != nil && line == "" {
						// EOF. Consent is never inferred, so nothing is
						// installed — but the reason is that nobody was
						// asked, and the remaining agents are not prompted
						// into a closed pipe.
						fmt.Fprintln(out)
						noInput = true
						return false, false
					}
					ans := strings.ToLower(strings.TrimSpace(line))
					return ans == "y" || ans == "yes", true
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

	if env.warm != nil {
		env.warm(ctx, out)
	}
	if *noDaemon {
		fmt.Fprintln(out, "  background sync not started (--no-daemon: worker-only setup)")
	} else if cfg.Mode == config.ModeCommunity && env.startDaemon != nil {
		if err := env.startDaemon(ctx); err != nil {
			fmt.Fprintf(out, "  background sync not started (%v) — run `csx daemon start`\n", err)
		} else {
			fmt.Fprintln(out, "  background sync running")
		}
	}

	// An agent that was never registered is an agent that never calls csx,
	// and the run above can end that way without anything saying so — the
	// piped install reaches EOF at the first prompt and skips every agent.
	// "csx is ready" on its own would be true of the config and false of
	// the thing the user installed this for.
	var unasked []string
	for _, r := range results {
		if r.Skipped && strings.Contains(r.Reason, "not asked") {
			unasked = append(unasked, r.Agent)
		}
	}
	if len(unasked) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "NOT registered with %s: nothing read the prompt, so nothing\n",
			strings.Join(unasked, ", "))
		fmt.Fprintln(out, "was installed. Your agent cannot call csx until it is. In a terminal, run:")
		fmt.Fprintf(out, "  csx init --%s --yes\n", cfg.Mode)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "csx is ready. To set up another machine:")
	fmt.Fprintln(out, "  irm https://codesamplex.dev/install.ps1 | iex")
	fmt.Fprintln(out, "  curl -fsSL https://codesamplex.dev/install.sh | sh")
	return 0
}

// askContract prints the §5.4 contract screen verbatim and reads the
// single answer, re-prompting on anything unrecognized.
//
// The answer is a single keystroke: typing "community" in full is a lot of
// work for the only question csx ever asks, and arrow-key menus need raw
// terminal mode, which breaks under pipes, CI and non-tty installers.
// Numbers work everywhere; the spelled-out words still answer too.
func askContract(in *bufio.Reader, out io.Writer) (string, error) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, strings.TrimRight(contractText, "\n"))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  1) JOIN COMMUNITY   share anonymous public-package evidence, get the network")
	// "nothing ever leaves this machine" was not literally true and could
	// not be: warming the cache is a request to the server. What is true,
	// and is what the reader is actually deciding about, is that nothing
	// ABOUT THEIR PROJECTS leaves — no packages, no versions, no symbols,
	// no results. The cache warms from the server's own popularity list,
	// which is identical for everyone.
	fmt.Fprintln(out, "  2) LOCAL ONLY       nothing about your projects ever leaves this machine")
	for {
		fmt.Fprint(out, "Choose [1/2] (default 1): ")
		line, err := in.ReadString('\n')

		// Bare Enter and end-of-input both take the advertised default. This
		// matters for piped installers, whose stdin is already consumed: a
		// plain `csx init` must have the same community default whether it is
		// run interactively or by an installer. Users can always opt out with
		// the explicit, re-runnable --local-only flag.
		if err != nil {
			if strings.TrimSpace(line) == "" {
				fmt.Fprintln(out)
				fmt.Fprintln(out, "No answer received (input is not a terminal). Choosing the default: COMMUNITY.")
				fmt.Fprintln(out, "To keep all project evidence local instead, run:")
				fmt.Fprintln(out, "  csx init --local-only")
				return config.ModeCommunity, nil
			}
			return "", fmt.Errorf("no mode chosen (answer 1 or 2, or pass --community/--local-only/--yes): %w", err)
		}

		switch strings.ToLower(strings.TrimSpace(line)) {
		case "1", "", "community", "c", "join", "join community":
			return config.ModeCommunity, nil
		case "2", "local-only", "local", "l", "local only":
			return config.ModeLocalOnly, nil
		}
		fmt.Fprintln(out, `Please answer 1 (community) or 2 (local only).`)
	}
}

// warmShardCache does the first shard sync so the user's FIRST question can
// be answered.
//
// Measured in a clean container with a real Go project and the toolchain
// present: before any sync the first query returns NO_SAFE_MATCH, and after
// one the same query returns EXACT. Search is local-cache-first, and an
// empty cache has nothing to match against — so every new install used to
// miss on its first question, which is the worst possible first impression
// and the user had no way to know why. It was documented nowhere.
//
// It is safe in both modes. SyncNow is a complete no-op outside community
// mode; even its popularity-list GET is forbidden because local-only promises
// that this process transmits nothing at all.
//
// Failure is never fatal. A machine installing without a network, or behind
// a proxy that blocks it, still finishes init — it is told the cache is cold
// and how to warm it later, rather than having init fail over something that
// is only an optimisation of the first query.
func warmShardCache(ctx context.Context, out io.Writer) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Compatibility cache:")

	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(out, "  not warmed (%v) — run `csx sync` before your first question\n", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, shardWarmTimeout)
	defer cancel()

	d, err := daemon.New(home)
	if err != nil {
		fmt.Fprintf(out, "  not warmed (%v) — run `csx sync` before your first question\n", err)
		return
	}
	defer d.Close()

	res := d.SyncNow(ctx)
	switch {
	case res.WarmedKeys > 0 && len(res.Errors) == 0:
		fmt.Fprintf(out, "  warmed %d shard keys\n", res.WarmedKeys)
	case len(res.Errors) > 0:
		fmt.Fprintf(out, "  partly warmed (%s)\n", res.Errors[0])
		fmt.Fprintln(out, "  run `csx sync` once you have a network to finish it")
	default:
		fmt.Fprintln(out, "  nothing to warm yet — `csx sync` after your first build")
	}
}

// shardWarmTimeout bounds the first sync. An install must not hang on a
// network that is not there.
const shardWarmTimeout = 20 * time.Second
