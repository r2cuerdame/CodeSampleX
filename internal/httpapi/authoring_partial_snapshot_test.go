package httpapi

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type partialAuthoringSource struct {
	*serverstore.Fake
	calls       atomic.Int64
	failThrough int64
	rows        []serverstore.WantedRow
}

func (s *partialAuthoringSource) read() ([]serverstore.WantedRow, error) {
	if s.calls.Add(1) <= s.failThrough {
		return nil, &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}
	}
	return s.rows, nil
}
func (s *partialAuthoringSource) ListAuthoringExpansionCandidates(context.Context, int) ([]serverstore.WantedRow, error) {
	return s.read()
}
func (s *partialAuthoringSource) ListAuthoringExpansionCandidatesUnhurried(context.Context, int) ([]serverstore.WantedRow, error) {
	return s.read()
}

func TestPartialAuthoringSnapshotRetriesWithoutWaitingForTheTTL(t *testing.T) {
	store := &partialAuthoringSource{Fake: serverstore.NewFake(), failThrough: 1, rows: []serverstore.WantedRow{
		{Ecosystem: "npm", Name: "@tiptap/core", Version: "3.11.0", Kind: "EXPANSION", Axis: serverstore.AuthoringAxisSample, Score: 10000},
	}}
	a := &api{d: Deps{Store: store, Now: func() time.Time { return testNow }, authoringWorkTimeout: time.Second}}
	waiting, release := make(chan time.Duration, 1), make(chan struct{})
	defer close(release)
	a.authoringCandidates.retryDraw = func(time.Duration) time.Duration { return 0 }
	a.authoringCandidates.retryWait = func(d time.Duration) { waiting <- d; <-release }
	first, err := a.loadAuthoringCandidates(t.Context(), store)
	if err != nil || len(first.expansion) != 0 {
		t.Fatalf("partial answer=%+v err=%v", first, err)
	}
	select {
	case delay := <-waiting:
		if delay != time.Second {
			t.Fatalf("retry delay=%v", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("partial snapshot was cached as complete for 30 minutes; no background retry was scheduled")
	}
	for range 20 {
		if _, err := a.loadAuthoringCandidates(t.Context(), store); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("polls bypassed shared retry backoff: %d scans", got)
	}
	release <- struct{}{}
	waitFor(t, func() bool {
		a.authoringCandidates.mu.Lock()
		defer a.authoringCandidates.mu.Unlock()
		return len(a.authoringCandidates.snapshot.expansion) == 1
	}, "background retry never restored the eligible Sample")
	snap, err := a.loadAuthoringCandidates(t.Context(), store)
	if err != nil || len(snap.expansion) != 1 || snap.expansion[0].Axis != serverstore.AuthoringAxisSample {
		t.Fatalf("recovered snapshot=%+v err=%v", snap, err)
	}
}

func TestPartialAuthoringRefreshKeepsGoodCandidatesAndBoundsRetries(t *testing.T) {
	store := &partialAuthoringSource{Fake: serverstore.NewFake(), failThrough: 100}
	a := &api{d: Deps{Store: store, Now: func() time.Time { return testNow }, authoringWorkTimeout: time.Second}}
	good := authoringCandidateSnapshot{expansion: []serverstore.WantedRow{expansionRow("previously-discovered-sample")}, takenAt: testNow.Add(-authoringCandidateTTL)}
	a.authoringCandidates.have = true
	a.authoringCandidates.snapshot = good
	a.authoringCandidates.takenAt = good.takenAt
	a.authoringCandidates.retryDraw = func(time.Duration) time.Duration { return 0 }
	a.authoringCandidates.retryWait = func(time.Duration) {}
	if _, err := a.loadAuthoringCandidates(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		a.authoringCandidates.mu.Lock()
		defer a.authoringCandidates.mu.Unlock()
		return a.authoringCandidates.retries.State() == retrypolicy.FailedDeferred
	}, "partial refresh failures never reached bounded deferred state")
	snap, err := a.loadAuthoringCandidates(t.Context(), store)
	if err != nil || len(snap.expansion) != 1 || snap.expansion[0].Name != "previously-discovered-sample" {
		t.Fatalf("partial refresh erased discovered Sample: %+v err=%v", snap, err)
	}
	if got := store.calls.Load(); got != 1+retrypolicy.MaxRetries {
		t.Fatalf("scans=%d want %d", got, 1+retrypolicy.MaxRetries)
	}
}
