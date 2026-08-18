package serverstore

import (
	"context"
	"testing"
	"time"
)

func TestIntegrationSearchOutcomesAreAtomicDailyAggregates(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC)

	for _, event := range []struct {
		at      time.Time
		outcome SearchOutcome
	}{
		{now.AddDate(0, 0, -1), SearchOutcomeSampleHit},
		{now.AddDate(0, 0, -1).Add(time.Hour), SearchOutcomeSampleHit},
		{now, SearchOutcomeNoMatch},
		// Outside the 30-day admin window.
		{now.AddDate(0, 0, -31), SearchOutcomeNoMatch},
	} {
		if err := pg.RecordSearchOutcome(ctx, event.at, event.outcome); err != nil {
			t.Fatalf("RecordSearchOutcome(%s, %s): %v", event.at, event.outcome, err)
		}
	}
	if err := pg.RecordSearchOutcome(ctx, now, SearchOutcome("raw-query-must-not-fit")); err == nil {
		t.Fatal("unsupported outcome was persisted")
	}

	got, err := pg.AdminInsights(ctx, now)
	if err != nil {
		t.Fatalf("AdminInsights: %v", err)
	}
	if !got.Search.Available || got.Search.SampleHits != 2 || got.Search.NoMatches != 1 || got.Search.Total() != 3 || got.Search.Days != 2 {
		t.Fatalf("search outcomes = %+v, want 2 hits / 1 miss across 2 days", got.Search)
	}
	if got.Search.FirstDay != "2026-08-17" || got.Search.LastDay != "2026-08-18" {
		t.Fatalf("search outcome range = %s..%s", got.Search.FirstDay, got.Search.LastDay)
	}
}
