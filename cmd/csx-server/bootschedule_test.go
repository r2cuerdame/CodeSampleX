package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// fakeClock is a settable clock for the timeline: tests advance it inside
// steps to simulate work that took time, without sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestTimeline(t *testing.T) (*bootTimeline, *fakeClock, *[]string) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)}
	var lines []string
	var mu sync.Mutex
	tl := newBootTimeline(clock.now)
	tl.now = clock.Now
	tl.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	return tl, clock, &lines
}

// The schedule's first invariant: steps run one at a time, in order, and the
// Builder starts only after the last one -- never beside any of them.
func TestBootScheduleRunsStepsSeriallyThenStartsBuilder(t *testing.T) {
	tl, clock, _ := newTestTimeline(t)
	tl.mark(bootMarkListen)
	sched := newBootSchedule(tl, defaultBootMaintenanceBudget())

	var inFlight, maxInFlight atomic.Int32
	var order []string
	var mu sync.Mutex
	step := func(name string) bootStep {
		return bootStep{Name: name, Budget: time.Minute, Run: func(ctx context.Context) (string, error) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				cur := maxInFlight.Load()
				if n <= cur || maxInFlight.CompareAndSwap(cur, n) {
					break
				}
			}
			clock.Advance(time.Second)
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return "did " + name, nil
		}}
	}
	builderStarted := make(chan struct{})
	sched.mu.Lock()
	sched.builder = func() {
		if _, done := tl.markAt(bootMarkMaintenanceDone); !done {
			t.Error("builder started before the maintenance lane was done")
		}
		if got := inFlight.Load(); got != 0 {
			t.Errorf("builder started with %d maintenance steps in flight", got)
		}
		close(builderStarted)
	}
	sched.mu.Unlock()

	go sched.run(context.Background(), []bootStep{step("a"), step("b"), step("c")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !sched.wait(ctx) {
		t.Fatal("schedule never finished")
	}
	select {
	case <-builderStarted:
	default:
		t.Fatal("builder was never started")
	}
	if got := maxInFlight.Load(); got != 1 {
		t.Fatalf("max steps in flight = %d, want 1", got)
	}
	if strings.Join(order, ",") != "a,b,c" {
		t.Fatalf("order = %v, want a,b,c", order)
	}
	phases := tl.recorded()
	if len(phases) != 3 {
		t.Fatalf("recorded %d phases, want 3: %+v", len(phases), phases)
	}
	for i, p := range phases {
		if p.Outcome != bootOutcomeOK {
			t.Errorf("phase %s outcome = %s, want ok", p.Name, p.Outcome)
		}
		if p.Took != time.Second {
			t.Errorf("phase %s took %s, want 1s", p.Name, p.Took)
		}
		// Every step ran beside serving (listen was marked first) and
		// beside nothing else: "builder" must never appear.
		if strings.Join(p.Concurrent, "+") != bootLaneServing {
			t.Errorf("phase %s concurrent = %v, want [serving]", p.Name, p.Concurrent)
		}
		if p.Detail != "did "+p.Name {
			t.Errorf("phase %s detail = %q", p.Name, p.Detail)
		}
		if i > 0 && p.StartedAt < phases[i-1].StartedAt+phases[i-1].Took {
			t.Errorf("phase %s started at %s, before %s finished at %s", p.Name, p.StartedAt, phases[i-1].Name, phases[i-1].StartedAt+phases[i-1].Took)
		}
	}
	snap := tl.snapshot()
	if !snap.MaintenanceDone {
		t.Error("snapshot.MaintenanceDone = false after the lane finished")
	}
	var names []string
	for _, m := range snap.Marks {
		names = append(names, m.Name)
	}
	if got := strings.Join(names, ","); got != "listen,maintenance-done,builder-started" {
		t.Fatalf("marks = %s", got)
	}
}

// The second invariant: the total budget is a ceiling on how long the lane
// may hold the Builder back. A step that starts with nothing left is
// recorded as budget-exceeded WITHOUT running, and the Builder still starts.
func TestBootScheduleTotalBudgetSkipsLateStepsAndStillStartsBuilder(t *testing.T) {
	tl, clock, _ := newTestTimeline(t)
	budget := defaultBootMaintenanceBudget()
	budget.Total = 90 * time.Second
	sched := newBootSchedule(tl, budget)

	var ran []string
	slow := func(name string, took time.Duration) bootStep {
		return bootStep{Name: name, Budget: time.Minute, Run: func(ctx context.Context) (string, error) {
			ran = append(ran, name)
			clock.Advance(took)
			return "", nil
		}}
	}
	builderStarted := false
	sched.mu.Lock()
	sched.builder = func() { builderStarted = true }
	sched.mu.Unlock()

	// a takes 60 s of a 90 s total, so b gets the 30 s remainder; c starts
	// with nothing left and must not run at all.
	sched.run(context.Background(), []bootStep{slow("a", 60*time.Second), slow("b", 30*time.Second), slow("c", time.Second)})

	if strings.Join(ran, ",") != "a,b" {
		t.Fatalf("steps run = %v, want a,b (c skipped for budget)", ran)
	}
	phases := tl.recorded()
	if len(phases) != 3 {
		t.Fatalf("recorded %d phases, want 3", len(phases))
	}
	if phases[1].Budget != 30*time.Second {
		t.Errorf("b budget = %s, want the 30s remainder of the total", phases[1].Budget)
	}
	if phases[2].Outcome != bootOutcomeBudgetExceeded || phases[2].Took != 0 {
		t.Errorf("c = %+v, want budget-exceeded with took=0", phases[2])
	}
	if !builderStarted {
		t.Fatal("builder did not start after the lane ran out of budget")
	}
}

// A step whose own budget elapses is cut off by its context and recorded as
// budget-exceeded; a step that fails on its own is recorded as failed; and
// neither stops the lane or the Builder.
func TestBootScheduleClassifiesStepOutcomes(t *testing.T) {
	tl, _, lines := newTestTimeline(t)
	budget := defaultBootMaintenanceBudget()
	sched := newBootSchedule(tl, budget)
	sched.mu.Lock()
	builderStarted := false
	sched.builder = func() { builderStarted = true }
	sched.mu.Unlock()

	steps := []bootStep{
		{Name: "over", Budget: 20 * time.Millisecond, Run: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			// Wrapped the way a driver would wrap it, not the bare error.
			return "", fmt.Errorf("query cancelled: %w", ctx.Err())
		}},
		{Name: "broken", Budget: time.Second, Run: func(ctx context.Context) (string, error) {
			return "", errors.New("relation does not exist")
		}},
		{Name: "fine", Budget: time.Second, Run: func(ctx context.Context) (string, error) {
			return "ok", nil
		}},
	}
	sched.run(context.Background(), steps)

	phases := tl.recorded()
	want := map[string]string{"over": bootOutcomeBudgetExceeded, "broken": bootOutcomeFailed, "fine": bootOutcomeOK}
	for _, p := range phases {
		if p.Outcome != want[p.Name] {
			t.Errorf("phase %s outcome = %s, want %s (detail %q)", p.Name, p.Outcome, want[p.Name], p.Detail)
		}
	}
	if !builderStarted {
		t.Fatal("builder did not start after a failed step")
	}
	var sawSummary bool
	for _, l := range *lines {
		if strings.HasPrefix(l, "csx-server: boot schedule ") && strings.Contains(l, "over:budget-exceeded") && strings.Contains(l, "broken:failed") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Fatalf("no summary line naming both outcomes; log was:\n%s", strings.Join(*lines, "\n"))
	}
}

// Shutdown during the lane is neither a failure nor a budget breach, and the
// Builder must not be started into a context that is already gone.
func TestBootScheduleShutdownStopsLaneAndBuilder(t *testing.T) {
	tl, _, _ := newTestTimeline(t)
	sched := newBootSchedule(tl, defaultBootMaintenanceBudget())
	ctx, cancel := context.WithCancel(context.Background())
	builderStarted := false
	sched.mu.Lock()
	sched.builder = func() { builderStarted = true }
	sched.mu.Unlock()

	var secondRan bool
	sched.run(ctx, []bootStep{
		{Name: "first", Budget: time.Second, Run: func(ctx context.Context) (string, error) {
			cancel()
			<-ctx.Done()
			return "", ctx.Err()
		}},
		{Name: "second", Budget: time.Second, Run: func(ctx context.Context) (string, error) {
			secondRan = true
			return "", nil
		}},
	})
	phases := tl.recorded()
	if len(phases) != 1 || phases[0].Outcome != bootOutcomeShutdown {
		t.Fatalf("phases = %+v, want exactly [first:shutdown]", phases)
	}
	if secondRan {
		t.Error("second step ran after shutdown")
	}
	if builderStarted {
		t.Error("builder started after shutdown")
	}
}

// Standalone mode: primeWantedBeforeBuilder never hands the schedule a
// Builder, and the timeline says so explicitly rather than showing a
// builder-started mark that never comes.
func TestBootScheduleRecordsStandaloneBuilder(t *testing.T) {
	tl, _, _ := newTestTimeline(t)
	sched := newBootSchedule(tl, defaultBootMaintenanceBudget())
	store := serverstore.NewFake()
	cfg := serverstore.ServerConfig{BuilderMode: serverstore.BuilderModeStandalone}
	if _, err := primeWantedBeforeBuilder(context.Background(), cfg, store, sched.deferBuilder); err != nil {
		t.Fatal(err)
	}
	sched.run(context.Background(), nil)
	if _, ok := tl.markAt(bootMarkBuilderStandalone); !ok {
		t.Fatal("no builder-standalone mark")
	}
	if _, ok := tl.markAt(bootMarkBuilder); ok {
		t.Fatal("builder-started mark set in standalone mode")
	}
}

// The phase's pool account comes from its own ClassBackground budget: what
// the step was refused or had cancelled is read back per phase.
func TestBootTimelineRecordsPerPhasePoolPressure(t *testing.T) {
	tl, _, _ := newTestTimeline(t)
	err := tl.run(context.Background(), "probe", time.Second, func(ctx context.Context) (string, error) {
		b := serverstore.BudgetOf(ctx)
		if b == nil {
			return "", errors.New("no query budget on the step context")
		}
		if b.Class() != serverstore.ClassBackground {
			return "", fmt.Errorf("class = %v, want background", b.Class())
		}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A mark happens once: a second call is not a second event.
func TestBootTimelineMarkIsIdempotent(t *testing.T) {
	tl, clock, _ := newTestTimeline(t)
	clock.Advance(time.Second)
	tl.mark(bootMarkListen)
	clock.Advance(time.Second)
	tl.mark(bootMarkListen)
	at, ok := tl.markAt(bootMarkListen)
	if !ok || at != time.Second {
		t.Fatalf("listen mark = %s, %v; want 1s, true", at, ok)
	}
	if got := tl.snapshot().Marks; len(got) != 1 {
		t.Fatalf("marks = %+v, want one", got)
	}
}
