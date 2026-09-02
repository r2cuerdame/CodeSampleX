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
	"strings"
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
const workerUpdateExitCode = 75

var errWorkerUpdateReady = errors.New("verified update installed; restart required")

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

// unsupportedWorkReporter is implemented by a verifier that remembers which
// offered jobs it had no verifier image for. Optional, like the announcer
// below, so the scheduling boundary stays small.
type unsupportedWorkReporter interface {
	UnsupportedWork() []string
}

// stageLogAnnouncer is implemented by a verifier that keeps the stage output
// of failed runs. It is optional so the small interface above stays small and
// test fakes are not made to carry a diagnostic they do not produce.
type stageLogAnnouncer interface {
	SetStageLogSink(func(path string))
}

type workerOptions struct {
	mode         string
	parallel     int
	budget       string
	once         bool
	pollInterval time.Duration
	drain        *workerDrain
}

// workerDrain makes the update boundary exact. A lane holds a read lock for
// the whole already-admitted RunOne; Request waits for those lanes, blocks new
// admissions, then lets the native service restart the fully drained worker.
type workerDrain struct {
	mu        sync.RWMutex
	requested bool
}

func (d *workerDrain) begin() bool {
	if d == nil {
		return true
	}
	d.mu.RLock()
	if d.requested {
		d.mu.RUnlock()
		return false
	}
	return true
}
func (d *workerDrain) end() {
	if d != nil {
		d.mu.RUnlock()
	}
}
func (d *workerDrain) request() {
	if d != nil {
		d.mu.Lock()
		d.requested = true
		d.mu.Unlock()
	}
}
func (d *workerDrain) isRequested() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.requested
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
	updates     func(context.Context, string, *config.Config, string) <-chan automaticUpdateResult
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
			// Docker Desktop serves Linux or Windows containers, never both.
			// Ask which, so a Windows machine contributes Windows evidence
			// instead of quietly producing Linux receipts.
			containerOS := sandbox.DetectContainerOS(context.Background())
			return &verifier.CrossVerifier{
				HTTP:             nil,
				ServerURL:        cfg.ServerURL,
				Ident:            ident,
				Cap:              domain.CapContainerRun,
				Runner:           sandbox.DockerRunner{ContainerOS: containerOS},
				ContainerOS:      containerOS,
				Env:              env,
				LastActivityFile: filepath.Join(home, "logs", "last-run.log"),
				// A failed cross-verification used to leave nothing readable:
				// the workspace is disposable and the receipt keeps only a
				// digest, so a reproducible failure could not be diagnosed at
				// all. These stay on this machine and are never uploaded.
				StageLogs: &verifier.StageLogStore{Home: home},
			}
		},
		notify: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(ctx, os.Interrupt)
		},
		updates: automaticUpdates,
	}
}

func workerMain(ctx context.Context, args []string) int {
	return workerMainWith(ctx, args, defaultWorkerEnv())
}

func workerUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "Usage: csx worker start [--mode verify] [--parallel 1..8] [--budget 5m|15m|idle|unlimited] [--once]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Runs only server-assigned VERIFY jobs with reason=cross or reason=matrix in disposable Docker")
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
	if env.updates != nil {
		if exe, exeErr := os.Executable(); exeErr == nil {
			drain := &workerDrain{}
			opts.drain = drain
			go func() {
				for outcome := range env.updates(runCtx, home, cfg, exe) {
					if outcome.Result.Applied {
						fmt.Fprintf(env.stdout, "Verified csx %s installed; draining active jobs before service restart.\n", outcome.Result.LatestVersion)
						drain.request()
						return
					}
					if outcome.Result.ManualInstallRequired {
						fmt.Fprintf(env.stderr, "csx worker: signed update %s needs the Windows launcher migration or a newer launcher protocol; rerun the official installer\n", outcome.Result.LatestVersion)
						continue
					}
					if outcome.Err != nil {
						fmt.Fprintf(env.stderr, "csx worker: automatic update check failed: %v\n", outcome.Err)
						continue
					}
				}
			}()
		}
	}
	fmt.Fprintln(env.stdout, "CodeSampleX Contributor Worker")
	fmt.Fprintln(env.stdout, "  mode:      VERIFY (server-assigned cross + runnable matrix jobs)")
	fmt.Fprintln(env.stdout, "  sandbox:   Docker / CONTAINER_RUN (no host fallback)")
	fmt.Fprintf(env.stdout, "  parallel:  %d\n", effectiveWorkerParallel(opts))
	fmt.Fprintf(env.stdout, "  budget:    %s\n", opts.budget)
	if opts.once {
		fmt.Fprintln(env.stdout, "  once:      at most one job")
	}
	fmt.Fprintln(env.stdout, "Press Ctrl-C to stop cleanly.")

	stats, runErr := runContributorWorker(runCtx, cv, opts, env.stdout)
	fmt.Fprintf(env.stdout, "Worker stopped: completed=%d failed=%d\n", stats.Completed, stats.Failed)
	return workerResultExitCode(stats, runErr, env.stdout, env.stderr)
}

func workerResultExitCode(stats workerStats, runErr error, stdout, stderr io.Writer) int {
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
		if errors.Is(runErr, errWorkerUpdateReady) {
			fmt.Fprintln(stdout, "Worker drained cleanly; native service will activate the update.")
			return workerUpdateExitCode
		}
		fmt.Fprintf(stderr, "csx worker: %v\n", runErr)
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
// noWorkHeartbeat bounds how often an idle worker repeats itself.
//
// Fifteen minutes: often enough that a watcher can tell a live idle node from
// a hung one within one check, rare enough that a day of legitimate idleness
// is ninety-six lines rather than tens of thousands.
const noWorkHeartbeat = 15 * time.Minute

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
	// A kept log is announced once, as it is written. Without this the only
	// thing stdout said about a failed job was that the counter went up, and
	// an operator had no way to know a diagnosis had been saved, let alone
	// where.
	if announcer, ok := cv.(stageLogAnnouncer); ok {
		announcer.SetStageLogSink(func(path string) {
			printMu.Lock()
			defer printMu.Unlock()
			fmt.Fprintf(out, "verification failed; stage logs kept locally: %s\n", path)
		})
	}
	// An idle worker has to be able to say it is idle.
	//
	// Reported from the farm: 140 jobs in 24 hours, then a day of "0 seconds
	// of CPU, idle since restart", with not one line in the log -- no "no
	// work", no "waiting", no "idle". From outside, a verifier that is
	// correctly idle and one that has hung are the same thing.
	//
	// And idleness is the EXPECTED state here, not a fault: a peer holding a
	// receipt for a sample is never offered that sample's cross job again, so
	// a node that authored nearly everything has almost nothing left it is
	// allowed to verify. Silence is the wrong way to report a designed
	// outcome.
	//
	// A heartbeat rather than a line per poll: the poll interval is seconds,
	// and a line per poll is a log nobody reads -- the same failure wearing
	// different clothes.
	var idleMu sync.Mutex
	var idlePolls int
	var idleSince time.Time
	var lastIdleSaid time.Time
	reportIdle := func(now time.Time) {
		idleMu.Lock()
		defer idleMu.Unlock()
		idlePolls++
		if idleSince.IsZero() {
			idleSince = now
		}
		if !lastIdleSaid.IsZero() && now.Sub(lastIdleSaid) < noWorkHeartbeat {
			return
		}
		lastIdleSaid = now
		printMu.Lock()
		defer printMu.Unlock()
		fmt.Fprintf(out, "no work offered: %d poll(s) over %v; completed=%d failed=%d\n",
			idlePolls, now.Sub(idleSince).Round(time.Second), completed.Load(), failed.Load())
	}
	clearIdle := func() {
		idleMu.Lock()
		defer idleMu.Unlock()
		idlePolls, idleSince, lastIdleSaid = 0, time.Time{}, time.Time{}
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
				if opts.drain.isRequested() {
					return
				}
				if err := runCtx.Err(); err != nil {
					return
				}
				if opts.budget == "idle" && !cv.IsIdle() {
					if opts.once || !waitWorkerPoll(runCtx, opts.pollInterval) {
						return
					}
					continue
				}

				if !opts.drain.begin() {
					return
				}
				worked, err := cv.RunOne(runCtx)
				opts.drain.end()
				if opts.drain.isRequested() && err == nil {
					if worked {
						completed.Add(1)
						printCounts("job completed:", nil)
					}
					return
				}
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
					clearIdle()
					printCounts("job completed:", nil)
					if opts.once {
						return
					}
					continue
				}
				reportIdle(time.Now())
				if opts.once || !waitWorkerPoll(runCtx, opts.pollInterval) {
					return
				}
			}
		}()
	}
	wg.Wait()

	// An idle run has two very different causes and used to print one line.
	// The queue can be empty, or it can be full of work no image in this
	// build can run -- and only the second one is waiting for a human to
	// either add a lane or fix what asked for it.
	if completed.Load() == 0 && failed.Load() == 0 {
		if reporter, ok := cv.(unsupportedWorkReporter); ok {
			if coordinates := dedupeCoordinates(reporter.UnsupportedWork()); len(coordinates) > 0 {
				printMu.Lock()
				fmt.Fprintf(out, "no runnable work: the queue offered %d coordinate(s) this build has no verifier image for: %s\n",
					len(coordinates), strings.Join(coordinates, ", "))
				printMu.Unlock()
			}
		}
	}

	stats := workerStats{Completed: completed.Load(), Failed: failed.Load()}
	if opts.drain.isRequested() {
		return stats, errWorkerUpdateReady
	}
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

// dedupeCoordinates keeps the first occurrence of each coordinate so several
// parallel lanes scanning the same queue report one list, not one per lane.
func dedupeCoordinates(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}
