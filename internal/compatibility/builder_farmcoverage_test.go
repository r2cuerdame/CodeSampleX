package compatibility

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// farm_coverage (CSX-452) is computed by the Builder from the exact same
// (os, ecosystem) aggregation the admin request path used to run live
// (farm_pg.go's FarmCoverage), so a pass over the shared builder fixture
// must publish rows matching what that live query would have returned.
func TestBuilderRunOncePublishesFarmCoverage(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	purl, _ := seedBuilderFixture(t, store)
	// FarmCoverage's join requires packages.publicness='PUBLIC'; a receipt-
	// only registration (ensureReceiptPackages, run by the Builder itself)
	// only ever inserts UNKNOWN, so the fixture needs an explicit publicness
	// check result the way the live-query PG integration test does.
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "axios", Version: "1.12.0", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}

	if _, _, found, err := store.GetFarmCoverage(ctx); err != nil {
		t.Fatalf("GetFarmCoverage before any pass: %v", err)
	} else if found {
		t.Fatal("expected found=false before RunOnce has published coverage")
	}

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// The live aggregation depends on packages ensureReceiptPackages
	// registers as a side effect of the very same pass (FarmCoverage's
	// join requires packages.publicness='PUBLIC'), so it can only be
	// compared against the published snapshot after RunOnce, not before.
	// This is still the invariant that matters: whatever RunOnce persisted
	// must agree with what the live query answers once the corpus it read
	// is in the state that pass left it in.
	want, wantErr := store.FarmCoverage(ctx)
	if wantErr != nil {
		t.Fatalf("live FarmCoverage: %v", wantErr)
	}
	if len(want) == 0 {
		t.Fatal("fixture produced no coverage cells to compare against")
	}

	got, generatedAt, found, err := store.GetFarmCoverage(ctx)
	if err != nil {
		t.Fatalf("GetFarmCoverage: %v", err)
	}
	if !found {
		t.Fatal("farm_coverage row missing after a pass that ran a full aggregation")
	}
	if !generatedAt.Equal(testNow) {
		t.Fatalf("generatedAt = %s, want %s", generatedAt, testNow)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("published coverage = %+v, want %+v (the live query's own answer)", got, want)
	}
}

// A second, incremental pass that only touches one package must still
// republish farm_coverage -- the Builder republishes the whole table every
// pass it reaches (full or incremental-with-changes), unlike
// package_symbols, which only upserts purls the pass actually saw.
func TestBuilderRunOnceRepublishesFarmCoverageOnIncrementalPass(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	purl, _ := seedBuilderFixture(t, store)
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "axios", Version: "1.12.0", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	first, firstAt, _, err := store.GetFarmCoverage(ctx)
	if err != nil {
		t.Fatalf("GetFarmCoverage after first pass: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("fixture produced no coverage cells to compare across passes")
	}

	dirtyOnePackage(store, purl, "axios.post")
	later := testNow.Add(time.Minute)
	b.Now = func() time.Time { return later }
	store.NowFn = func() time.Time { return later }
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	second, secondAt, found, err := store.GetFarmCoverage(ctx)
	if err != nil {
		t.Fatalf("GetFarmCoverage after second pass: %v", err)
	}
	if !found {
		t.Fatal("farm_coverage row missing after second pass")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("coverage cells changed across an incremental pass with unrelated fixture: first=%+v second=%+v", first, second)
	}
	if !firstAt.Equal(testNow) || !secondAt.Equal(later) {
		t.Fatalf("generatedAt did not advance with the pass clock: first=%s second=%s", firstAt, secondAt)
	}
}
