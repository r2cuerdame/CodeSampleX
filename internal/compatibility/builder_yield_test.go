package compatibility

// The builder must yield to interactive readers (#445). Production v0.1.195
// ran a full pass for three hours in snapshot_write while the interactive
// lanes refused 1.2 million acquisitions around it: background batches and
// interactive reads share the general connection gate, and a pass that never
// pauses keeps that gate full for as long as the pass lasts. These tests pin
// the contract: while interactive pressure climbs between batches the
// builder pauses for a bounded, escalating interval; when it stops climbing
// the builder runs flat out; and the total yielded is bounded by the number
// of batches, so a pass under constant pressure still finishes.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// yieldRecorder replaces the sleep with bookkeeping, so the test measures
// the pauses the builder asked for rather than waiting through them.
type yieldRecorder struct {
	pauses []time.Duration
}

func (y *yieldRecorder) wait(ctx context.Context, d time.Duration) bool {
	y.pauses = append(y.pauses, d)
	return ctx.Err() == nil
}

func seededYieldBuilder(t *testing.T) (*Builder, *serverstore.Fake, *yieldRecorder) {
	t.Helper()
	fake := serverstore.NewFake()
	seedBulkCorpus(t, fake, 40, 25)
	rec := &yieldRecorder{}
	b := &Builder{Store: fake, Now: func() time.Time { return testNow }, yieldWait: rec.wait}
	return b, fake, rec
}

// A pressure counter that climbs between every batch is a site refusing
// readers for the whole pass: the builder pauses after every batch, the
// pause escalates from the minimum to the cap and never beyond it, and the
// pass still completes.
func TestBuilderYieldsWhileInteractiveReadersAreRefused(t *testing.T) {
	b, fake, rec := seededYieldBuilder(t)
	var refused atomic.Uint64
	b.InteractivePressure = func() uint64 { return refused.Add(1) }

	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatalf("pass under pressure did not complete: %v", err)
	}
	if b.yields == 0 || len(rec.pauses) == 0 {
		t.Fatalf("builder never yielded under constant interactive pressure (yields=%d)", b.yields)
	}
	if got := rec.pauses[0]; got != yieldMinPause {
		t.Errorf("first pause = %s, want the minimum %s", got, yieldMinPause)
	}
	for i := 1; i < len(rec.pauses); i++ {
		prev, cur := rec.pauses[i-1], rec.pauses[i]
		if cur > yieldMaxPause {
			t.Fatalf("pause %d = %s exceeds the cap %s", i, cur, yieldMaxPause)
		}
		if cur < prev {
			t.Fatalf("pause %d = %s shrank from %s while pressure persisted", i, cur, prev)
		}
	}
	if b.yieldedTotal > time.Duration(b.yields)*yieldMaxPause {
		t.Errorf("yielded %s over %d yields exceeds the per-batch bound", b.yieldedTotal, b.yields)
	}
	// The pass published its outputs despite yielding.
	if rows, err := fake.ListSnapshots(context.Background()); err != nil || len(rows) == 0 {
		t.Fatalf("pass under pressure published no snapshots (err=%v)", err)
	}
}

// A flat counter is an idle site: the builder must not pause at all.
func TestBuilderDoesNotYieldWithoutInteractivePressure(t *testing.T) {
	b, _, rec := seededYieldBuilder(t)
	b.InteractivePressure = func() uint64 { return 7 }

	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if b.yields != 0 || len(rec.pauses) != 0 || b.yieldedTotal != 0 {
		t.Fatalf("builder yielded %d times (%s) with no interactive pressure", b.yields, b.yieldedTotal)
	}
}

// Pressure that stops resets the escalation: the next pressured batch starts
// again from the minimum rather than from wherever the last burst ended.
func TestBuilderYieldEscalationResetsWhenPressureClears(t *testing.T) {
	b := &Builder{Store: serverstore.NewFake()}
	rec := &yieldRecorder{}
	b.yieldWait = rec.wait
	var counter uint64
	b.InteractivePressure = func() uint64 { return counter }
	b.seedYield()

	ctx := context.Background()
	for i := 0; i < 5; i++ { // five pressured batches escalate
		counter++
		b.yield(ctx)
	}
	b.yield(ctx) // one quiet batch
	counter++
	b.yield(ctx) // pressure resumes

	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second, 250 * time.Millisecond}
	if len(rec.pauses) != len(want) {
		t.Fatalf("pauses = %v, want %v", rec.pauses, want)
	}
	for i := range want {
		if rec.pauses[i] != want[i] {
			t.Fatalf("pauses = %v, want %v", rec.pauses, want)
		}
	}
}

// A store that does not expose its pool cannot report pressure, and the
// builder must run exactly as before rather than fail or stall.
func TestBuilderWithoutPoolStatsNeverYields(t *testing.T) {
	b := &Builder{Store: serverstore.NewFake()}
	b.seedYield()
	if b.InteractivePressure != nil || b.yieldSeeded {
		t.Fatalf("a store without PoolStats produced a pressure source")
	}
	if !b.yield(context.Background()) || b.yields != 0 {
		t.Fatalf("yield without a source paused or failed")
	}
}

// The derived source counts only the interactive class, and every way an
// interactive acquisition can be denied.
func TestInteractivePressureIsDerivedFromInteractivePoolStats(t *testing.T) {
	stats := serverstore.PoolStats{Classes: []serverstore.ClassPoolStats{
		{Class: "background", Busy: 100, Waited: 100},
		{Class: "interactive", Busy: 3, Waited: 4, Suppressed: 5, Timeouts: 6, Acquired: 1000},
		{Class: "probe", Busy: 50},
	}}
	src := interactivePressureOf(poolStatsStore{Fake: serverstore.NewFake(), stats: stats})
	if src == nil {
		t.Fatal("a store exposing PoolStats produced no pressure source")
	}
	if got := src(); got != 18 {
		t.Fatalf("interactive pressure = %d, want 18 (busy+waited+suppressed+timeouts)", got)
	}
}

type poolStatsStore struct {
	*serverstore.Fake
	stats serverstore.PoolStats
}

func (p poolStatsStore) PoolStats() serverstore.PoolStats { return p.stats }
