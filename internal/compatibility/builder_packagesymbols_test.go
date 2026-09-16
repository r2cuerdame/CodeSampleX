package compatibility

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// package_symbols (CSX-452) is grouped from the exact same attributed
// targets RunOnce already writes to compatibility_snapshots, so a pass over
// the shared builder fixture must publish the one symbol it seeds.
func TestBuilderRunOncePublishesPackageSymbols(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	purl, _ := seedBuilderFixture(t, store)

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	symbols, generatedAt, ok, err := store.GetPackageSymbols(ctx, purl)
	if err != nil {
		t.Fatalf("GetPackageSymbols: %v", err)
	}
	if !ok {
		t.Fatal("package_symbols row missing after a pass that materialized this purl")
	}
	if len(symbols) != 1 || symbols[0] != "axios.post" {
		t.Fatalf("symbols = %v, want [axios.post]", symbols)
	}
	if !generatedAt.Equal(testNow) {
		t.Fatalf("generatedAt = %v, want %v", generatedAt, testNow)
	}
}

// An incremental pass touching one package must not disturb another
// package's already-published symbol list -- writePackageSymbols only
// upserts purls the pass actually saw.
func TestIncrementalPassLeavesUntouchedPackageSymbolsAlone(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }
	purl, _ := seedBuilderFixture(t, store)

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	before, _, ok, err := store.GetPackageSymbols(ctx, purl)
	if err != nil || !ok {
		t.Fatalf("GetPackageSymbols after first pass: ok=%v err=%v", ok, err)
	}

	// A second pass with nothing changed still runs a scheduled pass; the
	// published row for the untouched package must read back identically.
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	after, _, ok, err := store.GetPackageSymbols(ctx, purl)
	if err != nil || !ok {
		t.Fatalf("GetPackageSymbols after second pass: ok=%v err=%v", ok, err)
	}
	if len(before) != len(after) || before[0] != after[0] {
		t.Fatalf("symbols changed across an idle pass: before=%v after=%v", before, after)
	}
}

// A purl the Builder has never touched has no package_symbols row at all --
// found=false, not an empty list masquerading as a computed answer.
func TestGetPackageSymbolsReportsNotFoundForAnUntouchedPURL(t *testing.T) {
	store := serverstore.NewFake()
	symbols, generatedAt, ok, err := store.GetPackageSymbols(context.Background(), "pkg:npm/never-seen@1.0.0")
	if err != nil {
		t.Fatalf("GetPackageSymbols: %v", err)
	}
	if ok {
		t.Fatalf("found = true for a purl the store never received, symbols=%v", symbols)
	}
	if !generatedAt.IsZero() {
		t.Fatalf("generatedAt = %v, want zero for an unfound purl", generatedAt)
	}
}
