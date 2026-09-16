package main

// #454's own acceptance line, measured rather than argued: "Automated
// pressure test proves background-first shedding."
//
// Everything below runs against the real HTTP handler, the real pool and a
// throwaway schema of the real PostgreSQL the other TestIntegration* tests
// in this package use. The pressure is real: a locked `wanted` table and
// more concurrent interactive readers than the class has connections, which
// is the exact shape R2C-58 measured in production and dbpressure_integration_test.go
// already pins. Nothing about the governor is faked except the host CPU
// reading, which is stubbed OUT (no signal) on purpose -- the point is to
// prove pool pressure alone drives the decision, on a machine whose real
// steal time is whatever the CI runner's hypervisor happens to be doing.
//
// The two halves matter equally. Pausing under pressure is half the promise;
// #454 also says "background jobs must make forward progress after pressure
// clears without manual restart", so the second half of this test stops the
// load and proves both producers come back with no process restarted, no
// operator involved and no second lever pulled.

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/hostpressure"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// syncBuffer collects log output from the governor goroutine and from HTTP
// handlers at the same time.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureStandardLog redirects the standard logger -- which is where the
// governor and the request middleware both write -- into w for the duration
// of one test, and hands back the restore.
func captureStandardLog(w io.Writer) func() {
	prevOut, prevFlags, prevPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(w)
	log.SetFlags(0)
	return func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	}
}

// waitForGovernor polls cond until it holds or the deadline passes. The governor
// acts on its own clock, so every assertion about it is a "within a few
// intervals" assertion.
func waitForGovernor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// interactiveLoad keeps more readers than ClassInteractive has connections
// all asking for a table that is locked, which is what produces real
// refusals (ErrPoolBusy) in the pool's own counters -- the numbers the
// governor windows. It is the load-generation shape
// TestIntegrationOneStuckPageDoesNotTakeTheSiteDown uses, driven at the
// store instead of through HTTP so that no response cache or admission gate
// upstream of the pool can absorb it.
type interactiveLoad struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func driveInteractivePressure(pg *serverstore.PG, readers int) *interactiveLoad {
	ctx, cancel := context.WithCancel(context.Background())
	load := &interactiveLoad{cancel: cancel}
	for i := 0; i < readers; i++ {
		load.wg.Add(1)
		go func() {
			defer load.wg.Done()
			for ctx.Err() == nil {
				// A fresh budget per read: each one is a new visitor, not a
				// follow-up read of the same page (which the pool suppresses
				// rather than counts).
				readCtx := serverstore.WithQueryBudget(ctx, serverstore.NewQueryBudget(serverstore.ClassInteractive))
				_, _, _ = pg.ListWanted(readCtx, "", 0, 20)
			}
		}()
	}
	return load
}

func (l *interactiveLoad) stop() {
	l.cancel()
	l.wg.Wait()
}

func TestIntegrationGovernorPausesBuilderAndFarmIngestUnderPressure(t *testing.T) {
	pol := testServerPoolPolicy()
	srv, outside, pg := openTestServer(t, pol)

	// The governor's log is the operator's evidence, and docs/operations.md
	// tells an operator to grep it for these exact reason strings, so the
	// test reads them the same way.
	logs := &syncBuffer{}
	restoreLog := captureStandardLog(logs)
	defer restoreLog()

	leader := &compatibility.Leader{
		Store: pg,
		Cfg: compatibility.LeaseConfig{
			Name:  compatibility.DefaultLeaseName,
			Owner: "csx-server-governor-test",
			// Comfortably longer than this test, so a pause ends because the
			// governor resumed it and not because it lapsed.
			PauseTTL: 5 * time.Minute,
		},
	}
	// A Builder holds the lease throughout, exactly as production has:
	// pausing must work on a lease somebody else owns, without its fencing
	// token, and must not disturb that ownership.
	held, err := pg.AcquireBuilderLease(
		serverstore.WithQueryClass(context.Background(), serverstore.ClassBackground),
		compatibility.DefaultLeaseName, "csx-builder-test", 10*time.Minute)
	if err != nil {
		t.Fatalf("seed the builder lease: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Real pool, real lease, real pool ceiling; only /proc is stubbed, and
	// stubbed to "no signal" so nothing here can pass for the wrong reason.
	go runGovernor(ctx, pg, &fakeHost{err: hostpressure.ErrUnsupportedPlatform},
		leader, pg, pol.FarmIngestConns, 100*time.Millisecond)

	// The governor starts by asserting the state it believes in, which
	// includes clearing any pause a predecessor left behind.
	waitForGovernor(t, 5*time.Second, "the governor's first tick", func() bool {
		paused, err := leader.IsPaused(context.Background())
		return err == nil && !paused
	})

	// ------------------------------------------------------- pressure on --

	release := blockWanted(t, outside)
	load := driveInteractivePressure(pg, pol.InteractiveConns+6)

	waitForGovernor(t, 15*time.Second, "the Builder to be paused", func() bool {
		paused, err := leader.IsPaused(context.Background())
		return err == nil && paused
	})
	waitForGovernor(t, 5*time.Second, "farm ingest admission to be shed", func() bool {
		return classStatOf(pg.PoolStats(), "farm_ingest").Limit == 0
	})

	// Farm's own route, during the pressure window: a fast 503 it can back
	// off on -- CSX-453's existing contract -- and not a hang and not a
	// success.
	started := time.Now()
	status, err := postEvidenceBatch(t, srv.URL, "governor-pressure")
	if err != nil {
		t.Fatalf("POST /v1/evidence/batches during the pause: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("farm ingest answered %d while the governor had it paused, want 503", status)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second || elapsed > pol.FarmIngestWait {
		t.Fatalf("the paused farm request took %v, want an answer well inside the %s wait budget",
			elapsed.Round(time.Millisecond), pol.FarmIngestWait)
	}

	// The pause did not touch the lease: the Builder that owns it still
	// owns it, at the same fence, and could renew it right now.
	leaseCtx := serverstore.WithQueryClass(context.Background(), serverstore.ClassBackground)
	current, ok, err := pg.GetBuilderLease(leaseCtx, compatibility.DefaultLeaseName)
	if err != nil || !ok {
		t.Fatalf("read the lease during the pause: ok=%v err=%v", ok, err)
	}
	if current.Owner != "csx-builder-test" || current.Fence != held.Fence {
		t.Fatalf("the pause disturbed the lease: %+v, want owner csx-builder-test at fence %d", current, held.Fence)
	}
	if _, err := pg.RenewBuilderLease(leaseCtx, compatibility.DefaultLeaseName, "csx-builder-test", held.Fence, 10*time.Minute); err != nil {
		t.Fatalf("the lease holder could not renew a paused lease: %v", err)
	}

	// ------------------------------------------------------ pressure off --

	load.stop()
	release()

	// Nothing is restarted here. The same processes, the same goroutines and
	// the same pool notice the window is clean and put both producers back.
	waitForGovernor(t, 20*time.Second, "the Builder to resume", func() bool {
		paused, err := leader.IsPaused(context.Background())
		return err == nil && !paused
	})
	waitForGovernor(t, 5*time.Second, "farm ingest admission to be restored", func() bool {
		return classStatOf(pg.PoolStats(), "farm_ingest").Limit == pol.FarmIngestConns
	})

	// And Farm is served again on the same route that was refusing it.
	status, err = postEvidenceBatch(t, srv.URL, "governor-resumed")
	if err != nil {
		t.Fatalf("POST /v1/evidence/batches after the resume: %v", err)
	}
	if status >= 300 {
		t.Fatalf("farm ingest answered %d after the governor resumed it, want a success", status)
	}

	// The operator-visible half: one line naming the reason on the way in,
	// one on the way out. Task 7's drill greps for these exact strings.
	out := logs.String()
	if !strings.Contains(out, "governor paused background work reason="+reasonInteractivePoolPressure) {
		t.Fatalf("no pause line naming %q in the log:\n%s", reasonInteractivePoolPressure, out)
	}
	if !strings.Contains(out, "governor resumed background work") {
		t.Fatalf("no resume line in the log:\n%s", out)
	}
	// The pressure window above spans dozens of 100ms ticks. A handful of
	// lines means the governor logs transitions; one per tick would mean it
	// logs ticks, which is the thing budgetPressureWindow exists to prevent.
	if got := strings.Count(out, "governor paused background work"); got < 1 || got > 3 {
		t.Fatalf("the governor wrote %d pause lines across the pressure window; it must log the transition, not the tick", got)
	}
}
