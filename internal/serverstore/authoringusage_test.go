package serverstore

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func seedUsage(t *testing.T, f *Fake, name, symbol, os string, count int) {
	t.Helper()
	ctx := context.Background()
	purl := "pkg:npm/" + name + "@1.0.0"
	if _, _, err := f.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "peer" + name + symbol,
		ProjectBucket: "proj" + name, Package: purl, Symbol: symbol,
		SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm",
			OS: os, Arch: "x64", Runtime: "node", RuntimeVersion: "22"},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: count,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := f.UpsertPackage(ctx, PackageRow{PURL: purl, Ecosystem: "npm",
		Name: name, Version: "1.0.0", Publicness: "PUBLIC"}); err != nil {
		t.Fatal(err)
	}
}

// Sample authoring should follow what people actually use. Observation volume
// was already the score, but it ranked fourth — behind a hardcoded
// linux-before-windows bias and the branch a candidate happened to come from.
// The bias is the worse half: every observation this network holds is
// recorded on Windows, so ordering Linux first pushes the entire measured
// demand to the back of the queue.
func TestAuthoringWorkFollowsUsage(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	seedUsage(t, f, "rare", "rare.call", "linux", 3)
	seedUsage(t, f, "popular", "popular.call", "windows", 900)

	candidates, err := f.ListAuthoringExpansionCandidates(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) < 2 {
		t.Fatalf("candidates = %+v, want both packages", candidates)
	}
	if candidates[0].Name != "popular" {
		t.Errorf("first candidate = %q (os %q); the most-used coordinate must lead",
			candidates[0].Name, candidates[0].TargetOS)
	}
}
