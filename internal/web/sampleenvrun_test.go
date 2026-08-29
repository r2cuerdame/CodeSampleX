package web

import "testing"

// ---------------------------------------------------------------------------
// R2C-127, generation 6: the document's colour must survive the three places
// PR #64's review caught it being dropped.
//
// The sample-state contract (samplestate.go) says a run of ours recorded at a
// coordinate decides the document's colour there. These tests pin the three
// paths that were still losing that outcome:
//
//  1. an environment axis strips verifications before the cell is built, so
//     the very grids that spread WHERE things ran demoted every verified
//     coordinate to grey (cubeview review, P1);
//  2. a failed sample-count lookup overwrote a recorded outcome with "cannot
//     be checked" on the answer card (cubeanswer review, P2);
//  3. the grid tally strip counted observation rates while the cells wear
//     sample outcomes, so a red-document cell could increment the green
//     tally (base.html review, P2).

// envRunFact is one coordinate of an environment grid: observations reported
// by real projects, plus however our own contract came out there.
func envRunFact(os string, agg pivotAgg) cubeFact {
	return cubeFact{
		Dims: map[string]string{
			"symbol": cubePackageLevel, "version": "1.0.0",
			"os": os, "runtime": "go 1.26",
		},
		EnvHash: os,
		Agg:     agg,
	}
}

// A grid spread over WHERE things ran still rates cells from observations
// alone — but the document is the sample-state contract, and a contract of
// ours that passed or failed in THIS cell's environment must keep its colour
// rather than fall to grey.
func TestEnvironmentGridKeepsOurRunOutcomeOnTheDocument(t *testing.T) {
	facts := []cubeFact{
		envRunFact("alpine musl", pivotAgg{obsPass: 3, obsPeers: 1, verPass: 1}),
		envRunFact("windows 11", pivotAgg{obsPass: 2, obsPeers: 1, verFail: 2}),
	}
	grid := buildCubeGrid(facts, "os", "runtime", pivotLinks{}, pivotNow, true)
	if len(grid.Rows) != 1 || len(grid.Rows[0].Cells) != 2 {
		t.Fatalf("grid = %d rows x %d cells, want 1 x 2", len(grid.Rows), len(grid.Rows[0].Cells))
	}
	alpine, windows := grid.Rows[0].Cells[0], grid.Rows[0].Cells[1]
	if alpine.Sample != samplePass {
		t.Errorf("alpine document = %q, want %q — the passing contract run was dropped", alpine.Sample, samplePass)
	}
	if windows.Sample != sampleFail {
		t.Errorf("windows document = %q, want %q — the failing contract run was dropped", windows.Sample, sampleFail)
	}
	// The rate beside the mark stays observation-backed: the run decides the
	// document, never the basis, or the per-platform verdict the environment
	// grids deliberately dropped would be back.
	if alpine.Basis != "observed" {
		t.Errorf("alpine basis = %q, want observed — verification must not rejoin the rate", alpine.Basis)
	}
	// Folding in the published count can only add a document; it must never
	// wash a recorded outcome back to grey. This is the exact path that
	// greyed the mixed version x OS grids.
	alpine.setPublishedSamples(4)
	if alpine.Sample != samplePass {
		t.Errorf("document after setPublishedSamples = %q, want %q", alpine.Sample, samplePass)
	}
}

// The production shape of the pgx/v5 regression: the farm's contract is the
// ONLY evidence at its coordinate — nobody observed a build there — and the
// grid still owes that cell its outcome, not a dash or a grey document.
func TestFarmOnlyCoordinateOnEnvironmentGridShowsItsOutcome(t *testing.T) {
	facts := []cubeFact{
		{
			Dims:    map[string]string{"symbol": "pgx.Batch", "version": "v5.10.0", "os": "ubuntu glibc"},
			EnvHash: "e-ubuntu",
			Agg:     pivotAgg{verFail: 1},
		},
		{
			Dims:    map[string]string{"symbol": "pgx.Batch", "version": "v5.10.0", "os": "windows 11"},
			EnvHash: "e-win",
			Agg:     pivotAgg{obsPass: 5, obsPeers: 2},
		},
	}
	grid := buildCubeGrid(facts, "os", "version", pivotLinks{}, pivotNow, true)
	if len(grid.Rows) != 1 {
		t.Fatalf("grid rows = %d, want 1", len(grid.Rows))
	}
	var ubuntu *pivotCell
	for i := range grid.Cols {
		if grid.Cols[i].Label == "ubuntu glibc" {
			ubuntu = &grid.Rows[0].Cells[i]
		}
	}
	if ubuntu == nil {
		t.Fatalf("no ubuntu glibc column in %+v", grid.Cols)
	}
	if ubuntu.Sample != sampleFail {
		t.Errorf("farm-only document = %q, want %q — the recorded failure has no mark to hang on", ubuntu.Sample, sampleFail)
	}
	if ubuntu.Glyph == "—" {
		t.Error(`farm-only cell renders "—" beside its document: the dash claims nothing was recorded`)
	}
}

// A failed sample-count lookup loses the COUNT, not the outcome: a signed run
// recorded at the coordinate both proves a sample existed and says how it
// went, and deriveSampleState explicitly lets recorded runs outrank the
// published aggregate.
func TestAnswerCardKeepsRunOutcomeWhenSampleCountLookupFails(t *testing.T) {
	coord := map[string]string{"symbol": "pgx.Batch", "version": "v5.10.0", "os": "ubuntu glibc"}
	fact := cubeFact{
		Dims:    coord,
		EnvHash: "e-ubuntu",
		Agg:     pivotAgg{verPass: 2},
	}
	ans := buildCubeAnswer([]cubeFact{fact}, coord, "golang", "github.com/jackc/pgx/v5", "en", pivotNow, nil)
	if ans == nil {
		t.Fatal("no answer card for a coordinate with recorded runs")
	}
	if ans.Sample != samplePass {
		t.Errorf("answer sample = %q, want %q — the recorded outcome was overwritten by the failed lookup", ans.Sample, samplePass)
	}
	if ans.SampleUnknownLabel != "" {
		t.Errorf("answer says %q while a recorded outcome is on the card", ans.SampleUnknownLabel)
	}

	// With no run of ours recorded, the failed read stays what it was: not an
	// absence claim, and not an outcome either.
	fact.Agg = pivotAgg{obsPass: 3, obsPeers: 1}
	ans = buildCubeAnswer([]cubeFact{fact}, coord, "golang", "github.com/jackc/pgx/v5", "en", pivotNow, nil)
	if ans == nil {
		t.Fatal("no answer card for an observed coordinate")
	}
	if ans.SampleUnknownLabel == "" {
		t.Error("a failed count lookup with no recorded run must keep saying the sample cannot be checked")
	}
	if ans.Sample != sampleNone {
		t.Errorf("answer sample = %q, want none — a failed read supports no claim", ans.Sample)
	}
}

// The tally strip wears the same document the cells wear, so it must count
// the cells' sample states. A cell whose reported builds passed but whose
// contract run failed draws a red document — and used to increment the green
// tally, because the counters were read off the observation rate.
func TestGridTalliesCountTheSampleStateTheCellsWear(t *testing.T) {
	aggs := map[cellKey]*pivotAgg{
		{"r", "redDoc"}:   {obsPass: 4, obsPeers: 1, verFail: 1},
		{"r", "observed"}: {obsPass: 3, obsPeers: 1},
		{"r", "greenDoc"}: {verPass: 2},
	}
	g := assembleGrid(aggs, sortPivotRows, sortPivotCols, false, false, pivotLinks{}, pivotNow, "")
	if g.CountFail != 1 {
		t.Errorf("CountFail = %d, want 1 — the red-document cell was tallied off its observation rate", g.CountFail)
	}
	if g.CountPass != 1 {
		t.Errorf("CountPass = %d, want 1 (the verified-pass cell alone), got the red-document cell counted green", g.CountPass)
	}
	if g.CountMixed != 0 {
		t.Errorf("CountMixed = %d, want 0", g.CountMixed)
	}
	if g.CountObserved != 1 {
		t.Errorf("CountObserved = %d, want 1 — cells with reported builds and no run of ours", g.CountObserved)
	}
}
