package serverstore

import (
	"context"
	"testing"
	"time"
)

func TestIntegrationFarmCoveragePublishAndRead(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	if _, _, found, err := pg.GetFarmCoverage(ctx); err != nil {
		t.Fatalf("GetFarmCoverage before publish: %v", err)
	} else if found {
		t.Fatal("expected found=false before any Builder pass has published coverage")
	}

	rows := []FarmAxisCoverage{
		{OS: "linux", Ecosystem: "npm", Observed: 10, Measured: 8, Proven: 7, ObservedProven: 6},
		{OS: "windows", Ecosystem: "golang", Observed: 3, Measured: 0, Proven: 0, ObservedProven: 0},
	}
	generatedAt := time.Now().UTC().Truncate(time.Second)
	if err := pg.PutFarmCoverage(ctx, rows, generatedAt); err != nil {
		t.Fatalf("PutFarmCoverage: %v", err)
	}

	got, gotAt, found, err := pg.GetFarmCoverage(ctx)
	if err != nil {
		t.Fatalf("GetFarmCoverage after publish: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after publish")
	}
	if !gotAt.Equal(generatedAt) {
		t.Fatalf("generatedAt = %v, want %v", gotAt, generatedAt)
	}
	if len(got) != len(rows) {
		t.Fatalf("got %d rows, want %d", len(got), len(rows))
	}

	// A second publish fully replaces the prior set (whole-table snapshot,
	// not a per-row upsert) -- coverage rows disappear when an axis stops
	// being observed, unlike package_symbols which is keyed per-purl.
	if err := pg.PutFarmCoverage(ctx, rows[:1], generatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("PutFarmCoverage second publish: %v", err)
	}
	got2, _, _, err := pg.GetFarmCoverage(ctx)
	if err != nil {
		t.Fatalf("GetFarmCoverage after second publish: %v", err)
	}
	if len(got2) != 1 {
		t.Fatalf("got %d rows after replace, want 1", len(got2))
	}
}

// A Builder pass that legitimately computes zero coverage cells (bootstrap
// state, or a moment where every axis is unobserved) still publishes.
// found must stay true and generatedAt must still be reported -- row count
// alone cannot answer "has a pass ever run", since an empty farm_coverage
// table looks the same in both cases.
func TestIntegrationFarmCoveragePublishWithZeroRowsIsStillFound(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	rows := []FarmAxisCoverage{{OS: "linux", Ecosystem: "npm", Observed: 5, Proven: 3}}
	firstAt := time.Now().UTC().Truncate(time.Second)
	if err := pg.PutFarmCoverage(ctx, rows, firstAt); err != nil {
		t.Fatalf("PutFarmCoverage (non-empty): %v", err)
	}

	emptyAt := firstAt.Add(time.Minute)
	if err := pg.PutFarmCoverage(ctx, []FarmAxisCoverage{}, emptyAt); err != nil {
		t.Fatalf("PutFarmCoverage (empty): %v", err)
	}

	got, gotAt, found, err := pg.GetFarmCoverage(ctx)
	if err != nil {
		t.Fatalf("GetFarmCoverage after empty publish: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after a publish with zero rows -- publication, not row count, is what found answers")
	}
	if len(got) != 0 {
		t.Fatalf("got %d rows, want 0 after an empty publish", len(got))
	}
	if !gotAt.Equal(emptyAt) {
		t.Fatalf("generatedAt = %v, want %v (the empty publish's own timestamp)", gotAt, emptyAt)
	}
}
