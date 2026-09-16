package serverstore

// Real-PostgreSQL proof of the builder lease's three guarantees (CSX-451):
// mutual exclusion between two owners, TTL-driven recovery from a crashed
// owner that never releases, and the fencing token that stops a deposed
// owner's delayed renew from resurrecting a lease someone else already took.
//
// The in-memory Fake mirrors this same CAS logic for fast control-flow
// tests (internal/compatibility/leader_test.go). What only real PostgreSQL
// proves is that two independent connections -- the shape two standalone
// Builder processes actually have -- agree on one row under concurrent
// UPDATE ... WHERE rather than on two copies of Go state.
//
//	$env:CSX_TEST_DSN = "postgres://csx:csx@localhost:5432/csx"
//	go test ./internal/serverstore/ -run TestIntegrationBuilderLease -v

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIntegrationBuilderLeaseIsMutuallyExclusiveBetweenTwoOwners(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	st, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if st.Owner != "owner-a" || st.Fence != 1 {
		t.Fatalf("first acquire state = %+v, want owner-a fence 1", st)
	}

	if _, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-b", time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second owner acquired an unexpired lease held by another owner: err=%v", err)
	}

	// The same owner may re-acquire (a process restarting its renew loop
	// after a transient error, still well inside the old TTL) without
	// waiting the lease out, and doing so advances the fence.
	again, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("owner-a re-acquire: %v", err)
	}
	if again.Fence != 2 {
		t.Fatalf("owner-a re-acquire fence = %d, want 2", again.Fence)
	}
}

func TestIntegrationBuilderLeaseRenewExtendsExpiryAndSurvivesPastTheOriginalTTL(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	st, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-a", 2*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	time.Sleep(1200 * time.Millisecond)
	renewed, err := pg.RenewBuilderLease(ctx, "compatibility-builder", "owner-a", st.Fence, 5*time.Second)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed.ExpiresAt.After(st.ExpiresAt) {
		t.Fatalf("renew did not extend expiry: before=%v after=%v", st.ExpiresAt, renewed.ExpiresAt)
	}

	// Past the ORIGINAL 2s TTL, but the renew bought 5 more seconds from
	// when it ran: a second owner must still be refused.
	time.Sleep(1200 * time.Millisecond)
	if _, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-b", time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("owner-b acquired a lease the renew had extended: err=%v", err)
	}
}

// This is the no-permanently-stuck-lock guarantee: an owner that crashes
// outright -- no release, no more renews -- stops blocking everyone else
// once its TTL passes.
func TestIntegrationBuilderLeaseExpiresAndRecoversAfterAnOwnerStopsRenewing(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	if _, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-crashed", 800*time.Millisecond); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if _, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-b", time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("owner-b acquired before the crashed owner's lease expired: err=%v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var recovered BuilderLeaseState
	var lastErr error
	for time.Now().Before(deadline) {
		recovered, lastErr = pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-b", time.Minute)
		if lastErr == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("lease was never recovered after the owning process stopped renewing: %v", lastErr)
	}
	if recovered.Owner != "owner-b" {
		t.Fatalf("recovered lease owner = %q, want owner-b", recovered.Owner)
	}
}

// The fencing token: a deposed owner's renew, delayed until after someone
// else has taken the lease over, must fail rather than resurrect a lease
// that has already moved on.
func TestIntegrationBuilderLeaseFencingRejectsAStaleRenewAfterTakeover(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	st, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-a", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	takeover, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-b", time.Minute)
	if err != nil {
		t.Fatalf("owner-b takeover: %v", err)
	}
	if takeover.Fence == st.Fence {
		t.Fatalf("takeover fence %d did not advance past the deposed owner's fence %d", takeover.Fence, st.Fence)
	}

	if _, err := pg.RenewBuilderLease(ctx, "compatibility-builder", "owner-a", st.Fence, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("owner-a's stale renew (fence %d) did not fail with ErrLeaseLost after owner-b's takeover: err=%v", st.Fence, err)
	}
	// And owner-b's own lease is unharmed by owner-a's stale renew attempt.
	current, ok, err := pg.GetBuilderLease(ctx, "compatibility-builder")
	if err != nil || !ok {
		t.Fatalf("get lease after stale renew: ok=%v err=%v", ok, err)
	}
	if current.Owner != "owner-b" || current.Fence != takeover.Fence {
		t.Fatalf("lease state after stale renew = %+v, want owner-b fence %d", current, takeover.Fence)
	}
}

func TestIntegrationBuilderLeaseReleaseLetsTheNextOwnerAcquireImmediately(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	st, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := pg.ReleaseBuilderLease(ctx, "compatibility-builder", "owner-a", st.Fence); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok, err := pg.GetBuilderLease(ctx, "compatibility-builder"); err != nil || ok {
		t.Fatalf("lease row still present after release: ok=%v err=%v", ok, err)
	}
	// No need to wait out a TTL: release is immediate, well inside the
	// minute-long TTL owner-a had asked for.
	if _, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-b", time.Minute); err != nil {
		t.Fatalf("owner-b acquire after release: %v", err)
	}
}

// ---------------------------------------- the governor's pause (CSX-454) --

// The pause is a deadline in the same row, written with no fencing token by
// a process that does not hold the lease -- csx-server's governor -- and
// read by whoever is doing the work. It must be visible immediately and must
// clear itself when nobody refreshes it.
func TestIntegrationBuilderPauseIsVisibleImmediatelyAndExpiresOnItsOwn(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	if _, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if paused, err := pg.BuilderLeasePaused(ctx, "compatibility-builder"); err != nil || paused {
		t.Fatalf("BuilderLeasePaused on a fresh lease = %v, %v; want false, nil", paused, err)
	}

	if err := pg.PauseBuilderLease(ctx, "compatibility-builder", 700*time.Millisecond); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused, err := pg.BuilderLeasePaused(ctx, "compatibility-builder"); err != nil || !paused {
		t.Fatalf("BuilderLeasePaused right after a pause = %v, %v; want true, nil", paused, err)
	}

	// Nobody refreshes it: a governor that died mid-incident must not leave
	// the aggregation pipeline stopped.
	time.Sleep(900 * time.Millisecond)
	if paused, err := pg.BuilderLeasePaused(ctx, "compatibility-builder"); err != nil || paused {
		t.Fatalf("BuilderLeasePaused after the pause TTL = %v, %v; want false, nil", paused, err)
	}
}

func TestIntegrationBuilderResumeClearsThePauseBeforeItsDeadline(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	if err := pg.PauseBuilderLease(ctx, "compatibility-builder", time.Hour); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := pg.ResumeBuilderLease(ctx, "compatibility-builder"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if paused, err := pg.BuilderLeasePaused(ctx, "compatibility-builder"); err != nil || paused {
		t.Fatalf("BuilderLeasePaused after resume = %v, %v; want false, nil", paused, err)
	}
	// Resuming something that is not paused is not an error: the governor
	// calls it whenever it owes a resume, without first asking.
	if err := pg.ResumeBuilderLease(ctx, "compatibility-builder"); err != nil {
		t.Fatalf("second resume: %v", err)
	}
}

// CSX-451's recovery guarantee, re-proven with CSX-454's flag set: a Builder
// that is paused and then dies leaves exactly the row a running one would,
// and the next process reclaims it on the lease's own TTL. Pausing must
// never be able to wedge production.
func TestIntegrationPausedBuilderLeaseStillExpiresAndIsReclaimed(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	st, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-a", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := pg.PauseBuilderLease(ctx, "compatibility-builder", time.Hour); err != nil {
		t.Fatalf("pause: %v", err)
	}
	// owner-a is paused and now dies: no release, no renew.
	time.Sleep(400 * time.Millisecond)

	took, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-b", time.Minute)
	if err != nil {
		t.Fatalf("a paused lease was not reclaimable after its TTL passed: %v", err)
	}
	if took.Owner != "owner-b" || took.Fence <= st.Fence {
		t.Fatalf("reclaim of a paused lease = %+v; want owner-b with a fence above %d", took, st.Fence)
	}
	// The successor inherits the pause: it belongs to the work, not to the
	// process that was doing it.
	if paused, err := pg.BuilderLeasePaused(ctx, "compatibility-builder"); err != nil || !paused {
		t.Fatalf("BuilderLeasePaused after takeover = %v, %v; want true, nil", paused, err)
	}
	// And a renew by the new owner keeps the pause exactly where it was --
	// the lease's own columns and the pause never write each other.
	if _, err := pg.RenewBuilderLease(ctx, "compatibility-builder", "owner-b", took.Fence, time.Minute); err != nil {
		t.Fatalf("renew after takeover: %v", err)
	}
	if paused, err := pg.BuilderLeasePaused(ctx, "compatibility-builder"); err != nil || !paused {
		t.Fatalf("a renew cleared the pause: %v, %v", paused, err)
	}
}

// Pausing before any Builder has ever run must still be seen by the first
// one that starts, or a governor reacting to pressure during a cold start
// would be talking to nobody.
func TestIntegrationBuilderPauseWithNoLeaseRowIsSeenByTheFirstAcquirer(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	if err := pg.PauseBuilderLease(ctx, "compatibility-builder", time.Hour); err != nil {
		t.Fatalf("pause with no lease row: %v", err)
	}
	st, err := pg.AcquireBuilderLease(ctx, "compatibility-builder", "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("the placeholder row left by a pause blocked the first real acquire: %v", err)
	}
	if st.Owner != "owner-a" {
		t.Fatalf("first acquire after a pause = %+v, want owner-a", st)
	}
	if paused, err := pg.BuilderLeasePaused(ctx, "compatibility-builder"); err != nil || !paused {
		t.Fatalf("the first Builder does not see the pause that preceded it: %v, %v", paused, err)
	}
}
