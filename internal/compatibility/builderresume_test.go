package compatibility

// A restart must not throw away a pass that already finished.
//
// RunOnce takes a full pass whenever lastRun is zero, and lastRun lives only
// in the process. So every restart -- and a deploy is a restart -- rebuilds
// the whole corpus from nothing, however recently the previous process
// finished doing exactly that.
//
// Measured on production 2026-09-02. The container was recreated at 02:49:25,
// four minutes after a completed pass had written generatedAt=02:45:57Z. The
// new process started a full pass anyway and had not finished it 104 minutes
// later:
//
//	generatedAt : 2026-09-02T02:45:57Z
//	now         : 2026-09-02T04:33:07Z
//	failure_clusters writes: +1406 in 60s   (still going, not wedged)
//	shards, compatibility_snapshots, stats_daily: 0 writes
//
// The cost is not only the wasted work. RunLoop calls the first RunOnce
// SYNCHRONOUSLY and starts its ticker only after that call returns, so while
// the first pass runs there are no five-minute ticks at all -- and stats,
// which carry the clock the website displays, are written at the end of a
// pass. The site's generatedAt was therefore frozen for the entire time, on
// a two-vCPU host that was also serving the traffic it competes with.
//
// The fix is to start from what the last process durably recorded. A pass
// writes generatedAt when it completes, so that timestamp already IS the
// "last run" marker; it simply was not read back. Seeding lastRun from it
// makes the pass after a restart incremental, which is what the same process
// would have done had it never been restarted.
//
// This makes no pass faster. It stops a restart from demanding the slowest
// one for no reason.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A completed pass is durable, so the pass after a restart is incremental.
func TestARestartResumesFromTheLastCompletedPass(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	seedBuilderFixture(t, store)

	// The process that ran before the restart, which finishes a full pass.
	first := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := first.RunOnce(ctx); err != nil {
		t.Fatalf("pre-restart pass: %v", err)
	}
	if store.ShardWrites() == 0 {
		t.Fatal("the pre-restart pass wrote no shards, so there is no completed pass to resume from")
	}

	// The restart. A brand new Builder over the same store, exactly as a
	// redeployed container gets, with nothing carried over in memory.
	restarted := &Builder{Store: store, Now: func() time.Time { return testNow.Add(4 * time.Minute) }}

	// Nothing changed in those four minutes.
	changedSinceCalled := false
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		changedSinceCalled = true
		return serverstore.Changes{}, nil
	}

	before := store.ShardWrites()
	if err := restarted.RunOnce(ctx); err != nil {
		t.Fatalf("post-restart pass: %v", err)
	}

	if !changedSinceCalled {
		t.Error("the pass after a restart did not ask what changed; it took a full pass instead")
	}
	if wrote := store.ShardWrites() - before; wrote != 0 {
		t.Errorf("the pass after a restart rewrote %d shards; nothing had changed", wrote)
	}
}

// The clock still advances on that pass, which is the whole point: the
// website's generatedAt must not stay frozen at the pre-restart value.
func TestTheClockAdvancesOnThePassAfterARestart(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	seedBuilderFixture(t, store)

	first := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := first.RunOnce(ctx); err != nil {
		t.Fatalf("pre-restart pass: %v", err)
	}
	beforeStamp := generatedAtOf(t, store)

	later := testNow.Add(4 * time.Minute)
	restarted := &Builder{Store: store, Now: func() time.Time { return later }}
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		return serverstore.Changes{}, nil
	}
	if err := restarted.RunOnce(ctx); err != nil {
		t.Fatalf("post-restart pass: %v", err)
	}

	if after := generatedAtOf(t, store); after == beforeStamp {
		t.Errorf("generatedAt is still %s after the post-restart pass; the site's clock did not move",
			beforeStamp)
	}
}

// With no completed pass on record there is nothing to resume from, and the
// first pass of a new deployment must still build everything.
func TestAnEmptyStoreStillTakesAFullPass(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	seedBuilderFixture(t, store)

	// If this were treated as a resume, ChangedSince would decide the work
	// and an empty answer would leave the corpus permanently unbuilt.
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		return serverstore.Changes{}, nil
	}

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if store.ShardWrites() == 0 {
		t.Error("the first pass on an empty store wrote no shards")
	}
}

func generatedAtOf(t *testing.T, store *serverstore.Fake) string {
	t.Helper()
	js, ok, err := store.GetLatestStats(context.Background())
	if err != nil || !ok {
		t.Fatalf("no stats on record: ok=%v err=%v", ok, err)
	}
	var doc struct {
		GeneratedAt string `json:"generatedAt"`
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.GeneratedAt == "" {
		t.Fatal("stats carry no generatedAt")
	}
	return doc.GeneratedAt
}

// A pass recorded long ago is not resumed from.
//
// Past resumeWindow the incremental path would be handed everything that
// changed over hours, which is a full rebuild taken by the slower route and
// with more ways to be wrong. The full pass is the honest answer there, and
// a gap that long means one was due anyway.
func TestAStaleRecordedPassIsNotResumedFrom(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	seedBuilderFixture(t, store)

	first := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := first.RunOnce(ctx); err != nil {
		t.Fatalf("pre-restart pass: %v", err)
	}

	// Well past the window, so the recorded stamp must be ignored.
	long := testNow.Add(resumeWindow + time.Minute)
	restarted := &Builder{Store: store, Now: func() time.Time { return long }}
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		t.Error("a stale recorded pass was resumed from; ChangedSince decided the work")
		return serverstore.Changes{}, nil
	}

	before := store.ShardWrites()
	if err := restarted.RunOnce(ctx); err != nil {
		t.Fatalf("post-gap pass: %v", err)
	}
	if store.ShardWrites() == before {
		t.Error("the pass after a long gap rebuilt nothing")
	}
}

// A stamp from the future is not trusted either. Clocks disagree, and a
// lastRun ahead of now would make ChangedSince ask for a negative window --
// silently building nothing, forever, which is the worst of the outcomes
// available here.
func TestAFutureStampIsNotResumedFrom(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	seedBuilderFixture(t, store)

	first := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := first.RunOnce(ctx); err != nil {
		t.Fatalf("pre-restart pass: %v", err)
	}

	// The recorded pass is stamped after the restarted process's clock.
	earlier := testNow.Add(-10 * time.Minute)
	restarted := &Builder{Store: store, Now: func() time.Time { return earlier }}
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		t.Error("a future stamp was resumed from")
		return serverstore.Changes{}, nil
	}

	before := store.ShardWrites()
	if err := restarted.RunOnce(ctx); err != nil {
		t.Fatalf("pass with a future stamp: %v", err)
	}
	if store.ShardWrites() == before {
		t.Error("the pass rebuilt nothing")
	}
}
