package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
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
	seedSearchCandidatesWith(t, store, n, nil)
}

// searchCandidateID is the sample ID of the i-th seeded candidate. The Fake
// lists same-instant samples by ID, so seed order is candidate order.
func searchCandidateID(i int) string {
	return fmt.Sprintf("sha256:%064x", 500000+i)
}

// seedSearchCandidatesWith is seedSearchCandidates with a hand on each
// manifest before it is saved, so a test can decide which candidates the
// filters will let through.
func seedSearchCandidatesWith(t *testing.T, store *serverstore.Fake, n int, mutate func(i int, m *domain.SampleManifest)) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		manifest := testManifest()
		manifest.Case.Goal = fmt.Sprintf("post JSON with axios, variant %d", i)
		if mutate != nil {
			mutate(i, &manifest)
		}
		sampleID := searchCandidateID(i)
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

// declareRequestSymbolAt makes the candidates at the given seed positions
// declare a second symbol nobody else declares. A request for that symbol
// then rejects every other candidate at the symbol filter, before any
// receipt is needed.
func declareRequestSymbolAt(positions ...int) func(int, *domain.SampleManifest) {
	chosen := make(map[int]bool, len(positions))
	for _, i := range positions {
		chosen[i] = true
	}
	return func(i int, m *domain.SampleManifest) {
		if chosen[i] {
			m.Symbols = append(m.Symbols, "axios.request")
		}
	}
}

// The receipt pages were cut from ALL candidates, so one survivor pulled the
// receipt histories of up to searchReceiptBatch neighbours the filters had
// already rejected, and kept them for the rest of the request. The pages
// must be cut from the candidates that survive every pre-receipt filter:
// one survivor is one row, and the answer is byte-identical to the
// row-at-a-time contract.
func TestSearchReadsReceiptsOnlyForTheCandidatesThatSurviveTheFilters(t *testing.T) {
	const n = 100
	const survivor = 57
	rowSrv, rowStore, rowCounter := searchServer(t, false)
	seedSearchCandidatesWith(t, rowStore, n, declareRequestSymbolAt(survivor))
	bulkSrv, bulkStore, bulkCounter := searchServer(t, true)
	seedSearchCandidatesWith(t, bulkStore, n, declareRequestSymbolAt(survivor))

	rowStatus, rowBody := postSearch(t, rowSrv.URL+"/v2/search", searchAxios("axios.request"))
	bulkStatus, bulkBody := postSearch(t, bulkSrv.URL+"/v2/search", searchAxios("axios.request"))
	if rowStatus != http.StatusOK || bulkStatus != http.StatusOK {
		t.Fatalf("status row-at-a-time=%d bulk=%d, want 200", rowStatus, bulkStatus)
	}
	if rowBody != bulkBody {
		t.Fatalf("survivor-only receipt pages changed the answer:\nrow-at-a-time: %s\nbulk:          %s", rowBody, bulkBody)
	}
	var resp domain.SearchResponse
	decodeBody(t, bulkBody, &resp)
	if resp.Miss || len(resp.Results) != 1 || resp.Results[0].SampleID != searchCandidateID(survivor) ||
		resp.Results[0].Evidence.ContractPasses != 2 {
		t.Fatalf("the survivor was not the graded answer: %+v", resp)
	}

	t.Logf("%d candidates, 1 survivor: row-at-a-time ReceiptsForSample=%d; bulk ReceiptsForSamples=%v",
		n, rowCounter.count("ReceiptsForSample"), bulkCounter.sizes("ReceiptsForSamples"))
	if got := rowCounter.count("ReceiptsForSample"); got != 1 {
		t.Fatalf("row-at-a-time receipt reads = %d, want 1", got)
	}
	if got := bulkCounter.count("ReceiptsForSample"); got != 0 {
		t.Fatalf("bulk store still read %d receipt histories one at a time", got)
	}
	if got, want := bulkCounter.pageKeys("ReceiptsForSamples"), []string{searchCandidateID(survivor)}; !equalStringSlices(got, want) {
		t.Fatalf("receipt pages asked for %v, want only the survivor %v", got, want)
	}
}

// The pages are cut from survivors in candidate order and bounded by
// searchReceiptBatch, whatever the survivors' positions among the
// candidates: three survivors spread over three candidate pages are one
// read of three, and half of three hundred is a full page and a half.
func TestSearchReceiptPagesAreCutFromSurvivorsNotCandidates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		n         int
		survivors []int
		wantPages []int
	}{
		{name: "one survivor per candidate page", n: 2*searchReceiptBatch + 5, survivors: []int{0, searchReceiptBatch, 2*searchReceiptBatch + 4}, wantPages: []int{3}},
		{name: "every other candidate", n: 3 * searchReceiptBatch, survivors: everyOther(3 * searchReceiptBatch), wantPages: []int{searchReceiptBatch, searchReceiptBatch / 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, counter := searchServer(t, true)
			seedSearchCandidatesWith(t, store, tc.n, declareRequestSymbolAt(tc.survivors...))

			status, body := postSearch(t, srv.URL+"/v2/search", searchAxios("axios.request"))
			if status != http.StatusOK {
				t.Fatalf("status = %d %s, want 200", status, body)
			}
			var resp domain.SearchResponse
			decodeBody(t, body, &resp)
			if resp.Miss || len(resp.Results) == 0 {
				t.Fatalf("no survivor was graded: %+v", resp)
			}
			if got := counter.sizes("ReceiptsForSamples"); !equalIntSlices(got, tc.wantPages) {
				t.Fatalf("receipt pages = %v, want %v", got, tc.wantPages)
			}
			want := make([]string, 0, len(tc.survivors))
			for _, i := range tc.survivors {
				want = append(want, searchCandidateID(i))
			}
			sort.Strings(want)
			got := counter.pageKeys("ReceiptsForSamples")
			sort.Strings(got)
			if !equalStringSlices(got, want) {
				t.Fatalf("receipt pages asked for %d ids, want exactly the %d survivors", len(got), len(want))
			}
			if got := counter.count("ReceiptsForSample"); got != 0 {
				t.Fatalf("bulk store still read %d receipt histories one at a time", got)
			}
		})
	}
}

func everyOther(n int) []int {
	var out []int
	for i := 0; i < n; i += 2 {
		out = append(out, i)
	}
	return out
}

// An exact failure detour is decided from the SELECTED receipt variant of
// the candidate, after the pre-receipt cluster match. Splitting grading
// into a pre-receipt and a post-receipt phase must not move that decision:
// both contracts report the same exact match, with the same evidence.
func TestSearchExactFailureMatchIsTheSameUnderBothContracts(t *testing.T) {
	fingerprint := "sha256:" + strings.Repeat("ab", 32)
	seed := func(store *serverstore.Fake) {
		seedSearchCandidates(t, store, 12)
		if err := store.UpsertFailureCluster(context.Background(), serverstore.ClusterRow{
			Ecosystem: "npm", PackageName: "axios", Symbol: "axios.post", Stage: "PROJECT_COMPILE",
			ErrorFingerprint: fingerprint, ErrorCode: "ERR_REQUIRE_ESM", ObservationCount: 7,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rowSrv, rowStore, _ := searchServer(t, false)
	seed(rowStore)
	bulkSrv, bulkStore, bulkCounter := searchServer(t, true)
	seed(bulkStore)

	req := searchAxios()
	req.ErrorFingerprint = fingerprint
	rowStatus, rowBody := postSearch(t, rowSrv.URL+"/v2/search", req)
	bulkStatus, bulkBody := postSearch(t, bulkSrv.URL+"/v2/search", req)
	if rowStatus != http.StatusOK || bulkStatus != http.StatusOK {
		t.Fatalf("status row-at-a-time=%d bulk=%d, want 200", rowStatus, bulkStatus)
	}
	if rowBody != bulkBody {
		t.Fatalf("the two contracts disagree on the exact failure match:\nrow-at-a-time: %s\nbulk:          %s", rowBody, bulkBody)
	}
	var resp domain.SearchResponse
	decodeBody(t, bulkBody, &resp)
	if resp.Miss || len(resp.Results) == 0 || !resp.Results[0].ExactFailureMatched {
		t.Fatalf("the exact fingerprint match was not exposed: %+v", resp)
	}
	if got, want := bulkCounter.sizes("ReceiptsForSamples"), []int{12}; !equalIntSlices(got, want) {
		t.Fatalf("receipt pages = %v, want %v", got, want)
	}
}

// One page is resident at a time. Survivors are graded in candidate order,
// so once grading moves past a page that page's rows are never asked for
// again; keeping them would hold up to the whole window's receipt history
// for the rest of the request.
func TestSearchReceiptReaderKeepsOneResidentPage(t *testing.T) {
	const n = 2*searchReceiptBatch + 5
	fake := serverstore.NewFake()
	seedSearchCandidates(t, fake, n)
	counter := newStoreCallCounter(fake)
	a := &api{d: Deps{Store: &bulkAPIStore{counter}}}
	survivors := make([]searchCandidate, 0, n)
	for i := 0; i < n; i++ {
		survivors = append(survivors, searchCandidate{row: serverstore.SampleRow{SampleID: searchCandidateID(i)}})
	}
	reader := a.newSearchReceiptReader(survivors)
	ctx := context.Background()

	for _, i := range []int{0, 1, searchReceiptBatch - 1} {
		if _, err := reader.rowsFor(ctx, searchCandidateID(i)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := counter.sizes("ReceiptsForSamples"), []int{searchReceiptBatch}; !equalIntSlices(got, want) {
		t.Fatalf("first page: receipt pages = %v, want %v", got, want)
	}
	if _, err := reader.rowsFor(ctx, searchCandidateID(searchReceiptBatch)); err != nil {
		t.Fatal(err)
	}
	if got, want := counter.sizes("ReceiptsForSamples"), []int{searchReceiptBatch, searchReceiptBatch}; !equalIntSlices(got, want) {
		t.Fatalf("second page: receipt pages = %v, want %v", got, want)
	}
	if reader.resident != 1 || len(reader.rows) != searchReceiptBatch {
		t.Fatalf("after moving to page 1 the reader holds page %d with %d ids resident", reader.resident, len(reader.rows))
	}
	if _, err := reader.rowsFor(ctx, searchCandidateID(n-1)); err != nil {
		t.Fatal(err)
	}
	if got, want := counter.sizes("ReceiptsForSamples"), []int{searchReceiptBatch, searchReceiptBatch, 5}; !equalIntSlices(got, want) {
		t.Fatalf("last page: receipt pages = %v, want %v", got, want)
	}
	if reader.resident != 2 || len(reader.rows) != 5 {
		t.Fatalf("after moving to page 2 the reader holds page %d with %d ids resident", reader.resident, len(reader.rows))
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
