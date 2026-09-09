package serverstore

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

// The authoring poll asks which of a window's dependency coordinates already
// have a PUBLIC package row. One GetPackage checkout per candidate made that
// a few hundred interactive checkouts per poll on the endpoint the whole
// fleet polls (#174). The bulk form must return the same rows GetPackage
// does -- every column, so publicness and checked_at can be judged from it --
// keep one page to one checkout, and cost nothing when asked about nothing.
func TestIntegrationPackagesByPURLMatchesGetPackageInOneCheckout(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	checked := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var purls []string
	for i := 0; i < 6; i++ {
		purl := fmt.Sprintf("pkg:npm/rows%03d@1.%d.0", i, i)
		purls = append(purls, purl)
		row := PackageRow{
			PURL: purl, Ecosystem: "npm", Name: fmt.Sprintf("rows%03d", i),
			Version: fmt.Sprintf("1.%d.0", i), Major: "1", Publicness: "UNKNOWN",
		}
		if i%2 == 0 {
			row.Publicness, row.CheckedAt = "PUBLIC", checked
		}
		if err := pg.UpsertPackage(ctx, row); err != nil {
			t.Fatalf("UpsertPackage %s: %v", purl, err)
		}
	}
	page := append(append([]string(nil), purls...), "pkg:npm/rows-absent@9.9.9")

	want := map[string]PackageRow{}
	for _, purl := range page {
		row, ok, err := pg.GetPackage(ctx, purl)
		if err != nil {
			t.Fatalf("GetPackage %s: %v", purl, err)
		}
		if ok {
			want[purl] = row
		}
	}

	before := classStat(t, pg.PoolStats(), "background").Acquired
	got, err := pg.PackagesByPURL(ctx, page)
	if err != nil {
		t.Fatalf("PackagesByPURL: %v", err)
	}
	after := classStat(t, pg.PoolStats(), "background").Acquired
	if got, want := after-before, uint64(1); got != want {
		t.Fatalf("package page checkouts = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PackagesByPURL disagrees with GetPackage:\n got %+v\nwant %+v", got, want)
	}
	if _, present := got["pkg:npm/rows-absent@9.9.9"]; present {
		t.Fatal("an absent purl came back present")
	}

	before = after
	if got, err := pg.PackagesByPURL(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty package page = %v, err=%v", got, err)
	}
	if after := classStat(t, pg.PoolStats(), "background").Acquired; after != before {
		t.Fatalf("empty package page acquired a connection: before=%d after=%d", before, after)
	}
}

// The registry symbol endpoint reads one family snapshot per release. The
// bulk form must answer exactly what GetSnapshot answers for that symbol --
// and only that symbol: a package-level snapshot on the same release is not
// evidence about the family -- in one checkout per page.
func TestIntegrationSnapshotsForPURLsMatchesGetSnapshotInOneCheckout(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	var purls []string
	for i := 0; i < 6; i++ {
		purl := fmt.Sprintf("pkg:npm/snap@1.%d.0", i)
		purls = append(purls, purl)
		if err := pg.PutSnapshot(ctx, purl, "", fmt.Sprintf(`{"purl": %q, "symbol": "", "i": %d}`, purl, i)); err != nil {
			t.Fatalf("PutSnapshot package-level %s: %v", purl, err)
		}
		if i%2 == 0 {
			if err := pg.PutSnapshot(ctx, purl, "snap.call", fmt.Sprintf(`{"purl": %q, "symbol": "snap.call", "i": %d}`, purl, i)); err != nil {
				t.Fatalf("PutSnapshot family %s: %v", purl, err)
			}
		}
	}
	page := append(append([]string(nil), purls...), "pkg:npm/snap-absent@9.9.9")

	for _, symbol := range []string{"snap.call", "", "snap.none"} {
		want := map[string]string{}
		for _, purl := range page {
			js, ok, err := pg.GetSnapshot(ctx, purl, symbol)
			if err != nil {
				t.Fatalf("GetSnapshot %s %q: %v", purl, symbol, err)
			}
			if ok {
				want[purl] = js
			}
		}
		before := classStat(t, pg.PoolStats(), "background").Acquired
		got, err := pg.SnapshotsForPURLs(ctx, page, symbol)
		if err != nil {
			t.Fatalf("SnapshotsForPURLs %q: %v", symbol, err)
		}
		after := classStat(t, pg.PoolStats(), "background").Acquired
		if got, want := after-before, uint64(1); got != want {
			t.Fatalf("symbol %q: snapshot page checkouts = %d, want %d", symbol, got, want)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("symbol %q: SnapshotsForPURLs disagrees with GetSnapshot:\n got %v\nwant %v", symbol, got, want)
		}
	}
	if got := mustSnapshotPage(t, pg, page, "snap.call"); len(got) != 3 {
		t.Fatalf("family page = %v, want the three releases with family evidence", got)
	}

	before := classStat(t, pg.PoolStats(), "background").Acquired
	if got, err := pg.SnapshotsForPURLs(ctx, nil, "snap.call"); err != nil || len(got) != 0 {
		t.Fatalf("empty snapshot page = %v, err=%v", got, err)
	}
	if after := classStat(t, pg.PoolStats(), "background").Acquired; after != before {
		t.Fatalf("empty snapshot page acquired a connection: before=%d after=%d", before, after)
	}
}

func mustSnapshotPage(t *testing.T, pg *PG, purls []string, symbol string) map[string]string {
	t.Helper()
	got, err := pg.SnapshotsForPURLs(context.Background(), purls, symbol)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Receipt-derived registration writes one packages row per release the
// registry has never seen, and it must never write anything else. The
// builder learns "never seen" from a membership probe and writes a page
// later; in between, the registry check can confirm one of those releases
// PUBLIC. A conflict rule that replaced publicness and checked_at turned
// that confirmation back into UNKNOWN (#174 review). RegisterPackages is
// insert-if-absent: a row that exists, however it came to exist, is left
// completely alone -- publicness, checked_at, first_seen and last_seen.
func TestIntegrationRegisterPackagesLeavesEveryExistingRowAlone(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	checked := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	rowFor := func(i int, publicness string, checkedAt time.Time) PackageRow {
		return PackageRow{
			PURL: fmt.Sprintf("pkg:npm/reg%03d@3.%d.0", i, i), Ecosystem: "npm",
			Name: fmt.Sprintf("reg%03d", i), Version: fmt.Sprintf("3.%d.0", i), Major: "3",
			Publicness: publicness, CheckedAt: checkedAt,
		}
	}
	// Rows 0 and 1 exist before the page: one confirmed PUBLIC, one still
	// UNKNOWN. Rows 2..5 are new; row 4 is repeated within the page.
	confirmed, unknown := rowFor(0, "PUBLIC", checked), rowFor(1, "UNKNOWN", time.Time{})
	for _, row := range []PackageRow{confirmed, unknown} {
		if err := pg.UpsertPackage(ctx, row); err != nil {
			t.Fatalf("UpsertPackage %s: %v", row.PURL, err)
		}
	}
	confirmedBefore, _, _ := pg.GetPackage(ctx, confirmed.PURL)
	unknownBefore, _, _ := pg.GetPackage(ctx, unknown.PURL)

	// The builder registers everything as UNKNOWN with no checked_at,
	// including the two releases it believed were absent.
	page := []PackageRow{
		rowFor(0, "UNKNOWN", time.Time{}), rowFor(1, "", time.Time{}),
		rowFor(2, "UNKNOWN", time.Time{}), rowFor(3, "", time.Time{}),
		rowFor(4, "UNKNOWN", time.Time{}), rowFor(4, "UNKNOWN", time.Time{}), rowFor(5, "UNKNOWN", time.Time{}),
	}
	before := classStat(t, pg.PoolStats(), "background").Acquired
	if err := pg.RegisterPackages(ctx, page); err != nil {
		t.Fatalf("RegisterPackages: %v", err)
	}
	after := classStat(t, pg.PoolStats(), "background").Acquired
	if got, want := after-before, uint64(1); got != want {
		t.Fatalf("registration page checkouts = %d, want %d", got, want)
	}

	for _, tc := range []struct {
		name   string
		purl   string
		before PackageRow
	}{{"confirmed PUBLIC", confirmed.PURL, confirmedBefore}, {"existing UNKNOWN", unknown.PURL, unknownBefore}} {
		got, ok, err := pg.GetPackage(ctx, tc.purl)
		if err != nil || !ok {
			t.Fatalf("%s: GetPackage ok=%t err=%v", tc.name, ok, err)
		}
		if !reflect.DeepEqual(got, tc.before) {
			t.Fatalf("%s: registration touched an existing row:\nbefore %+v\nafter  %+v", tc.name, tc.before, got)
		}
	}
	for i := 2; i <= 5; i++ {
		want := rowFor(i, "UNKNOWN", time.Time{})
		got, ok, err := pg.GetPackage(ctx, want.PURL)
		if err != nil || !ok {
			t.Fatalf("%s: not registered: ok=%t err=%v", want.PURL, ok, err)
		}
		if got.Ecosystem != want.Ecosystem || got.Name != want.Name || got.Version != want.Version ||
			got.Major != want.Major || got.Publicness != "UNKNOWN" || !got.CheckedAt.IsZero() ||
			got.FirstSeen.IsZero() || !got.FirstSeen.Equal(got.LastSeen) {
			t.Fatalf("%s: registered row = %+v, want a fresh UNKNOWN row", want.PURL, got)
		}
	}

	before = classStat(t, pg.PoolStats(), "background").Acquired
	if err := pg.RegisterPackages(ctx, nil); err != nil {
		t.Fatalf("empty registration page: %v", err)
	}
	if after := classStat(t, pg.PoolStats(), "background").Acquired; after != before {
		t.Fatalf("empty registration page acquired a connection: before=%d after=%d", before, after)
	}
}

// The race itself, run for real: registration pages keep landing while the
// registry check confirms the release PUBLIC. Whatever the interleaving,
// the confirmation is the last word, because registration never has one.
func TestIntegrationRegisterPackagesRacingAConfirmationKeepsItPublic(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	checked := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	row := PackageRow{
		PURL: "pkg:npm/raced@1.0.0", Ecosystem: "npm", Name: "raced", Version: "1.0.0",
		Major: "1", Publicness: "UNKNOWN",
	}

	const registrations = 40
	errs := make(chan error, registrations+1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < registrations; i++ {
			if err := pg.RegisterPackages(ctx, []PackageRow{row}); err != nil {
				errs <- fmt.Errorf("registration %d: %w", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		public := row
		public.Publicness, public.CheckedAt = "PUBLIC", checked
		if err := pg.UpsertPackage(ctx, public); err != nil {
			errs <- fmt.Errorf("confirmation: %w", err)
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	got, ok, err := pg.GetPackage(ctx, row.PURL)
	if err != nil || !ok {
		t.Fatalf("GetPackage ok=%t err=%v", ok, err)
	}
	if got.Publicness != "PUBLIC" || !got.CheckedAt.Equal(checked) {
		t.Fatalf("a registration page downgraded the confirmed release: %+v", got)
	}
}
