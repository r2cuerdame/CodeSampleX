package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// searchServer is the API over one of the two store contracts, with the Fake
// underneath both so the answers can be compared byte for byte.
func searchServer(t *testing.T, bulk bool) (*httptest.Server, *serverstore.Fake, *storeCallCounter) {
	t.Helper()
	var counter *storeCallCounter
	srv, store, _ := newTestServer(t, func(d *Deps) {
		counter = newStoreCallCounter(d.Store.(*serverstore.Fake))
		if bulk {
			d.Store = &bulkAPIStore{counter}
		} else {
			d.Store = &rowAtATimeAPIStore{counter}
		}
	})
	return srv, store, counter
}

// seedSearchCandidates publishes n samples about axios, each with two
// signed-shape receipts that resolved the package, so grading reads
// receipts for every candidate the filters let through.
func seedSearchCandidates(t *testing.T, store *serverstore.Fake, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		manifest := testManifest()
		manifest.Case.Goal = fmt.Sprintf("post JSON with axios, variant %d", i)
		sampleID := fmt.Sprintf("sha256:%064x", 500000+i)
		if err := store.SaveSample(ctx, serverstore.SampleRow{
			SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
			Status: "PUBLISHED", License: "MIT-0", SizeBytes: 512, CreatedAt: testNow,
		}); err != nil {
			t.Fatal(err)
		}
		for r := 0; r < 2; r++ {
			receipt := domain.VerificationReceipt{
				SchemaVersion: 2, SampleID: sampleID, PeerID: fmt.Sprintf("peer-%d", r),
				EnvironmentHash: nodeEnv("esm").Normalize().Hash(), Environment: nodeEnv("esm"),
				Stages: map[string]string{
					"resolve": string(domain.ResultPass), "contract": string(domain.ResultPass),
				},
				ResolvedPackages: []string{"pkg:npm/axios@1.12.0"},
			}
			if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
				ReceiptID: fmt.Sprintf("receipt-%d-%d", i, r), SampleID: sampleID, PeerID: receipt.PeerID,
				ContractResult: "PASS", CreatedAt: testNow,
				ReceiptJSON: string(domain.MustCanonicalJSON(receipt)),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func postSearch(t *testing.T, url string, body any) (int, string) {
	t.Helper()
	resp := postJSON(t, url, body, nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func searchAxios(symbols ...string) domain.SearchRequest {
	return domain.SearchRequest{
		SchemaVersion: 2, Query: "post JSON with axios", Packages: []string{"pkg:npm/axios@1.12.0"},
		Symbols: symbols, Environment: nodeEnv("esm"), Limit: 5,
	}
}

// Scoring read the receipts of every candidate one sample at a time: a
// search over a well-covered package was up to five hundred interactive
// checkouts for one answer (#174). Reading them a bounded page at a time
// must not change the answer in any byte -- same results, same grades, same
// evidence -- because the rows are the same rows in the same order.
func TestSearchReadsCandidateReceiptsInBoundedPages(t *testing.T) {
	const n = 60
	rowSrv, rowStore, rowCounter := searchServer(t, false)
	seedSearchCandidates(t, rowStore, n)
	bulkSrv, bulkStore, bulkCounter := searchServer(t, true)
	seedSearchCandidates(t, bulkStore, n)

	rowStatus, rowBody := postSearch(t, rowSrv.URL+"/v2/search", searchAxios())
	bulkStatus, bulkBody := postSearch(t, bulkSrv.URL+"/v2/search", searchAxios())
	if rowStatus != http.StatusOK || bulkStatus != http.StatusOK {
		t.Fatalf("status row-at-a-time=%d bulk=%d, want 200", rowStatus, bulkStatus)
	}
	if rowBody != bulkBody {
		t.Fatalf("the bulk receipt read changed the answer:\nrow-at-a-time: %s\nbulk:          %s", rowBody, bulkBody)
	}
	var resp domain.SearchResponse
	decodeBody(t, bulkBody, &resp)
	if resp.Miss || len(resp.Results) == 0 || resp.Results[0].Evidence.ContractPasses != 2 {
		t.Fatalf("the answer did not grade from receipts: %+v", resp)
	}

	t.Logf("%d candidates: row-at-a-time ReceiptsForSample=%d; bulk ReceiptsForSamples=%v ReceiptsForSample=%d",
		n, rowCounter.count("ReceiptsForSample"), bulkCounter.sizes("ReceiptsForSamples"), bulkCounter.count("ReceiptsForSample"))
	if got, want := rowCounter.count("ReceiptsForSample"), n; got != want {
		t.Fatalf("row-at-a-time receipt reads = %d, want %d", got, want)
	}
	if got := bulkCounter.count("ReceiptsForSample"); got != 0 {
		t.Fatalf("bulk store still read %d receipt histories one at a time", got)
	}
	if got, want := bulkCounter.sizes("ReceiptsForSamples"), []int{n}; !equalIntSlices(got, want) {
		t.Fatalf("receipt pages = %v, want %v", got, want)
	}
}

// The pages are BOUNDED and follow candidate order, so a candidate window
// the size of the search cap is a handful of checkouts, not one parameter
// the size of the window.
func TestSearchReceiptPagesAreBounded(t *testing.T) {
	const n = 2*searchReceiptBatch + 5
	srv, store, counter := searchServer(t, true)
	seedSearchCandidates(t, store, n)

	status, _ := postSearch(t, srv.URL+"/v2/search", searchAxios())
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got, want := counter.sizes("ReceiptsForSamples"), []int{searchReceiptBatch, searchReceiptBatch, 5}; !equalIntSlices(got, want) {
		t.Fatalf("receipt pages = %v, want %v", got, want)
	}
}

// Receipts are read only when a candidate reaches grading. A request every
// candidate fails before that point -- here, a symbol none of them declare --
// must not read a single receipt under either contract: the page is fetched
// on first need, not on arrival.
func TestSearchDoesNotReadReceiptsForCandidatesTheFiltersReject(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		srv, store, counter := searchServer(t, bulk)
		seedSearchCandidates(t, store, 30)
		postSearch(t, srv.URL+"/v2/search", searchAxios("axios.nope"))
		if got := counter.count("ReceiptsForSample") + counter.count("ReceiptsForSamples"); got != 0 {
			t.Fatalf("bulk=%t: %d receipt reads for a request no candidate survived", bulk, got)
		}
	}
}

// A page the store refuses is the same failure a refused row was: the
// request reports the backpressure rather than grading without evidence.
func TestSearchReceiptPageFailureIsReportedLikeARowFailure(t *testing.T) {
	var statuses []string
	for _, bulk := range []bool{false, true} {
		srv, store, counter := searchServer(t, bulk)
		seedSearchCandidates(t, store, 12)
		counter.readErr = serverstore.ErrPoolBusy
		status, body := postSearch(t, srv.URL+"/v2/search", searchAxios())
		statuses = append(statuses, fmt.Sprintf("%d %s", status, strings.TrimSpace(body)))
		if status != http.StatusServiceUnavailable {
			t.Fatalf("bulk=%t: a busy pool answered %d %s, want 503", bulk, status, body)
		}
	}
	if statuses[0] != statuses[1] {
		t.Fatalf("the two contracts report the failure differently: %v", statuses)
	}
}
