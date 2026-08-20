package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Package-level expansion excluded coordinates already proven, keyed by OS.
// The key never matched.
//
// A work row's target_os is where the package was OBSERVED — every observation
// this network holds is from Windows. The exclusion's target_os is where the
// contract RAN — every proof it holds is from Linux. Comparing them asks
// "linux = windows", so a package verified minutes ago was offered again, and
// again: production wrote eight samples for three@0.185.1 in twenty-eight
// minutes, each with a placeholder goal and no symbols.
//
// The guard was written for exactly this failure and its comment describes it
// ("offered for linux again, forever ... 201 verified samples for one
// coordinate"). It only ever worked while the two platforms agreed, and in
// this network they never do.
func TestPackageLevelWorkStopsOnceTheCoordinateIsProvenAnywhere(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Now().UTC()
	const purl = "pkg:npm/three@0.185.1"

	if err := f.UpsertPackage(ctx, PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "three", Version: "0.185.1",
		Publicness: "PUBLIC", LastSeen: now,
	}); err != nil {
		t.Fatal(err)
	}
	// Observed on Windows, which is where every observation comes from.
	if _, _, err := f.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "anon-1", ProjectBucket: "proj-1",
		Package: purl, Stage: domain.StageProjectTest, Result: domain.ResultPass,
		ObservationCount: 40,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64",
			Runtime: "node", RuntimeVersion: "22",
		},
	}}); err != nil {
		t.Fatal(err)
	}
	before := countExpansionWork(t, f, purl)
	if before == 0 {
		t.Fatal("an observed, unproven package produced no expansion work")
	}

	// Proven — on Linux, which is where every proof comes from.
	seedVerifiedSample(t, f, ctx, purl, "linux", now)

	if after := countExpansionWork(t, f, purl); after != 0 {
		t.Errorf("package-level work = %d after the coordinate was proven, want 0", after)
	}
}

func countExpansionWork(t *testing.T, f *Fake, purl string) int {
	t.Helper()
	rows, err := f.ListAuthoringExpansionCandidates(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Kind == "EXPANSION" && r.Symbol == "" &&
			"pkg:"+r.Ecosystem+"/"+r.Name+"@"+r.Version == purl {
			n++
		}
	}
	return n
}

// seedVerifiedSample publishes a sample covering purl with one passing
// receipt from the given OS — the shape the verifier fleet actually produces.
func seedVerifiedSample(t *testing.T, f *Fake, ctx context.Context, purl, os string, now time.Time) {
	t.Helper()
	sampleID := "sha256:" + purl
	manifest := `{"packages":["` + purl + `"],"case":{"goal":"verify ` + purl + `"}}`
	if err := f.SaveSample(ctx, SampleRow{
		SampleID: sampleID, ManifestJSON: manifest, Status: "CROSS_PASS",
		License: "MIT-0", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "r-" + purl, SampleID: sampleID, PeerID: "ed25519:1111111111111111",
		ContractResult: "PASS",
		ReceiptJSON:    `{"environment":{"os":"` + os + `"}}`,
	}); err != nil {
		t.Fatal(err)
	}
}

// A FINDING is about a failure, not about coverage. Excluding symbol-less
// coordinates that a verified sample already covers was right for expansion —
// expansion asks "is this answered" — and wrong for everything else: 77% of
// production's failure clusters carry no symbol, and a package having some
// verified sample says nothing about whether its failure has been explained.
// Scoping the exclusion to expansion is the difference between stopping a
// loop and stopping the work.
func TestSymbollessFindingSurvivesAnAlreadyVerifiedPackage(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Now().UTC()
	const purl = "pkg:npm/three@0.185.1"

	if err := f.UpsertPackage(ctx, PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "three", Version: "0.185.1",
		Publicness: "PUBLIC", LastSeen: now,
	}); err != nil {
		t.Fatal(err)
	}
	seedVerifiedSample(t, f, ctx, purl, "linux", now)
	if err := f.UpsertFailureCluster(ctx, ClusterRow{
		Ecosystem: "npm", PackageName: "three", Symbol: "",
		Stage: "PROJECT_TEST", ErrorFingerprint: "fp-1", ErrorCode: "ERR_X",
		ObservationCount: 12, EnvSummaryJSON: `{"os":"windows"}`,
		VersionsJSON: `["0.185.1"]`,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := f.ListAuthoringExpansionCandidates(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Kind == "FINDING" && r.Symbol == "" && r.Name == "three" {
			return
		}
	}
	t.Errorf("the symbol-less finding was excluded as though it were coverage: %+v", rows)
}
