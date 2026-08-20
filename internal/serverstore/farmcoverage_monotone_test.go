package serverstore

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func seedObservation(t *testing.T, f *Fake, name, eco, os string) string {
	t.Helper()
	ctx := context.Background()
	purl := "pkg:" + eco + "/" + name + "@1.0.0"
	if accepted, _, err := f.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "peer" + name, ProjectBucket: "proj" + name,
		Package: purl, Symbol: name + ".Call", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: eco, OS: os,
			Arch: "amd64", Runtime: "go", RuntimeVersion: "1.26"},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 3,
	}}); err != nil || accepted != 1 {
		t.Fatalf("ingest %s: %v", name, err)
	}
	return purl
}

func seedProof(t *testing.T, f *Fake, name, eco, os, result string) string {
	t.Helper()
	ctx := context.Background()
	purl := "pkg:" + eco + "/" + name + "@1.0.0"
	if err := f.UpsertPackage(ctx, PackageRow{PURL: purl, Ecosystem: eco,
		Name: name, Version: "1.0.0", Publicness: "PUBLIC"}); err != nil {
		t.Fatal(err)
	}
	id := "sha256:proof" + name + os
	if err := f.SaveSample(ctx, SampleRow{SampleID: id,
		ManifestJSON: `{"packages":["` + purl + `"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{ReceiptID: "r" + name + os, SampleID: id,
		ContractResult: result,
		ReceiptJSON:    `{"environment":{"os":"` + os + `"}}`}); err != nil {
		t.Fatal(err)
	}
	return purl
}

func cellFor(t *testing.T, f *Fake, os, eco string) FarmAxisCoverage {
	t.Helper()
	rows, err := f.FarmCoverage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.OS == os && r.Ecosystem == eco {
			return r
		}
	}
	return FarmAxisCoverage{}
}

// Adding data must never lower a coverage number. The first implementation
// counted every proof when a cell had no observations and only the
// intersection once it had one, so a single unrelated observation could drop
// the cell from five proofs to one. A coverage figure that falls when the
// network learns something is worse than no figure.
func TestFarmCoverageIsMonotoneInTheData(t *testing.T) {
	f := NewFake()
	for _, n := range []string{"a", "b", "c"} {
		seedProof(t, f, n, "golang", "linux", "PASS")
	}
	before := cellFor(t, f, "linux", "golang")
	if before.Proven != 3 {
		t.Fatalf("proven before = %d, want 3 (%+v)", before.Proven, before)
	}

	// An observation about a package nobody proved must not remove proofs.
	seedObservation(t, f, "unrelated", "golang", "linux")
	if err := f.UpsertPackage(context.Background(), PackageRow{
		PURL: "pkg:golang/unrelated@1.0.0", Ecosystem: "golang",
		Name: "unrelated", Version: "1.0.0", Publicness: "PUBLIC"}); err != nil {
		t.Fatal(err)
	}
	after := cellFor(t, f, "linux", "golang")
	if after.Proven < before.Proven {
		t.Errorf("proven fell from %d to %d when an observation was added", before.Proven, after.Proven)
	}
}

// "We ran it here and it failed" is a measurement of this platform, and the
// most interesting one the fleet produces. Counting only PASS made it
// indistinguishable from "we never ran here", which is the exact collapse
// this panel exists to prevent.
func TestFarmCoverageSeparatesMeasuredFromProven(t *testing.T) {
	f := NewFake()
	seedProof(t, f, "works", "golang", "windows", "PASS")
	seedProof(t, f, "broken", "golang", "windows", "FAIL")

	cell := cellFor(t, f, "windows", "golang")
	if cell.Measured != 2 {
		t.Errorf("measured = %d, want 2 — a FAIL receipt is still a measurement", cell.Measured)
	}
	if cell.Proven != 1 {
		t.Errorf("proven = %d, want 1", cell.Proven)
	}
}

// The manifest version is author input; the resolver claim is what actually
// installed. pg.go:2331-2340 already settled this for answering a wanted row
// — coverage must not credit a release the run never resolved, or the map
// reports proof for a version nobody ever ran.
func TestFarmCoverageCreditsWhatResolvedNotWhatWasClaimed(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	claimed := "pkg:golang/example.com/mod@1.0.0"
	actual := "pkg:golang/example.com/mod@1.0.1"
	for _, purl := range []string{claimed, actual} {
		if err := f.UpsertPackage(ctx, PackageRow{PURL: purl, Ecosystem: "golang",
			Name: "example.com/mod", Version: purl[len(purl)-5:], Publicness: "PUBLIC"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.SaveSample(ctx, SampleRow{SampleID: "sha256:matrix",
		ManifestJSON: `{"packages":["` + claimed + `"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{ReceiptID: "r-matrix", SampleID: "sha256:matrix",
		ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS"},` +
			`"resolvedPackages":["` + actual + `"],"environment":{"os":"linux"}}`}); err != nil {
		t.Fatal(err)
	}

	cell := cellFor(t, f, "linux", "golang")
	if cell.Proven != 1 {
		t.Fatalf("proven = %d, want 1 (%+v)", cell.Proven, cell)
	}
	rows, err := f.FarmCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = rows
	// The credited purl must be the resolved one. Prove it by removing the
	// resolved package from the registry: the cell must then be empty.
	f2 := NewFake()
	if err := f2.UpsertPackage(ctx, PackageRow{PURL: claimed, Ecosystem: "golang",
		Name: "example.com/mod", Version: "1.0.0", Publicness: "PUBLIC"}); err != nil {
		t.Fatal(err)
	}
	if err := f2.SaveSample(ctx, SampleRow{SampleID: "sha256:matrix",
		ManifestJSON: `{"packages":["` + claimed + `"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := f2.SaveReceipt(ctx, ReceiptRow{ReceiptID: "r-matrix", SampleID: "sha256:matrix",
		ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS"},` +
			`"resolvedPackages":["` + actual + `"],"environment":{"os":"linux"}}`}); err != nil {
		t.Fatal(err)
	}
	if got := cellFor(t, f2, "linux", "golang"); got.Proven != 0 {
		t.Errorf("proven = %d, want 0 — credit went to the manifest's claim, not the resolved release", got.Proven)
	}
}
