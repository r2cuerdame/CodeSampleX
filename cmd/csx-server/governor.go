package main

// The resource governor (#454): background work is what gets shed first when
// this server runs out of database or host CPU.
//
// The two mechanisms that came before it are both passive. Per-class pool
// admission (internal/serverstore/pool.go) guarantees interactive reads a
// floor no background caller can take, and the Builder's own yield (#445)
// steps aside between batches while readers are being refused. Both act
// inside one pass, and neither can stop the next one from starting: a
// standalone Builder (#458) and CodeSampleX-Farm's ingest (#461) are
// separate processes that keep arriving regardless of what this server is
// living through.
//
// This governor is the active half. It samples what the pool actually
// refused in the last few seconds, and what the hypervisor took from this VM
// in the same window, and when either crosses its threshold it pauses both
// background producers -- the Builder through its lease's pause flag, Farm
// ingest by capping its admission ceiling to zero -- then puts both back the
// moment the window comes back clean. Nothing about that requires an
// operator, a restart or a deploy, which is the acceptance line #454 is
// written against: "background jobs must make forward progress after
// pressure clears without manual restart".
//
// The host-steal branch exists because the two causes have opposite
// responses. A pool this server saturated is a pool this server can shed
// load off; a VM that is not being scheduled by its hypervisor is not
// something any retry, timeout or connection count can fix, and #454 says
// so outright: classify it as infrastructure contention and stop tuning
// application values as a substitute. The governor still sheds background
// work under steal (it is the cheapest thing to give up, and it keeps the
// site answering), but it names the reason differently so an operator reads
// "resize or migrate", not "tune the pool".

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/hostpressure"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type governorThresholds struct {
	InteractiveRefusalRate float64 // Busy/Attempts over the sampled window
	HostStealPercent       float64
}

// defaultGovernorThresholds are the shipped trip points.
//
// 10% interactive refusals in one window: a single refused page read is
// noise, but one in ten is the site already telling visitors 503, and by
// then the cheapest connection to hand back is one the Builder or Farm is
// holding.
//
// 20% steal: sustained double-digit steal on a 2-vCPU instance is the
// hypervisor withholding roughly a fifth of the CPU this VM is paying for.
// Below that, short spikes are ordinary noisy-neighbour behaviour; above it,
// application tuning has nothing left to give.
func defaultGovernorThresholds() governorThresholds {
	return governorThresholds{InteractiveRefusalRate: 0.10, HostStealPercent: 20}
}

type governorDecision struct {
	PauseBuilder    bool
	PauseFarmIngest bool
	Reason          string
}

// The reason strings are part of this package's operator contract: they are
// what the governor's log line carries, what docs/operations.md's runbook
// tells an operator to grep for, and what the pressure-drill script matches
// on. Changing one is an operator-visible change, not a rename.
const (
	reasonNone                    = ""
	reasonInteractivePoolPressure = "interactive-pool-pressure"
	reasonHostCPUSteal            = "host-cpu-steal"
)

// decide is the whole policy, as a pure function of one window's counters:
// no clock, no I/O, no state. Everything around it -- sampling, windowing,
// the pause and resume calls -- is mechanism, and this is the part worth
// arguing about in a unit test.
//
// stats must be a WINDOW: the counters accumulated since the previous
// sample, not the process-lifetime totals PoolStats returns. Fed cumulative
// counters, a pause would be permanent, because the refusals that caused it
// never leave the numerator. See poolStatsWindow.
func decide(stats serverstore.PoolStats, host hostpressure.Reading, th governorThresholds) governorDecision {
	for _, c := range stats.Classes {
		if c.Class != "interactive" || c.Attempts == 0 {
			continue
		}
		if float64(c.Busy)/float64(c.Attempts) >= th.InteractiveRefusalRate {
			return governorDecision{PauseBuilder: true, PauseFarmIngest: true, Reason: reasonInteractivePoolPressure}
		}
	}
	if host.StealPercent >= th.HostStealPercent {
		return governorDecision{PauseBuilder: true, PauseFarmIngest: true, Reason: reasonHostCPUSteal}
	}
	return governorDecision{}
}

// poolStatsWindow subtracts one PoolStats snapshot from a later one, so
// decide sees what happened during the interval rather than since boot.
//
// Only the cumulative counters are differenced; the point-in-time fields
// (Limit, InUse) are carried through from the newer snapshot, where they are
// already current. A counter that moved backwards -- a pool that was closed
// and reopened -- contributes 0 rather than an enormous unsigned wraparound.
func poolStatsWindow(prev, cur serverstore.PoolStats) serverstore.PoolStats {
	before := make(map[string]serverstore.ClassPoolStats, len(prev.Classes))
	for _, c := range prev.Classes {
		before[c.Class] = c
	}
	window := cur
	window.Classes = make([]serverstore.ClassPoolStats, 0, len(cur.Classes))
	for _, c := range cur.Classes {
		p := before[c.Class]
		c.Attempts = monotoneDelta(p.Attempts, c.Attempts)
		c.Busy = monotoneDelta(p.Busy, c.Busy)
		c.Acquired = monotoneDelta(p.Acquired, c.Acquired)
		c.Waited = monotoneDelta(p.Waited, c.Waited)
		c.Suppressed = monotoneDelta(p.Suppressed, c.Suppressed)
		c.Canceled = monotoneDelta(p.Canceled, c.Canceled)
		c.Failed = monotoneDelta(p.Failed, c.Failed)
		c.Timeouts = monotoneDelta(p.Timeouts, c.Timeouts)
		window.Classes = append(window.Classes, c)
	}
	return window
}

func monotoneDelta(prev, cur uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

// ------------------------------------------------------------ the loop --

// defaultGovernorInterval is how often the governor samples. Short enough
// that a pause lands inside the incident rather than after it, long enough
// that the window carries a meaningful number of acquisitions.
const defaultGovernorInterval = 5 * time.Second

// poolStatsReader, hostSampler, builderPauser and farmIngestLimiter are the
// four seams the governor needs, named as narrowly as it uses them. The real
// implementations are *serverstore.PG (the first and the last),
// *hostpressure.Sampler and *compatibility.Leader; the tests supply fakes,
// which is the only way a decision loop like this can be proven to pause AND
// resume without a database and a hypervisor.
type poolStatsReader interface {
	PoolStats() serverstore.PoolStats
}

type hostSampler interface {
	Sample() (hostpressure.Reading, error)
}

type builderPauser interface {
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
}

type farmIngestLimiter interface {
	SetFarmIngestConns(n int)
}

// governor is one process's shedding loop. Every field but the counters
// below is set once at construction and never written again; the loop is a
// single goroutine, so the running state needs no locking.
type governor struct {
	pool       poolStatsReader
	host       hostSampler
	builder    builderPauser
	farm       farmIngestLimiter
	farmConns  int // the configured ceiling, restored on resume
	thresholds governorThresholds
	interval   time.Duration
	logf       func(format string, args ...any)

	prev     serverstore.PoolStats
	havePrev bool
	// state is the decision currently in force. Its zero value is "nothing
	// paused", which is also what the loop asserts on its first tick: a
	// predecessor process that died while paused must not leave the Builder
	// paused forever, and clearing it here is what makes a restart recover.
	state governorDecision
	// needResume is set while a Resume is owed and not yet acknowledged by
	// the database. It starts true so the boot-time clear above is retried
	// until it succeeds rather than lost to one failed round trip.
	needResume bool
	// hostErrLogged suppresses the repeat of a host-sampling error that will
	// be identical on every tick for the life of the process -- notably
	// ErrUnsupportedPlatform on every non-Linux build. ctrlErrLogged does
	// the same for a pause/resume the database refused.
	hostErrLogged string
	ctrlErrLogged string
}

// runGovernor samples, decides and acts until ctx ends. It is the entry
// point cmd/csx-server's serve path starts as a goroutine.
//
// farmConns is the configured FarmIngestConns the governor restores on
// resume; it is passed in rather than read back from the pool because the
// pool's live value is the thing this loop is changing.
func runGovernor(
	ctx context.Context,
	pool poolStatsReader,
	host hostSampler,
	builder builderPauser,
	farm farmIngestLimiter,
	farmConns int,
	interval time.Duration,
) {
	g := &governor{
		pool:       pool,
		host:       host,
		builder:    builder,
		farm:       farm,
		farmConns:  farmConns,
		thresholds: defaultGovernorThresholds(),
		interval:   interval,
		logf:       log.Printf,
		needResume: true,
	}
	g.run(ctx)
}

// startGovernor launches the loop for csx-server's serve path.
//
// It pauses the Builder through the same named lease both Builder
// topologies use (CSX_BUILDER_MODE=inprocess runs it in this process under
// its own owner identity, standalone runs it in cmd/csx-builder), so one
// pause reaches whichever one is actually running. The owner identity it
// takes for itself is never used to hold the lease -- the governor does not
// contend for it, it only writes the pause flag.
func startGovernor(ctx context.Context, cfg serverstore.ServerConfig, pg *serverstore.PG, stdout io.Writer) {
	leader := &compatibility.Leader{
		Store: pg,
		Cfg: compatibility.LeaseConfig{
			Owner: serverstore.NewProcessLeaseOwner("csx-server-governor"),
		},
	}
	fmt.Fprintf(stdout, "csx-server: resource governor active (every %s, interactive refusals >= %.0f%%, host steal >= %.0f%%)\n",
		defaultGovernorInterval,
		defaultGovernorThresholds().InteractiveRefusalRate*100,
		defaultGovernorThresholds().HostStealPercent)
	go runGovernor(ctx, pg, hostpressure.NewSampler(), leader, pg, cfg.DBPool.FarmIngestConns, defaultGovernorInterval)
}

func (g *governor) run(ctx context.Context) {
	if g.interval <= 0 {
		g.interval = defaultGovernorInterval
	}
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.tick(ctx)
		}
	}
}

// tick is one sample-decide-act cycle.
func (g *governor) tick(ctx context.Context) governorDecision {
	cur := g.pool.PoolStats()
	window := serverstore.PoolStats{}
	if g.havePrev {
		window = poolStatsWindow(g.prev, cur)
	}
	g.prev, g.havePrev = cur, true

	d := decide(window, g.sampleHost(), g.thresholds)
	g.apply(ctx, d, window)
	return d
}

// sampleHost returns the host reading, or a zero reading when there is no
// signal to be had. A failed or unsupported sample must never read as "0%
// steal measured": decide treats the zero value as "this branch has nothing
// to say", which is the only honest answer when /proc could not be read.
func (g *governor) sampleHost() hostpressure.Reading {
	if g.host == nil {
		return hostpressure.Reading{}
	}
	r, err := g.host.Sample()
	if err != nil {
		if msg := err.Error(); msg != g.hostErrLogged {
			g.hostErrLogged = msg
			g.logf("csx-server: governor has no host CPU signal: %v (shedding on pool pressure only)", err)
		}
		return hostpressure.Reading{}
	}
	g.hostErrLogged = ""
	return r
}

// apply puts the decision into effect and logs only when it changed. During
// an incident every tick decides the same thing; one line per transition
// says everything a line every five seconds would, and it is the line an
// operator can actually find afterwards.
func (g *governor) apply(ctx context.Context, d governorDecision, window serverstore.PoolStats) {
	if d.PauseBuilder {
		// Re-asserted on every paused tick, not only on the transition: the
		// pause carries a TTL (compatibility.LeaseConfig.PauseTTL) so that a
		// governor which dies mid-incident cannot leave the Builder paused
		// forever. Refreshing it is what keeps the pause alive while this
		// process is alive to mean it.
		if err := g.builder.Pause(ctx); err != nil {
			g.logPauseError("pause", err)
		}
		g.needResume = true
	} else if g.needResume {
		if err := g.builder.Resume(ctx); err != nil {
			g.logPauseError("resume", err)
		} else {
			g.needResume = false
		}
	}

	if d.PauseFarmIngest {
		g.farm.SetFarmIngestConns(0)
	} else {
		g.farm.SetFarmIngestConns(g.farmConns)
	}

	if d == g.state {
		return
	}
	prev := g.state
	g.state = d
	if d.Reason != reasonNone {
		i := interactiveWindow(window)
		g.logf("csx-server: governor paused background work reason=%s builder=paused farm_ingest=paused"+
			" interactive_busy=%d interactive_attempts=%d",
			d.Reason, i.Busy, i.Attempts)
		return
	}
	g.logf("csx-server: governor resumed background work after=%s builder=running farm_ingest=%d",
		prev.Reason, g.farmConns)
}

// logPauseError reports a control-plane failure once per distinct message.
// The governor retries on the next tick regardless; what must not happen is
// one unreachable database turning into a log line every five seconds for
// the rest of the incident.
func (g *governor) logPauseError(action string, err error) {
	msg := action + ": " + err.Error()
	if msg == g.ctrlErrLogged {
		return
	}
	g.ctrlErrLogged = msg
	g.logf("csx-server: governor could not %s the builder: %v (retrying next tick)", action, err)
}

func interactiveWindow(stats serverstore.PoolStats) serverstore.ClassPoolStats {
	for _, c := range stats.Classes {
		if c.Class == serverstore.ClassInteractive.String() {
			return c
		}
	}
	return serverstore.ClassPoolStats{}
}
