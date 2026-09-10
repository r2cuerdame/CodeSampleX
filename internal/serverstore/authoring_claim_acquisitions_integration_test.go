package serverstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ClaimAuthoringWork walks the candidate window inside ONE transaction: the
// attempt ledgers arrive in one query and each candidate that is still open
// costs one INSERT ... ON CONFLICT DO NOTHING inside that transaction. The
// worst case -- every coordinate in a full window already held by somebody
// else -- is therefore a few hundred statements on one connection, never a
// few hundred connections. This pins that bound so the claim cannot quietly
// become a checkout per candidate (#174).
func TestIntegrationClaimAuthoringWorkHoldsOneCheckoutForAFullWindow(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	lease := now.Add(24 * time.Hour)

	const window = 400
	candidates := make([]WantedRow, 0, window)
	for i := 0; i < window; i++ {
		candidates = append(candidates, WantedRow{
			Ecosystem: "npm", Name: fmt.Sprintf("held-%04d", i), Version: "1.0.0",
			Kind: "WANTED", Axis: AuthoringAxisSample, Score: int64(window - i),
		})
	}
	// Every coordinate is already leased by a different live session, so the
	// poller below has to try each one and be refused each time.
	for i, candidate := range candidates {
		if _, ok, err := pg.ClaimAuthoringWork(ctx, fmt.Sprintf("holder-%04d", i), []WantedRow{candidate}, now, lease); err != nil || !ok {
			t.Fatalf("holder %d: ok=%t err=%v", i, ok, err)
		}
	}

	before := classStat(t, pg.PoolStats(), "background").Acquired
	_, found, err := pg.ClaimAuthoringWork(ctx, "poller", candidates, now.Add(time.Minute), lease)
	if err != nil {
		t.Fatalf("ClaimAuthoringWork: %v", err)
	}
	after := classStat(t, pg.PoolStats(), "background").Acquired
	if found {
		t.Fatal("the poller was handed a coordinate somebody else holds")
	}
	if got, want := after-before, uint64(1); got != want {
		t.Fatalf("a %d-candidate window that is entirely held cost %d checkouts, want %d", window, got, want)
	}
}
