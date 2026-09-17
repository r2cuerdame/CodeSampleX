package serverstore

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type releaseSnapshotStore interface {
	PutSnapshot(ctx context.Context, purl, symbol, snapshotJSON string) error
	GetSnapshotsForPURL(ctx context.Context, purl string) ([]SnapshotRow, error)
	GetSnapshotsForPURLs(ctx context.Context, purls []string) ([]SnapshotRow, error)
}

// runGetSnapshotsForPURLsContract plays one corpus through a store and
// checks that the bulk release read (#426: one gated round trip for a
// package page's whole release window) answers exactly the concatenation
// of the per-release reads it replaces -- every symbol of every requested
// release, nothing of a release that was not asked for, ordered by
// (purl, symbol), tolerant of duplicates and blanks in the request, and
// empty for an empty request.
func runGetSnapshotsForPURLsContract(t *testing.T, store releaseSnapshotStore) {
	t.Helper()
	ctx := context.Background()

	var window []string
	for i := 0; i < 6; i++ {
		purl := fmt.Sprintf("pkg:npm/snap_x@1.%d.0", i)
		window = append(window, purl)
		for _, symbol := range []string{"", "snap.call", "snap.other"} {
			if i%2 == 1 && symbol == "snap.other" {
				continue // odd releases carry two rows, even ones three
			}
			js := fmt.Sprintf(`{"i": %d, "purl": %q, "symbol": %q}`, i, purl, symbol)
			if err := store.PutSnapshot(ctx, purl, symbol, js); err != nil {
				t.Fatalf("PutSnapshot %s %q: %v", purl, symbol, err)
			}
		}
	}
	// A release outside the window, and a release whose name differs from a
	// requested one only where a LIKE wildcard would match: neither may
	// come back.
	for _, decoy := range []string{"pkg:npm/snap_x@2.0.0", "pkg:npm/snapXx@1.0.0"} {
		if err := store.PutSnapshot(ctx, decoy, "", `{"decoy": true}`); err != nil {
			t.Fatalf("PutSnapshot decoy %s: %v", decoy, err)
		}
	}

	var want []SnapshotRow
	for _, purl := range window {
		rows, err := store.GetSnapshotsForPURL(ctx, purl)
		if err != nil {
			t.Fatalf("GetSnapshotsForPURL %s: %v", purl, err)
		}
		if len(rows) == 0 {
			t.Fatalf("GetSnapshotsForPURL %s returned nothing; the corpus never reached the store", purl)
		}
		want = append(want, rows...)
	}

	request := append([]string{"", "pkg:npm/snap-absent@9.9.9"}, window...)
	request = append(request, window[0]) // a duplicate must not double a row
	got, err := store.GetSnapshotsForPURLs(ctx, request)
	if err != nil {
		t.Fatalf("GetSnapshotsForPURLs: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetSnapshotsForPURLs disagrees with the per-release reads:\n got %+v\nwant %+v", got, want)
	}
	if len(got) != 15 {
		t.Fatalf("bulk read returned %d rows, want 15 (3+2+3+2+3+2)", len(got))
	}
	for _, row := range got {
		if row.PURL == "pkg:npm/snapXx@1.0.0" || row.PURL == "pkg:npm/snap_x@2.0.0" {
			t.Fatalf("a release nobody asked for came back: %+v", row)
		}
	}

	if got, err := store.GetSnapshotsForPURLs(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty request = %v, err=%v; want nothing and no error", got, err)
	}
	if got, err := store.GetSnapshotsForPURLs(ctx, []string{"pkg:npm/snap-absent@9.9.9"}); err != nil || len(got) != 0 {
		t.Fatalf("absent release = %v, err=%v; want nothing and no error", got, err)
	}
}

func TestFakeGetSnapshotsForPURLsMatchesPerReleaseReads(t *testing.T) {
	runGetSnapshotsForPURLsContract(t, NewFake())
}

// The PG form must also be ONE checkout for the whole window: the point of
// the bulk read is that a cold package page stands at the admission gate
// once, not once per release.
func TestIntegrationGetSnapshotsForPURLsMatchesPerReleaseReadsInOneCheckout(t *testing.T) {
	pg := openTestPG(t)
	runGetSnapshotsForPURLsContract(t, pg)

	ctx := context.Background()
	page := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		page = append(page, fmt.Sprintf("pkg:npm/snap_x@1.%d.0", i))
	}
	before := classStat(t, pg.PoolStats(), "background").Acquired
	if _, err := pg.GetSnapshotsForPURLs(ctx, page); err != nil {
		t.Fatalf("GetSnapshotsForPURLs: %v", err)
	}
	if got := classStat(t, pg.PoolStats(), "background").Acquired - before; got != 1 {
		t.Fatalf("six-release window took %d checkouts, want 1", got)
	}
	before = classStat(t, pg.PoolStats(), "background").Acquired
	if _, err := pg.GetSnapshotsForPURLs(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := classStat(t, pg.PoolStats(), "background").Acquired - before; got != 0 {
		t.Fatalf("an empty window acquired %d connections, want 0", got)
	}
}
