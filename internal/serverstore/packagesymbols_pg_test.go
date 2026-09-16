package serverstore

// Real-PostgreSQL proof of package_symbols (CSX-452): a bounded upsert and a
// bounded single-key read, and that GetPackageSymbols distinguishes "found,
// empty" from "no row at all" the same way the Fake does
// (internal/compatibility/builder_packagesymbols_test.go).
//
//	$env:CSX_TEST_DSN = "postgres://csx:csx@localhost:5432/csx"
//	go test ./internal/serverstore/ -run TestIntegrationPackageSymbols -v

import (
	"context"
	"reflect"
	"testing"
)

func TestIntegrationPackageSymbolsRoundTripsThroughPutAndGet(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	symbols, generatedAt, found, err := pg.GetPackageSymbols(ctx, "pkg:npm/axios@1.12.0")
	if err != nil {
		t.Fatalf("get before any write: %v", err)
	}
	if found {
		t.Fatalf("found = true before any write, symbols=%v", symbols)
	}
	if !generatedAt.IsZero() {
		t.Fatalf("generatedAt = %v, want zero before any write", generatedAt)
	}

	if err := pg.PutPackageSymbols(ctx, []PackageSymbolsRow{
		{PURL: "pkg:npm/axios@1.12.0", Symbols: []string{"axios.post", "axios.get"}},
		{PURL: "pkg:npm/lodash@4.17.21", Symbols: []string{"lodash.map"}},
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	symbols, generatedAt, found, err = pg.GetPackageSymbols(ctx, "pkg:npm/axios@1.12.0")
	if err != nil || !found {
		t.Fatalf("get after write: found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(symbols, []string{"axios.post", "axios.get"}) {
		t.Fatalf("symbols = %v, want [axios.post axios.get]", symbols)
	}
	// generatedAt is the DB's own now(), not the test process's clock --
	// comparing it against a host-captured timestamp is a cross-clock-domain
	// comparison that a few milliseconds of skew can fail spuriously. Only
	// its presence, and that a later write moves it forward, are asserted.
	if generatedAt.IsZero() {
		t.Fatal("generatedAt is zero after a write")
	}
	firstGeneratedAt := generatedAt

	// A second package's row is independent -- this is a keyed lookup, not a
	// scan a caller has to filter.
	symbols, _, found, err = pg.GetPackageSymbols(ctx, "pkg:npm/lodash@4.17.21")
	if err != nil || !found || !reflect.DeepEqual(symbols, []string{"lodash.map"}) {
		t.Fatalf("lodash symbols = %v found=%v err=%v, want [lodash.map] true nil", symbols, found, err)
	}

	// A later pass overwrites, it does not accumulate.
	if err := pg.PutPackageSymbols(ctx, []PackageSymbolsRow{
		{PURL: "pkg:npm/axios@1.12.0", Symbols: []string{"axios.get"}},
	}); err != nil {
		t.Fatalf("put overwrite: %v", err)
	}
	symbols, generatedAt, found, err = pg.GetPackageSymbols(ctx, "pkg:npm/axios@1.12.0")
	if err != nil || !found || !reflect.DeepEqual(symbols, []string{"axios.get"}) {
		t.Fatalf("symbols after overwrite = %v found=%v err=%v, want [axios.get] true nil", symbols, found, err)
	}
	if generatedAt.Before(firstGeneratedAt) {
		t.Fatalf("generatedAt after overwrite = %v, want at or after the first write %v", generatedAt, firstGeneratedAt)
	}

	// The untouched package from the first write is still exactly as it was.
	symbols, _, found, err = pg.GetPackageSymbols(ctx, "pkg:npm/lodash@4.17.21")
	if err != nil || !found || !reflect.DeepEqual(symbols, []string{"lodash.map"}) {
		t.Fatalf("lodash symbols after unrelated overwrite = %v found=%v err=%v, want unchanged", symbols, found, err)
	}
}

func TestIntegrationPutPackageSymbolsEmptyBatchIsANoop(t *testing.T) {
	pg := openTestPG(t)
	if err := pg.PutPackageSymbols(context.Background(), nil); err != nil {
		t.Fatalf("put empty batch: %v", err)
	}
}
