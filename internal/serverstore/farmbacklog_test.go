package serverstore

import (
	"context"
	"testing"
	"time"
)

// backlogStore is the slice of a store the backlog parity check needs.
type backlogStore interface {
	expansionStore
	FarmBacklogNow(context.Context, time.Time, time.Time) (FarmBacklog, error)
}

// The panel exists to answer "how much is left", and the queue exists to work
// through it. If they read different sets, the number is worse than nothing:
// it would sit still while the fleet drains work, or reach zero while jobs are
// still going out. So the two stores are compared on identical input, and the
// dependency figure is compared against the queue's own definition.
func TestIntegrationFarmBacklogFakeMatchesPostgres(t *testing.T) {
	steps := []depStep{
		// A chosen package with two unobserved dependencies.
		{name: "express", version: "5.1.0", direct: true, bucket: "p1", count: 9,
			children: []string{"body-parser@2.2.0", "router@2.2.0"}},
		// A second project resolving one of the same dependencies: canonical,
		// so it is one coordinate with two project-days behind it.
		{name: "koa", version: "3.0.1", direct: true, bucket: "p2", count: 7,
			children: []string{"body-parser@2.2.0"}},
		// A package nobody chose: its child must not enter the backlog.
		{name: "shadow", version: "1.0.0", direct: false, bucket: "p3", count: 5,
			children: []string{"deep@1.0.0"}},
		// A proven coordinate: off the hole backlog, and an anchor for the
		// next level of the closure.
		{name: "left-pad", version: "1.3.0", direct: true, bucket: "p4", count: 3},
		{provenPURL: "pkg:npm/left-pad@1.3.0", provenOS: "linux"},
	}
	ctx := context.Background()
	fake := NewFake()
	replayDepSteps(t, fake, steps)
	pg := openTestPG(t)
	replayDepSteps(t, pg, steps)

	// A window wide enough to hold everything the replay just wrote, on both
	// clocks.
	since := time.Now().UTC().Add(-time.Hour)
	until := time.Now().UTC().Add(time.Hour)

	fakeBacklog, err := fake.FarmBacklogNow(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	pgBacklog, err := pg.FarmBacklogNow(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if fakeBacklog.CoverageHoles != pgBacklog.CoverageHoles {
		t.Errorf("coverage holes: fake=%d pg=%d", fakeBacklog.CoverageHoles, pgBacklog.CoverageHoles)
	}
	if fakeBacklog.Dependencies != pgBacklog.Dependencies {
		t.Errorf("dependency backlog: fake=%d pg=%d", fakeBacklog.Dependencies, pgBacklog.Dependencies)
	}
	if fakeBacklog.FirstProven != pgBacklog.FirstProven {
		t.Errorf("first proven: fake=%d pg=%d", fakeBacklog.FirstProven, pgBacklog.FirstProven)
	}

	// body-parser@2.2.0 and router@2.2.0, and nothing from the shadow.
	if pgBacklog.Dependencies != 2 {
		t.Errorf("dependency backlog = %d, want 2 (body-parser, router)", pgBacklog.Dependencies)
	}
	// express, koa and shadow are observed and unproven; left-pad is proven.
	if pgBacklog.CoverageHoles != 3 {
		t.Errorf("coverage holes = %d, want 3 (express, koa, shadow)", pgBacklog.CoverageHoles)
	}
	if pgBacklog.FirstProven != 1 {
		t.Errorf("first proven = %d, want 1 (left-pad)", pgBacklog.FirstProven)
	}

	// The backlog must agree with the queue that drains it.
	for _, store := range []backlogStore{fake, pg} {
		rows, err := store.ListAuthoringExpansionCandidates(ctx, 200)
		if err != nil {
			t.Fatal(err)
		}
		offered := 0
		for _, r := range rows {
			if r.Kind == "DEPENDENCY" {
				offered++
			}
		}
		backlog, err := store.FarmBacklogNow(ctx, since, until)
		if err != nil {
			t.Fatal(err)
		}
		if offered != backlog.Dependencies {
			t.Errorf("the queue offers %d dependency coordinates and the panel counts %d", offered, backlog.Dependencies)
		}
	}
}

// The generation rate is read from what was actually claimed, so it survives a
// restart -- and it is split by queue source, because "the fleet is busy" says
// nothing about whether it is busy on demand or on breadth.
func TestFarmBacklogCountsWorkHandedOutByKind(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }
	if err := f.IssueAuthoringSessions(ctx, []AuthoringSessionRow{{
		TokenHash: "hash-flow", SessionID: "flow", Label: "flow", Model: "agy",
		Reasoning: "auto", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.ClaimAuthoringWork(ctx, "flow", []WantedRow{{
		Ecosystem: "npm", Name: "body-parser", Version: "2.2.0", Kind: "DEPENDENCY", Score: 2,
	}}, now, now.Add(24*time.Hour)); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	backlog, err := f.FarmBacklogNow(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if backlog.ClaimedByKind["DEPENDENCY"] != 1 {
		t.Errorf("claimed by kind = %v, want one DEPENDENCY", backlog.ClaimedByKind)
	}

	// Outside the window it is not this hour's rate any more.
	later, err := f.FarmBacklogNow(ctx, now.Add(time.Hour), now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(later.ClaimedByKind) != 0 {
		t.Errorf("claimed by kind outside the window = %v, want none", later.ClaimedByKind)
	}
}
