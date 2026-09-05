package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func passReceipt(t *testing.T, resolved []string, stages map[string]string) serverstore.ReceiptRow {
	t.Helper()
	body := map[string]any{
		"schemaVersion":    2,
		"stages":           stages,
		"resolvedPackages": resolved,
		"environment": map[string]any{
			"schemaVersion": 1, "ecosystem": "npm", "os": "linux", "arch": "x64",
			"libc": "musl", "runtime": "node", "runtimeVersion": "22",
			"virtualization": "container", "containerRuntime": "docker",
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return serverstore.ReceiptRow{
		ReceiptID: "receipt:1",
		// A REAL sample id. The short stand-in that used to be here is what
		// let the whole backfill be written against a batch production would
		// refuse: every one of 9,883 was rejected on the first apply because
		// "sha256:" plus 64 hex is 71 bytes and a bucket may be 64.
		SampleID:       "sha256:" + strings.Repeat("a", 64),
		PeerID:         "peer-farm-1",
		ReceiptJSON:    string(raw),
		ContractResult: "PASS",
		CreatedAt:      time.Date(2026, 8, 23, 4, 0, 0, 0, time.UTC),
	}
}

// Writing the sample and running it IS an execution, in an environment this
// network recorded rather than assumed. It was kept only as a verification,
// so a coordinate the farm had built itself showed a dash where its own runs
// belonged — 329 packages and 7,173 snapshot rows reading "never measured"
// about work we did.
//
// This does not merge the two classes. The batch carries the farm's own peer
// id and one project bucket per sample, so a reader sees one reporting peer
// and can tell exactly whose machine it was. What changes is that the run is
// recorded at all.
func TestAContractRunIsRecordedAsAnObservation(t *testing.T) {
	r := passReceipt(t, []string{"pkg:npm/axios@1.12.0", "pkg:npm/follow-redirects@1.15.6"},
		map[string]string{"resolve": "PASS", "compile": "SKIPPED", "load": "PASS", "contract": "PASS"})

	batches := ObservationsFromReceipt(r)
	if len(batches) == 0 {
		t.Fatal("a contract run produced no observation at all")
	}

	byPkg := map[string][]domain.ObservationBatch{}
	for _, b := range batches {
		byPkg[b.Package] = append(byPkg[b.Package], b)
		if b.AnonID != "peer-farm-1" {
			t.Errorf("anonId = %q, want the peer that actually ran it", b.AnonID)
		}
		if b.ProjectBucket == "" {
			t.Error("no project bucket: the run cannot be counted as one place")
		}
		if b.Environment.OS != "linux" || b.Environment.ContainerRuntime != "docker" {
			t.Errorf("environment lost: %+v", b.Environment)
		}
		if b.Epoch != "2026-08-23" {
			t.Errorf("epoch = %q, want the day the run happened", b.Epoch)
		}
		if b.Symbol != "" {
			t.Errorf("symbol = %q: a receipt says which packages ran, not which symbols", b.Symbol)
		}
	}
	for _, want := range []string{"pkg:npm/axios@1.12.0", "pkg:npm/follow-redirects@1.15.6"} {
		if len(byPkg[want]) == 0 {
			t.Errorf("no observation for %s, which the run resolved", want)
		}
	}

	// The stages have to land where the cube counts them, or the dash stays.
	stages := map[domain.Stage]bool{}
	for _, b := range byPkg["pkg:npm/axios@1.12.0"] {
		stages[b.Stage] = true
	}
	if !stages[domain.StageProjectTest] {
		t.Errorf("a contract PASS was not recorded as a run: stages %v", stages)
	}
	if stages[domain.StageProjectCompile] {
		t.Error("a SKIPPED compile was recorded as though it had run")
	}
}

// A failing contract is a run too, and its result is FAIL.
func TestAFailedContractIsRecordedAsAFailedRun(t *testing.T) {
	r := passReceipt(t, []string{"pkg:npm/axios@1.12.0"},
		map[string]string{"resolve": "PASS", "contract": "FAIL"})
	r.ContractResult = "FAIL"

	var sawFail bool
	for _, b := range ObservationsFromReceipt(r) {
		if b.Stage == domain.StageProjectTest {
			if b.Result != domain.ResultFail {
				t.Errorf("contract FAIL recorded as %s", b.Result)
			}
			sawFail = true
		}
	}
	if !sawFail {
		t.Error("a failed contract produced no run observation")
	}
}

// A receipt that does not say which packages resolved cannot be attributed to
// any coordinate, and inventing one would be worse than the dash.
func TestAReceiptWithoutResolvedPackagesObservesNothing(t *testing.T) {
	r := passReceipt(t, nil, map[string]string{"resolve": "PASS", "contract": "PASS"})
	if got := ObservationsFromReceipt(r); len(got) != 0 {
		t.Errorf("invented %d observations from a receipt that named no package", len(got))
	}
}

// Every batch must be one the ingest path accepts, or this records nothing at
// all and does it silently.
func TestReceiptObservationsAreAcceptedByIngest(t *testing.T) {
	r := passReceipt(t, []string{"pkg:npm/axios@1.12.0"},
		map[string]string{"resolve": "PASS", "contract": "PASS"})
	store := serverstore.NewFake()
	n, rejected, err := store.IngestBatches(t.Context(), ObservationsFromReceipt(r))
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 {
		t.Fatalf("ingest rejected %d of them: %+v", len(rejected), rejected)
	}
	if n == 0 {
		t.Fatal("ingest accepted none")
	}
}

// Every batch has to survive the SAME validation the server applies, not just
// look well-formed. Measured against production, the project bucket was the
// sample id — "sha256:" plus 64 hex, 71 bytes — and a bucket may be 64, so
// the store refused all 9,883 observations the backfill offered it and
// reported the refusal as a count with no reason attached.
func TestReceiptObservationsPassTheStoresOwnValidation(t *testing.T) {
	r := passReceipt(t, []string{"pkg:npm/axios@1.12.0"},
		map[string]string{"resolve": "PASS", "contract": "PASS"})

	batches := ObservationsFromReceipt(r)
	if len(batches) == 0 {
		t.Fatal("no observations to validate")
	}
	for _, b := range batches {
		if err := serverstore.ValidateBatch(b); err != nil {
			t.Errorf("the store would refuse this batch: %v", err)
		}
	}
}

func TestReceiptObservationsReconstructDependencyAtlas(t *testing.T) {
	// 1. Single-package receipt is a leaf dependency.
	single := passReceipt(t, []string{"pkg:npm/is-number@7.0.0"},
		map[string]string{"resolve": "PASS", "contract": "PASS"})
	singleBatches := ObservationsFromReceipt(single)
	var sawLeaf bool
	for _, b := range singleBatches {
		if b.Stage == domain.StageUsed && b.DependsOnNone {
			sawLeaf = true
		}
	}
	if !sawLeaf {
		t.Fatal("single-package receipt did not produce DependsOnNone=true on StageUsed")
	}

	// 2. Multi-package receipt records edges from primary package.
	multi := passReceipt(t, []string{"pkg:npm/express@4.18.2", "pkg:npm/body-parser@1.20.1", "pkg:npm/cookie@0.5.0"},
		map[string]string{"resolve": "PASS", "contract": "PASS"})
	multiBatches := ObservationsFromReceipt(multi)
	var sawEdge bool
	for _, b := range multiBatches {
		if b.Package == "pkg:npm/express@4.18.2" && b.Stage == domain.StageUsed {
			if len(b.DependsOn) == 2 && b.DependsOn[0] == "pkg:npm/body-parser@1.20.1" && b.DependsOn[1] == "pkg:npm/cookie@0.5.0" {
				sawEdge = true
			}
		}
	}
	if !sawEdge {
		t.Fatal("multi-package receipt did not record children under primary package DependsOn")
	}
}

func TestReceiptObservationsRecordDeclaredSymbols(t *testing.T) {
	r := passReceipt(t, []string{"pkg:npm/axios@1.12.0", "pkg:npm/follow-redirects@1.15.6"},
		map[string]string{"resolve": "PASS", "contract": "PASS"})

	batches := ObservationsFromReceipt(r, "axios.post", "axios.get")
	var postSeen, getSeen bool
	for _, b := range batches {
		if b.Package == "pkg:npm/axios@1.12.0" && b.Stage == domain.StageProjectTest {
			if b.Symbol == "axios.post" && b.SymbolConfidence == domain.SymbolExact && b.Result == domain.ResultPass {
				postSeen = true
			}
			if b.Symbol == "axios.get" && b.SymbolConfidence == domain.SymbolExact && b.Result == domain.ResultPass {
				getSeen = true
			}
		}
	}
	if !postSeen || !getSeen {
		t.Fatalf("expected observation batches for declared symbols: postSeen=%v getSeen=%v", postSeen, getSeen)
	}
}

