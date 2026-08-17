package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/verifier"
)

const workerPollInterval = 10 * time.Second

func init() {
	Register(Command{
		Name:    "worker",
		Summary: "contribute Docker-isolated verification: csx worker start",
		Run:     workerMain,
	})
}

// contributionVerifier is the deliberately small boundary between the CLI
// scheduler and CrossVerifier. The production implementation downloads only
// server-assigned, content-addressed artifacts and executes them through the
// DockerRunner; tests can exercise scheduling without running sample code.
type contributionVerifier interface {
	RunOne(context.Context) (worked bool, err error)
	IsIdle() bool
}

type workerOptions struct {
	mode         string
	parallel     int
	budget       string
	once         bool
	pollInterval time.Duration
}

type workerStats struct {
	Completed int64
	Failed    int64
}

// workerEnv keeps host and process effects injectable. In particular, tests
// prove that local-only mode returns before Docker detection or identity
// creation, so the refusal cannot accidentally contact the network.
type workerEnv struct {
	stdout      io.Writer
	stderr      io.Writer
	home        func() (string, error)
	load        func(string) (*config.Config, error)
	ensure      func(string) error
	detect      func(context.Context) domain.SandboxCapability
	ident       func(string) (*identity.Identity, error)
	collect     func(context.Context, map[string]string) domain.EnvironmentFingerprint
	newVerifier func(home string, cfg *config.Config, ident *identity.Identity, env domain.EnvironmentFingerprint) contributionVerifier
	notify      func(context.Context) (context.Context, context.CancelFunc)
}

func defaultWorkerEnv() *workerEnv {
	return &workerEnv{
		stdout:  os.Stdout,
		stderr:  os.Stderr,
		home:    config.Home,
		load:    config.Load,
		ensure:  config.EnsureHome,
		detect:  sandbox.Detect,
		ident:   identity.LoadOrCreate,
		collect: environment.Collect,
		newVerifier: func(home string, cfg *config.Config, ident *identity.Identity, env domain.EnvironmentFingerprint) contributionVerifier {
			return &verifier.CrossVerifier{
				HTTP:             nil,
				ServerURL:        cfg.ServerURL,
				Ident:            ident,
				Cap:              domain.CapContainerRun,
				Runner:           sandbox.DockerRunner{},
				Env:              env,
				LastActivityFile: filepath.Join(home, "logs", "last-run.log"),
			}
		},
		notify: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(ctx, os.Interrupt)
		},
	}
}

func workerMain(ctx context.Context, args []string) int {
	return workerMainWith(ctx, args, defaultWorkerEnv())
}

func workerUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "Usage: csx worker start [--mode verify] [--parallel 1..8] [--budget 5m|15m|idle|unlimited] [--once]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Runs only server-assigned VERIFY jobs with reason=cross in disposable Docker")
	fmt.Fprintln(w, "workspaces. EXPAND and CREATE are not available in the public worker yet.")
	fmt.Fprintln(w, "Community mode and a reachable Docker daemon are required; downloaded sample")
	fmt.Fprintln(w, "code is never executed directly on the host.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fs.SetOutput(w)
	fs.PrintDefaults()
}

func workerMainWith(ctx context.Context, args []string, env *workerEnv) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fs, _ := newWorkerFlagSet(env.stderr)
		workerUsage(env.stdout, fs)
		return 0
	}
	if args[0] != "start" {
		fmt.Fprintf(env.stderr, "csx worker: unknown command %q\n", args[0])
		fs, _ := newWorkerFlagSet(env.stderr)
		workerUsage(env.stderr, fs)
		return 2
	}

	fs, values := newWorkerFlagSet(env.stderr)
	fs.Usage = func() { workerUsage(env.stderr, fs) }
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(env.stderr, "csx worker start: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	if *values.mode != "verify" {
		fmt.Fprintf(env.stderr, "csx worker start: mode %q is unavailable; the public worker currently supports verify only\n", *values.mode)
		return 2
	}
	if *values.parallel < 1 || *values.parallel > 8 {
		fmt.Fprintln(env.stderr, "csx worker start: --parallel must be between 1 and 8")
		return 2
	}
	if _, ok := workerBudgetDuration(*values.budget); !ok {
		fmt.Fprintln(env.stderr, "csx worker start: --budget must be 5m, 15m, idle, or unlimited")
		return 2
	}

	home, err := env.home()
	if err != nil {
		fmt.Fprintf(env.stderr, "csx worker start: %v\n", err)
		return 1
	}
	cfg, err := env.load(home)
	if err != nil {
		fmt.Fprintf(env.stderr, "csx worker start: %v\n", err)
		return 1
	}
	if cfg.Mode != config.ModeCommunity {
		fmt.Fprintln(env.stderr, "csx worker start: COMMUNITY mode is required; no network request or sample execution was attempted")
		fmt.Fprintln(env.stderr, "Run `csx init --community` first, then start the worker again.")
		return 1
	}
	if env.detect(ctx) != domain.CapContainerRun {
		fmt.Fprintln(env.stderr, "csx worker start: a reachable Docker daemon is required; host execution is never used as a fallback")
		return 1
	}
	if err := env.ensure(home); err != nil {
		fmt.Fprintf(env.stderr, "csx worker start: %v\n", err)
		return 1
	}
	ident, err := env.ident(home)
	if err != nil {
		fmt.Fprintf(env.stderr, "csx worker start: %v\n", err)
		return 1
	}
	hostEnv := env.collect(ctx, nil)
	cv := env.newVerifier(home, cfg, ident, hostEnv)

	runCtx, stop := env.notify(ctx)
	defer stop()
	opts := workerOptions{
		mode:         *values.mode,
		parallel:     *values.parallel,
		budget:       *values.budget,
		once:         *values.once,
		pollInterval: workerPollInterval,
	}
	fmt.Fprintln(env.stdout, "CodeSampleX Contributor Worker")
	fmt.Fprintln(env.stdout, "  mode:      VERIFY (server-assigned cross jobs only)")
	fmt.Fprintln(env.stdout, "  sandbox:   Docker / CONTAINER_RUN (no host fallback)")
	fmt.Fprintf(env.stdout, "  parallel:  %d\n", effectiveWorkerParallel(opts))
	fmt.Fprintf(env.stdout, "  budget:    %s\n", opts.budget)
	if opts.once {
		fmt.Fprintln(env.stdout, "  once:      at most one job")
	}
	fmt.Fprintln(env.stdout, "Press Ctrl-C to stop cleanly.")

	stats, runErr := runContributorWorker(runCtx, cv, opts, env.stdout)
	fmt.Fprintf(env.stdout, "Worker stopped: completed=%d failed=%d\n", stats.Completed, stats.Failed)
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
		fmt.Fprintf(env.stderr, "csx worker: %v\n", runErr)
		return 1
	}
	if stats.Failed > 0 {
		return 1
	}
	return 0
}

type workerFlagValues struct {
	mode     *string
	parallel *int
	budget   *string
	once     *bool
}

func newWorkerFlagSet(out io.Writer) (*flag.FlagSet, workerFlagValues) {
	fs := flag.NewFlagSet("worker start", flag.ContinueOnError)
	fs.SetOutput(out)
	values := workerFlagValues{
		mode:     fs.String("mode", "verify", "job mode (only verify is available; expand/create are unavailable)"),
		parallel: fs.Int("parallel", 2, "number of concurrent Docker verification lanes (1..8)"),
		budget:   fs.String("budget", "idle", "run budget: 5m, 15m, idle, or unlimited"),
		once:     fs.Bool("once", false, "process at most one available job, then exit"),
	}
	return fs, values
}

func workerBudgetDuration(budget string) (time.Duration, bool) {
	switch budget {
	case "5m":
		return 5 * time.Minute, true
	case "15m":
		return 15 * time.Minute, true
	case "idle", "unlimited":
		return 0, true
	default:
		return 0, false
	}
}

func effectiveWorkerParallel(opts workerOptions) int {
	if opts.once {
		return 1
	}
	return opts.parallel
}

// runContributorWorker schedules Docker verification lanes. Queue polling is
// cheap and declarative; the shared CrossVerifier serializes only list+claim,
// then the expensive container work proceeds in parallel.
func runContributorWorker(ctx context.Context, cv contributionVerifier, opts workerOptions, out io.Writer) (workerStats, error) {
	duration, ok := workerBudgetDuration(opts.budget)
	if !ok {
		return workerStats{}, fmt.Errorf("unknown budget %q", opts.budget)
	}
	if opts.parallel < 1 || opts.parallel > 8 {
		return workerStats{}, fmt.Errorf("parallel must be between 1 and 8")
	}
	if opts.pollInterval <= 0 {
		opts.pollInterval = workerPollInterval
	}

	runCtx := ctx
	cancel := func() {}
	if duration > 0 {
		runCtx, cancel = context.WithTimeout(ctx, duration)
	}
	defer cancel()

	var completed atomic.Int64
	var failed atomic.Int64
	var printMu sync.Mutex
	var firstErr error
	var errMu sync.Mutex
	recordErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	printCounts := func(label string, err error) {
		printMu.Lock()
		defer printMu.Unlock()
		if err == nil {
			fmt.Fprintf(out, "%s completed=%d failed=%d\n", label, completed.Load(), failed.Load())
			return
		}
		fmt.Fprintf(out, "%s completed=%d failed=%d (%v)\n", label, completed.Load(), failed.Load(), err)
	}

	lanes := effectiveWorkerParallel(opts)
	var wg sync.WaitGroup
	wg.Add(lanes)
	for range lanes {
		go func() {
			defer wg.Done()
			for {
				if err := runCtx.Err(); err != nil {
					return
				}
				if opts.budget == "idle" && !cv.IsIdle() {
					if opts.once || !waitWorkerPoll(runCtx, opts.pollInterval) {
						return
					}
					continue
				}

				worked, err := cv.RunOne(runCtx)
				if err != nil {
					if runCtx.Err() != nil {
						return
					}
					if worked {
						failed.Add(1)
						printCounts("job failed:", err)
					} else {
						printCounts("queue unavailable; retrying:", err)
					}
					recordErr(err)
					if opts.once || !waitWorkerPoll(runCtx, opts.pollInterval) {
						return
					}
					continue
				}
				if worked {
					completed.Add(1)
					printCounts("job completed:", nil)
					if opts.once {
						return
					}
					continue
				}
				if opts.once || !waitWorkerPoll(runCtx, opts.pollInterval) {
					return
				}
			}
		}()
	}
	wg.Wait()

	stats := workerStats{Completed: completed.Load(), Failed: failed.Load()}
	if opts.once {
		errMu.Lock()
		defer errMu.Unlock()
		return stats, firstErr
	}
	return stats, nil
}

func waitWorkerPoll(ctx context.Context, interval time.Duration) bool {
	t := time.NewTimer(interval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
