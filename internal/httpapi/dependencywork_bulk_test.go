package httpapi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// dependencyAPI is a handler over the given store with the publicness gate
// ON, which is the configuration confirmDependencyWork runs in.
func dependencyAPI(store serverstore.Store, checker PublicnessChecker) *api {
	return &api{d: Deps{Store: store, Checker: checker, Cfg: serverstore.ServerConfig{PublicCheck: "registry"}}}
}

// dependencyWindow is one poll's candidate window: n DEPENDENCY coordinates
// on the sample axis, with a few WANTED rows and dependency-axis rows mixed
// in so the pass-through cases are covered too.
func dependencyWindow(n int) []serverstore.WantedRow {
	out := make([]serverstore.WantedRow, 0, n+5)
	for i := 0; i < n; i++ {
		out = append(out, serverstore.WantedRow{
			Ecosystem: "npm", Name: fmt.Sprintf("dep-%04d", i), Version: "1.0.0",
			Kind: "DEPENDENCY", Axis: serverstore.AuthoringAxisSample, Score: int64(n - i),
		})
		if i%20 == 0 {
			out = append(out, serverstore.WantedRow{
				Ecosystem: "npm", Name: fmt.Sprintf("wanted-%04d", i), Version: "2.0.0",
				Kind: "WANTED", Axis: serverstore.AuthoringAxisSample, Score: int64(n - i),
			})
		}
		if i%25 == 0 {
			out = append(out, serverstore.WantedRow{
				Ecosystem: "npm", Name: fmt.Sprintf("dep-%04d", i), Version: "1.0.0",
				Kind: "DEPENDENCY", Axis: serverstore.AuthoringAxisDependency, Score: int64(n - i),
			})
		}
	}
	return out
}

// registerDependencies gives the first `public` dependency coordinates a
// PUBLIC row and the next `unknown` an UNKNOWN row; the rest have no row at
// all, which is the state a lockfile edge is born in.
func registerDependencies(t *testing.T, store *serverstore.Fake, public, unknown int) {
	t.Helper()
	for i := 0; i < public+unknown; i++ {
		publicness := scanner.PublicnessPublic
		if i >= public {
			publicness = scanner.PublicnessUnknown
		}
		if err := store.UpsertPackage(context.Background(), serverstore.PackageRow{
			PURL: fmt.Sprintf("pkg:npm/dep-%04d@1.0.0", i), Ecosystem: "npm",
			Name: fmt.Sprintf("dep-%04d", i), Version: "1.0.0", Major: "1", Publicness: publicness,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// One poll of /v1/authoring/work/next asked the packages table once per
// DEPENDENCY candidate to learn which coordinates it had already confirmed:
// a window of a few hundred was a few hundred pool checkouts on the endpoint
// the whole fleet polls several times a minute (#174). The question is a set
// question -- which of these purls have a PUBLIC row -- and the answer must
// be the same whichever contract the store offers.
func TestDependencyWorkConfirmsKnownCoordinatesInOneBulkLookup(t *testing.T) {
	const n, public, unknown = 60, 50, 5
	confirmed := map[string]string{
		"pkg:npm/dep-0050@1.0.0": scanner.PublicnessPublic,
		"pkg:npm/dep-0052@1.0.0": scanner.PublicnessPublic,
	}
	window := dependencyWindow(n)

	rowFake := serverstore.NewFake()
	registerDependencies(t, rowFake, public, unknown)
	rowCounter := newStoreCallCounter(rowFake)
	rowChecker := &scriptedRegistry{verdicts: confirmed}
	rowOut := dependencyAPI(&rowAtATimeAPIStore{rowCounter}, rowChecker).confirmDependencyWork(t.Context(), window)

	bulkFake := serverstore.NewFake()
	registerDependencies(t, bulkFake, public, unknown)
	bulkCounter := newStoreCallCounter(bulkFake)
	bulkChecker := &scriptedRegistry{verdicts: confirmed}
	bulkOut := dependencyAPI(&bulkAPIStore{bulkCounter}, bulkChecker).confirmDependencyWork(t.Context(), window)

	if !reflect.DeepEqual(rowOut, bulkOut) {
		t.Fatalf("the bulk lookup changed which work is handed out:\nrow-at-a-time: %v\nbulk:          %v", rowOut, bulkOut)
	}
	// Sanity on the shape of the answer itself: every registered PUBLIC row
	// passes without a probe, the two the registry confirmed pass, and the
	// unconfirmed remainder is dropped from this pass.
	wantKept := public + 2 + 3 + 3 // public rows, confirmed probes, WANTED, dependency-axis
	if len(rowOut) != wantKept {
		t.Fatalf("kept %d candidates, want %d: %v", len(rowOut), wantKept, rowOut)
	}
	if rowChecker.count() != bulkChecker.count() || rowChecker.count() > maxDependencyProbesPerRequest {
		t.Fatalf("registry probes row-at-a-time=%d bulk=%d, want equal and at most %d",
			rowChecker.count(), bulkChecker.count(), maxDependencyProbesPerRequest)
	}

	t.Logf("window=%d dependency candidates: row-at-a-time GetPackage=%d; bulk PackagesByPURL=%d GetPackage=%d",
		n, rowCounter.count("GetPackage"), bulkCounter.count("PackagesByPURL"), bulkCounter.count("GetPackage"))
	if got, want := rowCounter.count("GetPackage"), n; got != want {
		t.Fatalf("row-at-a-time package reads = %d, want %d (one per dependency candidate)", got, want)
	}
	if got := bulkCounter.count("GetPackage"); got != 0 {
		t.Fatalf("bulk store still read %d package rows one at a time", got)
	}
	if got, want := bulkCounter.count("PackagesByPURL"), 1; got != want {
		t.Fatalf("bulk package lookups = %d, want %d", got, want)
	}
	if got, want := bulkCounter.sizes("PackagesByPURL"), []int{n}; !equalIntSlices(got, want) {
		t.Fatalf("bulk lookup asked about %v purls, want %v: dependency candidates only, each once", got, want)
	}
}

// The page is BOUNDED. One unbounded array parameter is the same work handed
// to the database in one breath, not a smaller amount of it.
func TestDependencyWorkBulkLookupIsPaged(t *testing.T) {
	const n = 2*dependencyLookupBatch + 7
	fake := serverstore.NewFake()
	counter := newStoreCallCounter(fake)
	checker := &scriptedRegistry{verdicts: map[string]string{}}

	dependencyAPI(&bulkAPIStore{counter}, checker).confirmDependencyWork(t.Context(), dependencyWindow(n))

	if got, want := counter.sizes("PackagesByPURL"), []int{dependencyLookupBatch, dependencyLookupBatch, 7}; !equalIntSlices(got, want) {
		t.Fatalf("bulk lookup pages = %v, want %v", got, want)
	}
	if got := checker.count(); got != maxDependencyProbesPerRequest {
		t.Fatalf("registry probes = %d, want exactly the per-request bound %d", got, maxDependencyProbesPerRequest)
	}
}

// A store that cannot answer is not a store that answered "unknown" for free:
// the old contract fell through to a bounded registry probe when a row read
// failed, and the bulk contract must land in the same place -- same work
// kept, same number of probes -- rather than either dropping the whole window
// or probing all of it.
func TestDependencyWorkBulkLookupFailureMatchesRowAtATimeFailure(t *testing.T) {
	const n = 30
	window := dependencyWindow(n)
	pool := errors.New("pool exhausted")

	rowCounter := newStoreCallCounter(serverstore.NewFake())
	rowCounter.readErr = pool
	rowChecker := &scriptedRegistry{verdicts: map[string]string{"pkg:npm/dep-0001@1.0.0": scanner.PublicnessPublic}}
	rowOut := dependencyAPI(&rowAtATimeAPIStore{rowCounter}, rowChecker).confirmDependencyWork(t.Context(), window)

	bulkCounter := newStoreCallCounter(serverstore.NewFake())
	bulkCounter.readErr = pool
	bulkChecker := &scriptedRegistry{verdicts: map[string]string{"pkg:npm/dep-0001@1.0.0": scanner.PublicnessPublic}}
	bulkOut := dependencyAPI(&bulkAPIStore{bulkCounter}, bulkChecker).confirmDependencyWork(t.Context(), window)

	if !reflect.DeepEqual(rowOut, bulkOut) {
		t.Fatalf("a failed bulk lookup changed the answer:\nrow-at-a-time: %v\nbulk:          %v", rowOut, bulkOut)
	}
	if rowChecker.count() != bulkChecker.count() || bulkChecker.count() != maxDependencyProbesPerRequest {
		t.Fatalf("registry probes row-at-a-time=%d bulk=%d, want both %d", rowChecker.count(), bulkChecker.count(), maxDependencyProbesPerRequest)
	}
	if got := bulkCounter.count("GetPackage"); got != 0 {
		t.Fatalf("bulk store fell back to %d row reads after its page failed; the answer to a failed page is a bounded probe, not a second corpus read", got)
	}
}
