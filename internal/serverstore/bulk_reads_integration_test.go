package serverstore

import (
	"context"
	"fmt"
	"reflect"
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
// registry has never seen. The bulk form must leave the table in the state
// the sequential UpsertPackage calls would have -- same columns, including
// a NULL and a set checked_at, the same conflict rule on a row that already
// exists, first_seen untouched by the update -- in one checkout per page,
// and it must not choke on a purl repeated within the page.
func TestIntegrationUpsertPackagesMatchesUpsertPackageInOneCheckout(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	checked := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	rowFor := func(prefix string, i int) PackageRow {
		row := PackageRow{
			PURL: fmt.Sprintf("pkg:npm/%s%03d@2.%d.0", prefix, i, i), Ecosystem: "npm",
			Name: fmt.Sprintf("%s%03d", prefix, i), Version: fmt.Sprintf("2.%d.0", i), Major: "2",
		}
		switch i % 3 {
		case 0:
			row.Publicness = "UNKNOWN"
		case 1:
			row.Publicness, row.CheckedAt = "PUBLIC", checked
		case 2:
			row.Publicness = "" // the store's default, UNKNOWN
		}
		return row
	}
	const n = 9
	var seq, bulk []PackageRow
	for i := 0; i < n; i++ {
		seq = append(seq, rowFor("seq", i))
		bulk = append(bulk, rowFor("bulk", i))
	}
	for _, row := range seq {
		if err := pg.UpsertPackage(ctx, row); err != nil {
			t.Fatalf("UpsertPackage %s: %v", row.PURL, err)
		}
	}
	before := classStat(t, pg.PoolStats(), "background").Acquired
	if err := pg.UpsertPackages(ctx, bulk); err != nil {
		t.Fatalf("UpsertPackages: %v", err)
	}
	after := classStat(t, pg.PoolStats(), "background").Acquired
	if got, want := after-before, uint64(1); got != want {
		t.Fatalf("package write checkouts = %d, want %d", got, want)
	}

	// Same columns, prefix aside. The clocks are the server's, so they are
	// compared for shape (set, and first_seen == last_seen on a fresh row)
	// rather than for value.
	sameShape := func(a, b PackageRow) bool {
		return a.Ecosystem == b.Ecosystem && a.Version == b.Version && a.Major == b.Major &&
			a.Publicness == b.Publicness && a.CheckedAt.Equal(b.CheckedAt) &&
			!a.FirstSeen.IsZero() && !b.FirstSeen.IsZero() &&
			a.FirstSeen.Equal(a.LastSeen) && b.FirstSeen.Equal(b.LastSeen)
	}
	for i := 0; i < n; i++ {
		s, sOK, err := pg.GetPackage(ctx, seq[i].PURL)
		if err != nil || !sOK {
			t.Fatalf("GetPackage %s: ok=%t err=%v", seq[i].PURL, sOK, err)
		}
		b, bOK, err := pg.GetPackage(ctx, bulk[i].PURL)
		if err != nil || !bOK {
			t.Fatalf("GetPackage %s: ok=%t err=%v", bulk[i].PURL, bOK, err)
		}
		if !sameShape(s, b) {
			t.Fatalf("row %d differs between contracts:\nsequential %+v\nbulk       %+v", i, s, b)
		}
	}

	// The conflict rule: publicness and checked_at replaced, last_seen moved,
	// first_seen kept -- through both contracts.
	seqBefore, _, _ := pg.GetPackage(ctx, seq[0].PURL)
	bulkBefore, _, _ := pg.GetPackage(ctx, bulk[0].PURL)
	later := checked.Add(time.Hour)
	update := func(row PackageRow) PackageRow {
		row.Publicness, row.CheckedAt = "PRIVATE", later
		return row
	}
	if err := pg.UpsertPackage(ctx, update(seq[0])); err != nil {
		t.Fatal(err)
	}
	// The repeated purl is the bulk analogue of calling UpsertPackage twice:
	// the later value is the one that lands.
	stale := update(bulk[0])
	stale.Publicness = "PUBLIC"
	if err := pg.UpsertPackages(ctx, []PackageRow{stale, update(bulk[0])}); err != nil {
		t.Fatalf("UpsertPackages with a repeated purl: %v", err)
	}
	for _, tc := range []struct {
		name   string
		purl   string
		before PackageRow
	}{{"sequential", seq[0].PURL, seqBefore}, {"bulk", bulk[0].PURL, bulkBefore}} {
		got, _, err := pg.GetPackage(ctx, tc.purl)
		if err != nil {
			t.Fatal(err)
		}
		if got.Publicness != "PRIVATE" || !got.CheckedAt.Equal(later) {
			t.Fatalf("%s: conflict did not replace publicness/checked_at: %+v", tc.name, got)
		}
		if !got.FirstSeen.Equal(tc.before.FirstSeen) {
			t.Fatalf("%s: conflict moved first_seen from %s to %s", tc.name, tc.before.FirstSeen, got.FirstSeen)
		}
		if got.LastSeen.Before(tc.before.LastSeen) {
			t.Fatalf("%s: conflict did not refresh last_seen: %s -> %s", tc.name, tc.before.LastSeen, got.LastSeen)
		}
	}

	before = classStat(t, pg.PoolStats(), "background").Acquired
	if err := pg.UpsertPackages(ctx, nil); err != nil {
		t.Fatalf("empty package write: %v", err)
	}
	if after := classStat(t, pg.PoolStats(), "background").Acquired; after != before {
		t.Fatalf("empty package write acquired a connection: before=%d after=%d", before, after)
	}
}
