package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/hostpressure"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestGovernorDecidePausesOnInteractivePoolPressure(t *testing.T) {
	stats := serverstore.PoolStats{
		Classes: []serverstore.ClassPoolStats{
			{Class: "interactive", Busy: 50, Attempts: 100}, // 50% refusal rate
		},
	}
	d := decide(stats, hostpressure.Reading{}, defaultGovernorThresholds())
	if !d.PauseBuilder || !d.PauseFarmIngest {
		t.Fatalf("expected both paused under interactive pressure, got %+v", d)
	}
	if d.Reason != reasonInteractivePoolPressure {
		t.Fatalf("Reason = %q, want %q", d.Reason, reasonInteractivePoolPressure)
	}
}

func TestGovernorDecideResumesWhenClear(t *testing.T) {
	stats := serverstore.PoolStats{
		Classes: []serverstore.ClassPoolStats{
			{Class: "interactive", Busy: 0, Attempts: 100},
		},
	}
	d := decide(stats, hostpressure.Reading{StealPercent: 0.1, LoadAvg1: 0.5}, defaultGovernorThresholds())
	if d.PauseBuilder || d.PauseFarmIngest {
		t.Fatalf("expected no pause when clear, got %+v", d)
	}
}

func TestGovernorDecidePausesOnSustainedHostSteal(t *testing.T) {
	stats := serverstore.PoolStats{Classes: []serverstore.ClassPoolStats{{Class: "interactive"}}}
	d := decide(stats, hostpressure.Reading{StealPercent: 25}, defaultGovernorThresholds())
	if !d.PauseBuilder || !d.PauseFarmIngest {
		t.Fatalf("expected pause on high steal, got %+v", d)
	}
	if d.Reason != reasonHostCPUSteal {
		t.Fatalf("Reason = %q, want %q", d.Reason, reasonHostCPUSteal)
	}
}

// A class nobody used in the window must not be read as a class in trouble:
// zero attempts is no data, and no data is not a refusal rate of anything.
func TestGovernorDecideDoesNotShedOnAnIdleWindow(t *testing.T) {
	stats := serverstore.PoolStats{Classes: []serverstore.ClassPoolStats{
		{Class: "interactive", Busy: 0, Attempts: 0},
	}}
	if d := decide(stats, hostpressure.Reading{}, defaultGovernorThresholds()); d.PauseBuilder {
		t.Fatalf("an idle window shed background work: %+v", d)
	}
}

func TestPoolStatsWindowSubtractsTheSnapshotBefore(t *testing.T) {
	prev := serverstore.PoolStats{Classes: []serverstore.ClassPoolStats{
		{Class: "interactive", Attempts: 1000, Busy: 10, Timeouts: 3},
	}}
	cur := serverstore.PoolStats{Classes: []serverstore.ClassPoolStats{
		{Class: "interactive", Attempts: 1100, Busy: 30, Timeouts: 3, Limit: 6, InUse: 4},
	}}
	got := interactiveWindow(poolStatsWindow(prev, cur))
	if got.Attempts != 100 || got.Busy != 20 || got.Timeouts != 0 {
		t.Fatalf("window = %+v, want attempts 100 busy 20 timeouts 0", got)
	}
	// Point-in-time fields are not differences and are carried through.
	if got.Limit != 6 || got.InUse != 4 {
		t.Fatalf("window lost the current limit/in-use: %+v", got)
	}
	// A pool whose counters were reset contributes nothing rather than an
	// unsigned wraparound the size of the universe.
	reset := serverstore.PoolStats{Classes: []serverstore.ClassPoolStats{{Class: "interactive", Attempts: 5, Busy: 1}}}
	if got := interactiveWindow(poolStatsWindow(cur, reset)); got.Attempts != 0 || got.Busy != 0 {
		t.Fatalf("window across a counter reset = %+v, want zeroes", got)
	}
}

// ------------------------------------------------------- the loop's seams --

type fakePoolStats struct{ stats serverstore.PoolStats }

func (f *fakePoolStats) PoolStats() serverstore.PoolStats { return f.stats }

// set replaces the CUMULATIVE counters the pool would report, which is what
// the governor windows. Only the interactive class varies in these tests.
func (f *fakePoolStats) set(busy, attempts uint64) {
	f.stats = serverstore.PoolStats{Classes: []serverstore.ClassPoolStats{
		{Class: "interactive", Busy: busy, Attempts: attempts},
		{Class: "farm_ingest"},
	}}
}

type fakeHost struct {
	reading hostpressure.Reading
	err     error
	samples int
}

func (f *fakeHost) Sample() (hostpressure.Reading, error) {
	f.samples++
	return f.reading, f.err
}

type fakePauser struct {
	paused          bool
	pauses, resumes int
	pauseErr        error
	resumeErr       error
	// blockUntilDone models the control call this governor's own pool can
	// stall: Pause does not return until its context ends.
	blockUntilDone bool
	// budget is how much time the last call was given, and hadDeadline
	// whether it was given any at all.
	budget       time.Duration
	hadDeadline  bool
	sawNoTimeout bool
	// onCall runs at the top of Pause/Resume, before any blocking, so a test
	// can see what the rest of the governor had already done by the time the
	// control call began.
	onCall func()
}

func (f *fakePauser) observe(ctx context.Context) {
	if f.onCall != nil {
		f.onCall()
	}
	deadline, ok := ctx.Deadline()
	f.hadDeadline = ok
	if !ok {
		f.sawNoTimeout = true
		return
	}
	f.budget = time.Until(deadline)
}

func (f *fakePauser) Pause(ctx context.Context) error {
	f.pauses++
	f.observe(ctx)
	if f.blockUntilDone {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.pauseErr != nil {
		return f.pauseErr
	}
	f.paused = true
	return nil
}

func (f *fakePauser) Resume(ctx context.Context) error {
	f.resumes++
	f.observe(ctx)
	if f.blockUntilDone {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.resumeErr != nil {
		return f.resumeErr
	}
	f.paused = false
	return nil
}

type fakeFarmLimiter struct {
	set     []int
	ceiling int
}

func (f *fakeFarmLimiter) SetFarmIngestConns(n int) {
	f.set = append(f.set, n)
	f.ceiling = n
}

func (f *fakeFarmLimiter) FarmIngestConns() int { return f.ceiling }

func (f *fakeFarmLimiter) last() int {
	if len(f.set) == 0 {
		return -1
	}
	return f.set[len(f.set)-1]
}

func newTestGovernor(pool *fakePoolStats, host *fakeHost, pauser *fakePauser, farm *fakeFarmLimiter, lines *[]string) *governor {
	return &governor{
		pool:       pool,
		host:       host,
		builder:    pauser,
		farm:       farm,
		farmConns:  2,
		thresholds: defaultGovernorThresholds(),
		interval:   time.Millisecond,
		logf: func(format string, args ...any) {
			*lines = append(*lines, fmt.Sprintf(format, args...))
		},
		needResume: true,
	}
}

// #454's acceptance shape in miniature, with the database and /proc replaced
// by fakes: pressure pauses both producers, and a later window without it
// puts both back -- no restart, no operator, no second decision to make.
func TestGovernorPausesUnderPressureAndResumesWhenTheWindowGoesQuiet(t *testing.T) {
	pool := &fakePoolStats{}
	host := &fakeHost{err: hostpressure.ErrUnsupportedPlatform}
	pauser := &fakePauser{}
	farm := &fakeFarmLimiter{}
	var lines []string
	g := newTestGovernor(pool, host, pauser, farm, &lines)

	// The first tick only seeds the window; nothing has happened yet. It
	// does clear any pause a predecessor process left behind.
	pool.set(0, 0)
	if d := g.tick(context.Background()); d.PauseBuilder {
		t.Fatalf("the seeding tick paused on no data: %+v", d)
	}
	if pauser.resumes != 1 || pauser.paused {
		t.Fatalf("the first tick did not clear a possibly-stale pause: resumes=%d paused=%v", pauser.resumes, pauser.paused)
	}

	// 40 refusals out of 100 attempts since the last tick.
	pool.set(40, 100)
	d := g.tick(context.Background())
	if !d.PauseBuilder || !d.PauseFarmIngest || d.Reason != reasonInteractivePoolPressure {
		t.Fatalf("decision under pressure = %+v", d)
	}
	if !pauser.paused {
		t.Fatal("the Builder was not paused")
	}
	if farm.last() != 0 {
		t.Fatalf("farm ingest ceiling = %d, want 0", farm.last())
	}

	// The pause is re-asserted while it lasts, so it cannot expire under a
	// governor that still means it.
	pool.set(80, 200)
	g.tick(context.Background())
	if pauser.pauses < 2 {
		t.Fatalf("the pause was asserted %d time(s) across two pressured ticks", pauser.pauses)
	}

	// Pressure stops. The counters do not go down -- they never do -- so it
	// is the window, not the totals, that has to end the pause.
	pool.set(80, 400)
	d = g.tick(context.Background())
	if d.PauseBuilder || d.PauseFarmIngest {
		t.Fatalf("still paused after a quiet window: %+v", d)
	}
	if pauser.paused {
		t.Fatal("the Builder was never resumed")
	}
	if farm.last() != 2 {
		t.Fatalf("farm ingest ceiling after resume = %d, want the configured 2", farm.last())
	}
}

// One line per transition, not one per tick: during an incident the ticks
// all decide the same thing, and a line each would measure how long the
// incident lasted rather than what happened in it.
func TestGovernorLogsOnlyWhenTheDecisionChanges(t *testing.T) {
	pool := &fakePoolStats{}
	pauser := &fakePauser{}
	farm := &fakeFarmLimiter{}
	var lines []string
	g := newTestGovernor(pool, &fakeHost{}, pauser, farm, &lines)

	pool.set(0, 0)
	g.tick(context.Background())
	for i := 1; i <= 5; i++ {
		pool.set(uint64(50*i), uint64(100*i))
		g.tick(context.Background())
	}
	if len(lines) != 1 {
		t.Fatalf("five pressured ticks wrote %d lines: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], reasonInteractivePoolPressure) {
		t.Fatalf("the pause line does not name its reason: %q", lines[0])
	}

	for i := 0; i < 3; i++ {
		pool.set(250, uint64(600+100*i))
		g.tick(context.Background())
	}
	if len(lines) != 2 {
		t.Fatalf("three quiet ticks after a pause wrote %d lines in total: %v", len(lines), lines)
	}
	if !strings.Contains(lines[1], "resumed") {
		t.Fatalf("the second line is not the resume: %q", lines[1])
	}
}

// A host with no steal signal at all -- every non-Linux build, and any
// unreadable /proc -- must not be read as a healthy 0%, must not shed
// anything on its own account, and must not say so on every tick.
func TestGovernorTreatsAnUnreadableHostSignalAsNoSignal(t *testing.T) {
	pool := &fakePoolStats{}
	host := &fakeHost{
		// A reading that would trip the threshold if it were believed.
		reading: hostpressure.Reading{StealPercent: 99},
		err:     hostpressure.ErrUnsupportedPlatform,
	}
	var lines []string
	g := newTestGovernor(pool, host, &fakePauser{}, &fakeFarmLimiter{}, &lines)

	for i := 0; i < 4; i++ {
		pool.set(0, uint64(100*i))
		if d := g.tick(context.Background()); d.PauseBuilder {
			t.Fatalf("tick %d shed work on a host reading that came back with an error: %+v", i, d)
		}
	}
	if host.samples != 4 {
		t.Fatalf("host sampled %d times across 4 ticks", host.samples)
	}
	if len(lines) != 1 {
		t.Fatalf("the missing host signal was reported %d times: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "no host CPU signal") {
		t.Fatalf("unexpected line: %q", lines[0])
	}
}

// Real steal with a perfectly healthy pool: the governor still sheds
// background work -- it is the cheapest thing to give up, and it keeps the
// site answering -- but names the reason so the runbook sends the operator
// to the hypervisor rather than to the pool settings.
func TestGovernorShedsOnHostStealWithAHealthyPool(t *testing.T) {
	pool := &fakePoolStats{}
	host := &fakeHost{reading: hostpressure.Reading{StealPercent: 31, LoadAvg1: 4}}
	pauser := &fakePauser{}
	farm := &fakeFarmLimiter{}
	var lines []string
	g := newTestGovernor(pool, host, pauser, farm, &lines)

	pool.set(0, 0)
	g.tick(context.Background())
	pool.set(0, 500) // 500 acquisitions since the last tick, not one refused
	d := g.tick(context.Background())
	if d.Reason != reasonHostCPUSteal {
		t.Fatalf("reason = %q, want %q", d.Reason, reasonHostCPUSteal)
	}
	if !pauser.paused || farm.last() != 0 {
		t.Fatalf("host steal did not shed background work: paused=%v farm=%d", pauser.paused, farm.last())
	}
}

// A resume the database refused is owed, not forgotten: the governor keeps
// trying on later ticks rather than leaving the Builder paused because one
// round trip failed.
func TestGovernorRetriesAResumeThatFailed(t *testing.T) {
	pool := &fakePoolStats{}
	pauser := &fakePauser{resumeErr: errors.New("connection refused")}
	var lines []string
	g := newTestGovernor(pool, &fakeHost{}, pauser, &fakeFarmLimiter{}, &lines)

	pool.set(0, 0)
	g.tick(context.Background())
	g.tick(context.Background())
	if pauser.resumes != 2 {
		t.Fatalf("resume attempts = %d, want one per tick while it keeps failing", pauser.resumes)
	}
	pauser.resumeErr = nil
	g.tick(context.Background())
	if pauser.resumes != 3 || pauser.paused {
		t.Fatalf("resume did not land once the database came back: attempts=%d paused=%v", pauser.resumes, pauser.paused)
	}
	// And once it has landed it is not repeated forever.
	g.tick(context.Background())
	if pauser.resumes != 3 {
		t.Fatalf("resume was called again after it had succeeded: attempts=%d", pauser.resumes)
	}
	if len(lines) != 1 {
		t.Fatalf("the failing resume was reported %d times: %v", len(lines), lines)
	}
}

// The governor talks to the database over the pool it is reacting to, as
// ClassBackground -- which has no wait budget and no statement ceiling, and
// shares the general admission gate with the interactive reads that saturate
// during the incident this loop exists for. So its own control calls have to
// be bounded, or a single Pause can swallow the whole incident inside one
// tick.
func TestGovernorBoundsItsOwnControlCalls(t *testing.T) {
	pool := &fakePoolStats{}
	pauser := &fakePauser{}
	g := newTestGovernor(pool, &fakeHost{}, pauser, &fakeFarmLimiter{}, &[]string{})

	// A short interval is floored, so a control call always gets a budget a
	// healthy round trip can fit inside.
	pool.set(0, 0)
	g.tick(context.Background())
	if !pauser.hadDeadline {
		t.Fatal("the governor handed its control call the unbounded server context")
	}
	if pauser.budget > minGovernorControlTimeout || pauser.budget < minGovernorControlTimeout/2 {
		t.Fatalf("control budget = %v, want about the %v floor", pauser.budget, minGovernorControlTimeout)
	}

	// A production-sized interval is the budget: a call that has not landed
	// by the time the next decision is due has nothing left to add to this
	// one.
	g.interval = 30 * time.Second
	pool.set(50, 100)
	g.tick(context.Background())
	if pauser.budget > 30*time.Second || pauser.budget < 29*time.Second {
		t.Fatalf("control budget = %v, want it derived from the %v interval", pauser.budget, g.interval)
	}
	if pauser.sawNoTimeout {
		t.Fatal("at least one control call ran without a deadline")
	}
}

// The two levers are independent, and the order they are pulled in is what
// makes them so. Farm ingest is an atomic store in this process; the Builder
// pause is a round trip that can stall on the very saturation being shed. A
// stuck Pause must not take the Farm lever with it.
func TestGovernorShedsFarmIngestEvenWhenTheBuilderPauseIsStuck(t *testing.T) {
	pool := &fakePoolStats{}
	pauser := &fakePauser{blockUntilDone: true}
	farm := &fakeFarmLimiter{}
	g := newTestGovernor(pool, &fakeHost{}, pauser, farm, &[]string{})

	// What the Farm lever reads at the moment the control call begins -- not
	// afterwards, which a stuck call returning at its budget would also
	// satisfy. This is the ordering assertion: by the time the Builder pause
	// is entered, Farm has already been shed.
	farmWhenPauseBegan := -2
	pauser.onCall = func() { farmWhenPauseBegan = farm.last() }

	pool.set(0, 0)
	g.tick(context.Background()) // seed the window (and one stuck Resume)

	pool.set(50, 100)
	started := time.Now()
	d := g.tick(context.Background())
	elapsed := time.Since(started)

	if !d.PauseBuilder || !d.PauseFarmIngest {
		t.Fatalf("decision under pressure = %+v", d)
	}
	if farmWhenPauseBegan != 0 {
		t.Fatalf("farm ingest ceiling was %d when the Builder pause began; want it already shed (0), "+
			"or a Builder pause stuck on the saturation delays the shedding that saturation calls for",
			farmWhenPauseBegan)
	}
	if farm.last() != 0 {
		t.Fatalf("farm ingest ceiling = %d after the tick; want 0", farm.last())
	}
	// And the tick ended on its own budget rather than on the stuck call.
	if elapsed > 3*minGovernorControlTimeout {
		t.Fatalf("a tick with a stuck control call took %v; the budget is %v", elapsed, g.controlTimeout())
	}
}

// run must stop when its context does, and must not act after that.
func TestGovernorRunStopsWithItsContext(t *testing.T) {
	pool := &fakePoolStats{}
	pool.set(0, 0)
	g := newTestGovernor(pool, &fakeHost{}, &fakePauser{}, &fakeFarmLimiter{}, &[]string{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		g.run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the governor loop did not return after its context was cancelled")
	}
}

// Important #1's regression: what the governor puts back on resume is the
// pool's OWN live ceiling, captured before the loop ever moved it -- not the
// raw configured number.
//
// serverstore.PoolPolicy.normalize clamps an out-of-range FarmIngestConns up
// to the general share, and 0 is out of range: every sibling CSX_DB_* knob
// spells "none"/"disabled" as 0, so an operator writing CSX_DB_FARM_CONNS=0
// gets a pool running at 11, not a pool running at 0. A governor that
// restored the configuration instead would set the ceiling to 0 on the first
// tick that cleared -- permanently, since apply() only logs on a change of
// decision and the decision never changes again -- and Farm ingest would be
// silently, unrecoverably off.
func TestGovernorRestoresTheLiveCeilingNotTheConfiguredOne(t *testing.T) {
	const normalized = 11 // what normalize() makes of a configured 0
	pool := &fakePoolStats{}
	pauser := &fakePauser{}
	farm := &fakeFarmLimiter{ceiling: normalized}
	g := newGovernor(pool, &fakeHost{}, pauser, farm, time.Millisecond)

	if g.farmConns != normalized {
		t.Fatalf("captured ceiling = %d, want the pool's live %d", g.farmConns, normalized)
	}
	var lines []string
	g.logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	pool.set(0, 0)
	g.tick(context.Background())
	pool.set(50, 100)
	g.tick(context.Background())
	if farm.last() != 0 {
		t.Fatalf("farm ingest ceiling under pressure = %d, want 0", farm.last())
	}

	pool.set(50, 400) // a quiet window: no new refusals
	g.tick(context.Background())
	if farm.last() != normalized {
		t.Fatalf("farm ingest ceiling after resume = %d, want the pool's live %d; "+
			"restoring the unnormalized config would leave Farm ingest disabled for good", farm.last(), normalized)
	}
	if len(lines) != 2 || !strings.Contains(lines[1], fmt.Sprintf("farm_ingest=%d", normalized)) {
		t.Fatalf("the resume line does not report the restored ceiling: %v", lines)
	}
}

// A pause or resume the database refused is reported once per unbroken run
// of that failure, not once per process: a second incident an hour later
// with the same message is news again.
func TestGovernorReportsAControlFailureAgainAfterItRecovered(t *testing.T) {
	pool := &fakePoolStats{}
	pauser := &fakePauser{resumeErr: errors.New("connection refused")}
	var lines []string
	g := newTestGovernor(pool, &fakeHost{}, pauser, &fakeFarmLimiter{}, &lines)

	pool.set(0, 0)
	g.tick(context.Background()) // first resume fails and is reported
	pauser.resumeErr = nil
	g.tick(context.Background()) // it lands; the suppression must clear

	// A later incident, then a later recovery that fails the same way.
	pool.set(50, 100)
	g.tick(context.Background()) // paused
	pauser.resumeErr = errors.New("connection refused")
	pool.set(50, 400)
	g.tick(context.Background()) // resume fails again, same message

	// Four lines: the failure once per incident (two), plus the two
	// decision-transition lines. The second transition line is written even
	// though the Resume under it failed -- apply() reports the decision, not
	// the round trip, which is an accepted ruling from Task 6's review and
	// not what this test is about.
	if len(lines) != 4 {
		t.Fatalf("lines = %v; want the failure reported once per incident plus the two transition lines", lines)
	}
	if !strings.Contains(lines[0], "could not resume") || !strings.Contains(lines[2], "could not resume") {
		t.Fatalf("the second incident's control failure was swallowed: %v", lines)
	}
}
