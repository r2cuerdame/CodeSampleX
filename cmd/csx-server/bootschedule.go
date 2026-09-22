package main

// The boot schedule (#250): what csx-server does between exec and steady
// state, in what order, under what budget, and the record of how long each
// step actually took.
//
// Before this file, `serve` ran the schema migration, primed the wanted
// snapshot, started the Builder, and then -- with the Builder's first pass
// already consuming the pool -- ran four reconciles and the dedup purge
// serially, and only then bound the listener. Every one of those steps
// shared one process-lifetime context, so none had a budget of its own, and
// the dependency-atlas reconcile ran as a fourth concurrent lane beside the
// Builder, the purge and the mux's prewarm. On a starved database that
// ordering needed ten minutes to reach ListenAndServe, and the compose
// healthcheck (~135 s), the deploy smoke (120 s) and the exact-rollback loop
// (120 s) all failed closed against a process that was busy doing
// maintenance -- production 2026-09-08, v0.1.150, run 34243084351 -- and the
// v0.1.147 observer's 777% peak CPU, 143 pool_busy and 16.357 s maximum wait
// were measured in that same restart window (#174).
//
// The schedule this file enforces:
//
//  1. migrate            schema first; nothing below is valid against an old one
//  2. prime-wanted       the public wanted feed, read while the database is idle
//  3. build-mux          the handler, which starts its own bounded prewarm lanes
//  4. listen             /healthz answers from here on; the governor starts here
//  5. maintenance lane   ONE goroutine: stranded drafts, cross-job lanes,
//                        publicness, dependency atlas, dedup purge -- one at
//                        a time, each under its own budget, all under a total
//  6. builder            the in-process Builder starts only after the lane
//                        has finished or run out of budget
//
// The record of every boot is on GET /v1/ops/pool-metrics under "boot"
// (marks, phases, outcomes, per-phase pool pressure, what else was live),
// and in the log as one "csx-server: boot phase=" line per step plus a
// "csx-server: boot schedule" summary once the Builder decision is made.
//
// Two properties fall out of that order and are what the tests pin. At most
// one maintenance batch is ever in flight, and it is never in flight beside
// the Builder's first pass: the reconciles finish on an otherwise idle pool
// and the Builder gets the pool to itself afterwards. And the listener is
// bound before any of it, so a slow database costs the maintenance lane its
// budget rather than costing the deploy its health window.
//
// What the schedule does NOT change: which reconciles run, their row limits,
// or their per-boot backlog semantics. Every one of them was already written
// as "drain some of the backlog now, the rest next boot" (see the limits in
// main.go), so a phase that runs out of budget is the same outcome as a phase
// that hit its row limit -- less of the backlog drained this boot -- and not
// a lost guarantee.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/httpapi"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Phase outcomes. Part of the log/ops contract: docs/operations.md's boot
// schedule section tells an operator what each one means.
const (
	bootOutcomeOK             = "ok"
	bootOutcomeFailed         = "failed"
	bootOutcomeBudgetExceeded = "budget-exceeded"
	// bootOutcomeShutdown is a phase interrupted by the process's own
	// shutdown -- not a budget breach, not a failure, just a boot that did
	// not get to finish.
	bootOutcomeShutdown = "shutdown"
)

// Marks are the instants between phases. bootMarkListen and
// bootMarkBuilder are the two every phase is compared against: a phase that
// started after listen ran beside serving traffic, one that started after
// the builder mark ran beside the Builder.
const (
	bootMarkMigrated        = "migrated"
	bootMarkWantedPrimed    = "wanted-primed"
	bootMarkListen          = "listen"
	bootMarkMaintenanceDone = "maintenance-done"
	bootMarkBuilder         = "builder-started"
	// bootMarkBuilderStandalone records that the mode decision left the
	// Builder to cmd/csx-builder, so a timeline with no builder-started mark
	// reads as "standalone" rather than "never started".
	bootMarkBuilderStandalone = "builder-standalone"
)

// Lane names a phase can be recorded as concurrent with.
const (
	bootLaneServing = "serving"
	bootLaneBuilder = "builder"
)

// bootMaintenanceBudget is the explicit resource budget #250 asks for: how
// long each boot-time maintenance step may hold the lane, and how long the
// lane may hold up the Builder in total.
//
// The per-step numbers are generous against measured cost on an idle pool
// (each reconcile is seconds; publicness is the slow one because it goes to
// package registries over the network) and small against the Builder's
// snapshot interval (5 min), which is what the total is really bounding:
// the Builder starts at most Total after the listener is up, whatever the
// database is doing.
type bootMaintenanceBudget struct {
	StrandedDrafts  time.Duration
	CrossJobLanes   time.Duration
	Publicness      time.Duration
	DependencyAtlas time.Duration
	DedupPurge      time.Duration
	// Total caps the whole lane. A step that starts with less than its own
	// budget left in the total gets the remainder; a step that starts with
	// nothing left is recorded as budget-exceeded without running.
	Total time.Duration
}

func defaultBootMaintenanceBudget() bootMaintenanceBudget {
	return bootMaintenanceBudget{
		StrandedDrafts:  30 * time.Second,
		CrossJobLanes:   30 * time.Second,
		Publicness:      2 * time.Minute,
		DependencyAtlas: 2 * time.Minute,
		DedupPurge:      time.Minute,
		Total:           5 * time.Minute,
	}
}

func (b bootMaintenanceBudget) String() string {
	return fmt.Sprintf("stranded-drafts=%s cross-job-lanes=%s publicness=%s dependency-atlas=%s dedup-purge=%s total=%s",
		b.StrandedDrafts, b.CrossJobLanes, b.Publicness, b.DependencyAtlas, b.DedupPurge, b.Total)
}

// bootStep is one unit of the maintenance lane. Run returns a short human
// detail ("woke 12") for the log and the timeline, or an error.
type bootStep struct {
	Name   string
	Budget time.Duration
	Run    func(ctx context.Context) (detail string, err error)
}

// bootPhase is the record of one step, in process-relative time.
type bootPhase struct {
	Name      string
	Budget    time.Duration
	StartedAt time.Duration // since process start
	Took      time.Duration
	Outcome   string
	Detail    string
	// The pool's account of the phase, read from the phase's own
	// ClassBackground budget: what the step was refused, what it had killed,
	// and how long it stood in line. This is the per-phase correlation #250
	// asks for, taken from the same counters the pressure log reports.
	PoolBusy      int64
	QueryTimeouts int64
	PoolWaited    time.Duration
	// Concurrent names the lanes that were live when the phase started.
	Concurrent []string
}

// bootTimeline is the process's own record of its boot. One per process in
// production (bootRecord below); tests build their own.
type bootTimeline struct {
	now   func() time.Time
	start time.Time
	logf  func(format string, args ...any)

	mu     sync.Mutex
	phases []bootPhase
	marks  map[string]time.Duration
	// order keeps marks in the sequence they were set, so the ops view lists
	// them as they happened rather than alphabetically.
	order []string
}

func newBootTimeline(start time.Time) *bootTimeline {
	return &bootTimeline{
		now:   time.Now,
		start: start,
		logf:  log.Printf,
		marks: map[string]time.Duration{},
	}
}

// bootRecord is the live process's timeline. It starts with the process so
// that the offsets it reports are measured from exec, not from whenever
// serve happened to construct it.
var bootRecord = newBootTimeline(processStartedAt)

func (t *bootTimeline) since() time.Duration {
	return t.now().Sub(t.start)
}

// mark records an instant. Setting a mark twice keeps the first: a mark is
// an event, and an event happens once.
func (t *bootTimeline) mark(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.marks[name]; ok {
		return
	}
	t.marks[name] = t.since()
	t.order = append(t.order, name)
	t.logf("csx-server: boot mark=%s at=+%s", name, t.marks[name].Round(time.Millisecond))
}

func (t *bootTimeline) markAt(name string) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.marks[name]
	return d, ok
}

// concurrentLanes names what else was live at this instant.
func (t *bootTimeline) concurrentLanes() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var lanes []string
	if _, ok := t.marks[bootMarkListen]; ok {
		lanes = append(lanes, bootLaneServing)
	}
	if _, ok := t.marks[bootMarkBuilder]; ok {
		lanes = append(lanes, bootLaneBuilder)
	}
	return lanes
}

// run executes one step under its budget and records it.
//
// The step gets a context bounded by budget (when budget > 0) and its own
// ClassBackground query budget, so what the pool did to it can be read back
// afterwards. The returned error is the step's own; a budget breach is
// reported as context.DeadlineExceeded from the step and classified here.
func (t *bootTimeline) run(ctx context.Context, name string, budget time.Duration, step func(context.Context) (string, error)) error {
	concurrent := t.concurrentLanes()
	startedAt := t.since()
	started := t.now()

	stepCtx := ctx
	var cancel context.CancelFunc = func() {}
	if budget > 0 {
		stepCtx, cancel = context.WithTimeout(ctx, budget)
	}
	qb := serverstore.NewQueryBudget(serverstore.ClassBackground)
	stepCtx = serverstore.WithQueryBudget(stepCtx, qb)

	var detail string
	var err error
	if budget < 0 {
		// A negative budget is the lane saying "nothing left": recorded as
		// exceeded without running, so the log shows the step was skipped
		// for a reason rather than forgotten.
		err = context.DeadlineExceeded
	} else {
		detail, err = step(stepCtx)
	}
	cancel()

	took := t.now().Sub(started)
	outcome := bootOutcomeOK
	switch {
	case err == nil:
	case ctx.Err() != nil:
		outcome = bootOutcomeShutdown
	case budget < 0, errors.Is(stepCtx.Err(), context.DeadlineExceeded):
		// A step that errored after its own deadline passed ran out of
		// budget, whatever the driver wrapped the cancellation as.
		outcome = bootOutcomeBudgetExceeded
	default:
		outcome = bootOutcomeFailed
	}
	busy, timeouts, waited := qb.Pressure()
	phase := bootPhase{
		Name:          name,
		Budget:        budget,
		StartedAt:     startedAt,
		Took:          took,
		Outcome:       outcome,
		Detail:        detail,
		PoolBusy:      busy,
		QueryTimeouts: timeouts,
		PoolWaited:    waited,
		Concurrent:    concurrent,
	}
	if err != nil && outcome != bootOutcomeOK {
		if phase.Detail != "" {
			phase.Detail += "; "
		}
		phase.Detail += err.Error()
	}
	t.mu.Lock()
	t.phases = append(t.phases, phase)
	t.mu.Unlock()

	lanes := "none"
	if len(concurrent) > 0 {
		lanes = strings.Join(concurrent, "+")
	}
	t.logf("csx-server: boot phase=%s at=+%s took=%s budget=%s outcome=%s pool_busy=%d query_timeout=%d waited=%s concurrent=%s detail=%q",
		name, startedAt.Round(time.Millisecond), took.Round(time.Millisecond), budget, outcome,
		busy, timeouts, waited.Round(time.Millisecond), lanes, phase.Detail)
	return err
}

// snapshot is the ops view (httpapi.BootTimeline). Offsets are seconds
// because that is what the rest of /v1/ops/pool-metrics speaks.
func (t *bootTimeline) snapshot() httpapi.BootTimeline {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := httpapi.BootTimeline{
		StartedAt: t.start,
		Marks:     make([]httpapi.BootMark, 0, len(t.order)),
		Phases:    make([]httpapi.BootPhase, 0, len(t.phases)),
	}
	for _, name := range t.order {
		out.Marks = append(out.Marks, httpapi.BootMark{Name: name, AtSeconds: t.marks[name].Seconds()})
	}
	for _, p := range t.phases {
		concurrent := append([]string(nil), p.Concurrent...)
		if concurrent == nil {
			concurrent = []string{}
		}
		out.Phases = append(out.Phases, httpapi.BootPhase{
			Name:             p.Name,
			BudgetSeconds:    p.Budget.Seconds(),
			StartedAtSeconds: p.StartedAt.Seconds(),
			Seconds:          p.Took.Seconds(),
			Outcome:          p.Outcome,
			Detail:           p.Detail,
			PoolBusy:         p.PoolBusy,
			QueryTimeouts:    p.QueryTimeouts,
			PoolWaitSeconds:  p.PoolWaited.Seconds(),
			Concurrent:       concurrent,
		})
	}
	_, out.MaintenanceDone = t.marks[bootMarkMaintenanceDone]
	return out
}

// BootTimeline satisfies httpapi.BootTimelineSource.
func (t *bootTimeline) BootTimeline() httpapi.BootTimeline { return t.snapshot() }

// recorded returns the recorded phases in order, for tests.
func (t *bootTimeline) recorded() []bootPhase {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]bootPhase(nil), t.phases...)
}

// ---------------------------------------------------------- the schedule --

// bootSchedule owns the maintenance lane and the deferred Builder start.
type bootSchedule struct {
	tl     *bootTimeline
	budget bootMaintenanceBudget

	mu      sync.Mutex
	builder func() // set by deferBuilder; nil means standalone
	// done closes when the lane has finished and the Builder decision has
	// been acted on. Tests wait on it; production never needs to.
	done chan struct{}
}

func newBootSchedule(tl *bootTimeline, budget bootMaintenanceBudget) *bootSchedule {
	return &bootSchedule{tl: tl, budget: budget, done: make(chan struct{})}
}

// deferBuilder is the `start` primeWantedBeforeBuilder is handed. That
// function stays the one place the topology decision is made (and tested);
// what changes is that "start the Builder" now means "start it when the
// maintenance lane is done", which run below carries out.
func (s *bootSchedule) deferBuilder(ctx context.Context, cfg serverstore.ServerConfig, store serverstore.Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.builder = func() { StartBuilder(ctx, cfg, store) }
}

// run drives the maintenance lane to completion and then starts the Builder
// if the mode decision asked for one. It is one goroutine's work; steps run
// strictly in order and never concurrently with each other.
//
// The total budget is enforced by handing each step min(its budget, what is
// left of the total). A step that begins with nothing left is recorded as
// budget-exceeded and skipped. Whatever happens to the lane, the Builder
// start at the end is unconditional: a maintenance step failing must not
// leave the aggregation pipeline off.
func (s *bootSchedule) run(ctx context.Context, steps []bootStep) {
	defer close(s.done)
	laneStart := s.tl.now()
	for _, step := range steps {
		remaining := s.budget.Total - s.tl.now().Sub(laneStart)
		budget := step.Budget
		if s.budget.Total > 0 {
			switch {
			case remaining <= 0:
				budget = -1
			case budget <= 0 || budget > remaining:
				budget = remaining
			}
		}
		if ctx.Err() != nil {
			return
		}
		_ = s.tl.run(ctx, step.Name, budget, step.Run)
	}
	s.tl.mark(bootMarkMaintenanceDone)

	s.mu.Lock()
	builder := s.builder
	s.mu.Unlock()
	switch {
	case builder == nil:
		s.tl.mark(bootMarkBuilderStandalone)
	case ctx.Err() != nil:
		return
	default:
		s.tl.mark(bootMarkBuilder)
		builder()
	}
	s.tl.logf("%s", s.tl.summary())
}

// wait blocks until run has returned. Tests only.
func (s *bootSchedule) wait(ctx context.Context) bool {
	select {
	case <-s.done:
		return true
	case <-ctx.Done():
		return false
	}
}

// summary is one line an operator can read the whole boot from.
func (t *bootTimeline) summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	parts := make([]string, 0, len(t.order)+len(t.phases))
	for _, name := range t.order {
		parts = append(parts, fmt.Sprintf("%s=+%s", name, t.marks[name].Round(time.Millisecond)))
	}
	names := make([]string, 0, len(t.phases))
	for _, p := range t.phases {
		names = append(names, fmt.Sprintf("%s:%s/%s", p.Name, p.Outcome, p.Took.Round(time.Millisecond)))
	}
	return "csx-server: boot schedule " + strings.Join(parts, " ") + " phases=[" + strings.Join(names, " ") + "]"
}
