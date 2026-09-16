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
}

func (f *fakePauser) Pause(context.Context) error {
	f.pauses++
	if f.pauseErr != nil {
		return f.pauseErr
	}
	f.paused = true
	return nil
}

func (f *fakePauser) Resume(context.Context) error {
	f.resumes++
	if f.resumeErr != nil {
		return f.resumeErr
	}
	f.paused = false
	return nil
}

type fakeFarmLimiter struct{ set []int }

func (f *fakeFarmLimiter) SetFarmIngestConns(n int) { f.set = append(f.set, n) }

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
