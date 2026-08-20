package serverstore

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// One build writes a package-level observation AND one per detected symbol,
// so summing every row counts the same build once for the package and again
// for each symbol found in it. The front page carried that total as
// "measured observations from real machines" — 38% of it was the same builds
// counted a second time.
func TestNetworkCountsDoesNotCountOneBuildTwice(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	env := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm",
		OS: "linux", Arch: "x64", Runtime: "node", RuntimeVersion: "22"}
	base := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "aaaabbbbccccdddd",
		ProjectBucket: "proj", Package: "pkg:npm/lib@1.0.0", Environment: env,
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 10,
	}
	pkgLevel := base
	symA, symB := base, base
	symA.Symbol, symA.SymbolConfidence = "lib.a", domain.SymbolProbable
	symB.Symbol, symB.SymbolConfidence = "lib.b", domain.SymbolProbable
	if accepted, rejected, err := f.IngestBatches(ctx,
		[]domain.ObservationBatch{pkgLevel, symA, symB}); err != nil || accepted != 3 {
		t.Fatalf("ingest: accepted=%d rejected=%v err=%v", accepted, rejected, err)
	}

	counts, err := f.NetworkCounts(ctx, f.now())
	if err != nil {
		t.Fatal(err)
	}
	// Ten builds happened. Thirty is those ten builds plus the two symbols
	// each of them was detected in.
	if counts.Observations != 10 {
		t.Errorf("observations = %d, want 10 — one build is one observation, "+
			"however many symbols were detected in it", counts.Observations)
	}
}
