package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// registryServer is the API over one of the two store contracts, with the
// Fake underneath both so the answers can be compared byte for byte.
func registryServer(t *testing.T, bulk bool) (*httptest.Server, *serverstore.Fake, *storeCallCounter, *clock) {
	t.Helper()
	var counter *storeCallCounter
	srv, store, ck := newTestServer(t, func(d *Deps) {
		counter = newStoreCallCounter(d.Store.(*serverstore.Fake))
		if bulk {
			d.Store = &bulkAPIStore{counter}
		} else {
			d.Store = &rowAtATimeAPIStore{counter}
		}
	})
	return srv, store, counter, ck
}

// seedVersions registers n releases of one package, each seen a second
// later than the last so the version listing has a definite order, and
// materializes a family snapshot on every third release. Every release also
// carries a package-level snapshot, which the family endpoint must not
// confuse for evidence about the symbol.
func seedVersions(t *testing.T, store *serverstore.Fake, ck *clock, n int, family string) []string {
	t.Helper()
	ctx := context.Background()
	var withEvidence []string
	for i := 0; i < n; i++ {
		ck.t = testNow.Add(time.Duration(i) * time.Second)
		purl := fmt.Sprintf("pkg:npm/many@1.%d.0", i)
		if err := store.UpsertPackage(ctx, serverstore.PackageRow{
			PURL: purl, Ecosystem: "npm", Name: "many", Version: fmt.Sprintf("1.%d.0", i),
			Major: "1", Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutSnapshot(ctx, purl, "", fmt.Sprintf(`{"purl":%q,"symbol":"","rows":[]}`, purl)); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			if err := store.PutSnapshot(ctx, purl, family, fmt.Sprintf(`{"purl":%q,"symbol":%q,"rows":[{"i":%d}]}`, purl, family, i)); err != nil {
				t.Fatal(err)
			}
			withEvidence = append(withEvidence, purl)
		}
	}
	return withEvidence
}

func getRaw(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// GET /v1/registry/symbols/{eco}/{pkg}/{family} read one snapshot per
// version of the package: a library with three hundred releases was three
// hundred interactive checkouts for one public read (#174). The bulk form
// must return exactly the same document -- the same snapshots in the same
// version order, and 404 when there is no evidence at all.
func TestRegistrySymbolReadsSnapshotsInOneBulkLookup(t *testing.T) {
	const n = 120
	const family = "many.call"

	rowSrv, rowStore, rowCounter, rowClock := registryServer(t, false)
	want := seedVersions(t, rowStore, rowClock, n, family)
	bulkSrv, bulkStore, bulkCounter, bulkClock := registryServer(t, true)
	seedVersions(t, bulkStore, bulkClock, n, family)

	rowStatus, rowBody := getRaw(t, rowSrv.URL+"/v1/registry/symbols/npm/many/"+family)
	bulkStatus, bulkBody := getRaw(t, bulkSrv.URL+"/v1/registry/symbols/npm/many/"+family)
	if rowStatus != http.StatusOK || bulkStatus != http.StatusOK {
		t.Fatalf("status row-at-a-time=%d bulk=%d, want 200", rowStatus, bulkStatus)
	}
	if rowBody != bulkBody {
		t.Fatalf("the bulk read changed the document:\nrow-at-a-time: %s\nbulk:          %s", rowBody, bulkBody)
	}

	var doc struct {
		Snapshots []struct {
			PURL     string         `json:"purl"`
			Snapshot map[string]any `json:"snapshot"`
		} `json:"snapshots"`
	}
	decodeBody(t, bulkBody, &doc)
	if len(doc.Snapshots) != len(want) {
		t.Fatalf("snapshots = %d, want %d (one per version with family evidence)", len(doc.Snapshots), len(want))
	}
	// The listing is newest-seen first; the document must follow it.
	for i, snap := range doc.Snapshots {
		if wantPURL := want[len(want)-1-i]; snap.PURL != wantPURL {
			t.Fatalf("snapshot %d = %s, want %s: version order was not preserved", i, snap.PURL, wantPURL)
		}
		if snap.Snapshot["symbol"] != family {
			t.Fatalf("snapshot %d carries symbol %v, want the family %q", i, snap.Snapshot["symbol"], family)
		}
	}

	t.Logf("%d versions: row-at-a-time GetSnapshot=%d; bulk SnapshotsForPURLs=%d GetSnapshot=%d",
		n, rowCounter.count("GetSnapshot"), bulkCounter.count("SnapshotsForPURLs"), bulkCounter.count("GetSnapshot"))
	if got, want := rowCounter.count("GetSnapshot"), n; got != want {
		t.Fatalf("row-at-a-time snapshot reads = %d, want %d", got, want)
	}
	if got := bulkCounter.count("GetSnapshot"); got != 0 {
		t.Fatalf("bulk store still read %d snapshots one at a time", got)
	}
	if got, want := bulkCounter.sizes("SnapshotsForPURLs"), []int{n}; !equalIntSlices(got, want) {
		t.Fatalf("bulk snapshot pages = %v, want %v", got, want)
	}
}

// No evidence for the family is still a 404, and it still costs one page.
func TestRegistrySymbolWithoutEvidenceIs404UnderBothContracts(t *testing.T) {
	rowSrv, rowStore, _, rowClock := registryServer(t, false)
	seedVersions(t, rowStore, rowClock, 9, "many.call")
	bulkSrv, bulkStore, bulkCounter, bulkClock := registryServer(t, true)
	seedVersions(t, bulkStore, bulkClock, 9, "many.call")

	rowStatus, rowBody := getRaw(t, rowSrv.URL+"/v1/registry/symbols/npm/many/many.none")
	bulkStatus, bulkBody := getRaw(t, bulkSrv.URL+"/v1/registry/symbols/npm/many/many.none")
	if rowStatus != http.StatusNotFound || bulkStatus != http.StatusNotFound || rowBody != bulkBody {
		t.Fatalf("no evidence: row-at-a-time=%d %s bulk=%d %s, want identical 404s", rowStatus, rowBody, bulkStatus, bulkBody)
	}
	if got, want := bulkCounter.sizes("SnapshotsForPURLs"), []int{9}; !equalIntSlices(got, want) {
		t.Fatalf("bulk snapshot pages = %v, want %v", got, want)
	}
}

// The page is BOUNDED: a package with more releases than one page holds is
// read a page at a time, not as one array parameter the size of its history.
func TestRegistrySymbolBulkLookupIsPaged(t *testing.T) {
	const n = 2*registrySnapshotBatch + 3
	srv, store, counter, ck := registryServer(t, true)
	seedVersions(t, store, ck, n, "many.call")

	status, _ := getRaw(t, srv.URL+"/v1/registry/symbols/npm/many/many.call")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got, want := counter.sizes("SnapshotsForPURLs"), []int{registrySnapshotBatch, registrySnapshotBatch, 3}; !equalIntSlices(got, want) {
		t.Fatalf("bulk snapshot pages = %v, want %v", got, want)
	}
}

// A store that cannot answer is not a package with no evidence. Under the
// old read every failed version was silently skipped, so a busy pool became
// "no evidence for this symbol" -- a false 404 a client would cache. Both
// contracts must now report the backpressure for what it is.
func TestRegistrySymbolStoreFailureIsNotAFalse404(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		srv, store, counter, ck := registryServer(t, bulk)
		seedVersions(t, store, ck, 6, "many.call")
		counter.readErr = serverstore.ErrPoolBusy

		status, body := getRaw(t, srv.URL+"/v1/registry/symbols/npm/many/many.call")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("bulk=%t: a busy pool answered %d %s, want 503", bulk, status, body)
		}
	}
}
