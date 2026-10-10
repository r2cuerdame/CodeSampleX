package httpapi

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type wantedDuringPartialRefresh struct {
	*partialAuthoringSource
	wanted      []serverstore.WantedRow
	wantedErr   error
	wantedCalls atomic.Int64
}

func (s *wantedDuringPartialRefresh) TopWanted(context.Context, int) ([]serverstore.WantedRow, error) {
	s.wantedCalls.Add(1)
	return append([]serverstore.WantedRow(nil), s.wanted...), s.wantedErr
}

// A new explicit request must become visible even when optional expansion
// cannot refresh. Its successful empty result is authoritative too; an
// unavailable WANTED read, in contrast, must keep the last known demand.
func TestPartialAuthoringRefreshKeepsExplicitDemandCurrent(t *testing.T) {
	oldWanted := []serverstore.WantedRow{{Ecosystem: "npm", Name: "old-demand", Version: "1.0.0", Kind: "WANTED"}}
	newWanted := []serverstore.WantedRow{{Ecosystem: "npm", Name: "new-demand", Version: "1.0.0", Kind: "WANTED"}}
	oldExpansion := []serverstore.WantedRow{expansionRow("last-known-expansion")}
	oldCLI := []serverstore.WantedRow{{Ecosystem: "generic", Name: "cli:git", Kind: "CLI"}}
	oldAt := testNow.Add(-3 * authoringCandidateTTL)

	for _, tc := range []struct {
		name      string
		wanted    []serverstore.WantedRow
		wantedErr error
		want      []serverstore.WantedRow
	}{
		{name: "new explicit demand", wanted: newWanted, want: newWanted},
		{name: "successful empty demand", want: nil},
		{name: "unavailable explicit demand", wantedErr: errors.New("wanted unavailable"), want: oldWanted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &wantedDuringPartialRefresh{
				partialAuthoringSource: &partialAuthoringSource{Fake: serverstore.NewFake(), failThrough: 100},
				wanted:                 tc.wanted, wantedErr: tc.wantedErr,
			}
			a := &api{d: Deps{Store: store, Now: func() time.Time { return testNow }, authoringWorkTimeout: time.Second}}
			g := &a.authoringCandidates
			g.have = true
			g.snapshot = authoringCandidateSnapshot{
				wanted: oldWanted, expansion: oldExpansion, cli: oldCLI, takenAt: oldAt,
			}
			g.takenAt = oldAt
			g.retryDraw = func(time.Duration) time.Duration { return 0 }
			g.retryWait = func(time.Duration) {}

			if _, err := a.loadAuthoringCandidates(t.Context(), store); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool {
				g.mu.Lock()
				defer g.mu.Unlock()
				return g.retries.State() == retrypolicy.FailedDeferred
			}, "failed optional expansion did not enter bounded deferred state")

			snap, err := a.loadAuthoringCandidates(t.Context(), store)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(snap.wanted, tc.want) {
				t.Errorf("visible explicit demand=%+v; want %+v", snap.wanted, tc.want)
			}
			if !reflect.DeepEqual(snap.expansion, oldExpansion) || !reflect.DeepEqual(snap.cli, oldCLI) {
				t.Errorf("partial refresh erased previous expansion/CLI candidates: %+v", snap)
			}
			g.mu.Lock()
			clockPreserved := snap.takenAt.Equal(oldAt) && g.takenAt.Equal(oldAt)
			failurePreserved := g.lastErr != nil && g.retries.State() == retrypolicy.FailedDeferred
			g.mu.Unlock()
			if !clockPreserved || !failurePreserved {
				t.Errorf("partial refresh claimed full freshness or reset retry protection: clock=%t failure=%t", clockPreserved, failurePreserved)
			}
			for range 20 {
				if _, err := a.loadAuthoringCandidates(t.Context(), store); err != nil {
					t.Fatal(err)
				}
			}
			if got := store.wantedCalls.Load(); got != 1+retrypolicy.MaxRetries {
				t.Errorf("explicit-demand reads=%d; want bounded series %d", got, 1+retrypolicy.MaxRetries)
			}
		})
	}
}
