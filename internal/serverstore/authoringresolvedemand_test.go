package serverstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// R2C-90. A carried sighting weighs 1 against a chosen one's 1000, so a
// coordinate nobody has listed in their own manifest sits at the bottom of the
// package-level branch however many machines actually installed it. Production
// held 2,655 such purls, and 1,810 of them were the coverage holes the panel
// prints.
//
// The signal that separates the ones worth writing is not the sighting count.
// It is the resolved graph: how many distinct project-days had this exact
// release resolved into them. That is a machine having installed it, which is
// stronger than a mention and weaker than somebody choosing it -- and the
// weight says exactly how much weaker.
func TestResolveDemandLiftsACarriedOnlyCoordinateOverAThinlyChosenOne(t *testing.T) {
	f := NewFake()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	// Chosen once by one person, and never resolved into anything.
	seedObservedPackage(t, f, "thinly-chosen", "1.0.0", "windows", 1, true)
	// Never chosen, mentioned three times -- and resolved into twenty
	// distinct project-days, which is the top of production's distribution.
	seedObservedPackage(t, f, "widely-resolved", "1.0.0", "windows", 3, false)
	seedResolveDemand(t, f, "widely-resolved", "1.0.0", 20)

	got := packageLevelOrder(t, f)
	if rankOf(t, got, "widely-resolved") > rankOf(t, got, "thinly-chosen") {
		t.Errorf("package-level order = %v, want widely-resolved before thinly-chosen", got)
	}
}

// The other half of the same rule: lifting carried demand must not bury the
// demand that was there first. A coordinate fifty people listed themselves
// still outranks one twenty machines merely installed.
func TestResolveDemandDoesNotBuryAHighDemandChosenCoordinate(t *testing.T) {
	f := NewFake()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	seedObservedPackage(t, f, "widely-chosen", "1.0.0", "windows", 50, true)
	seedObservedPackage(t, f, "widely-resolved", "1.0.0", "windows", 3, false)
	seedResolveDemand(t, f, "widely-resolved", "1.0.0", 20)

	got := packageLevelOrder(t, f)
	if rankOf(t, got, "widely-chosen") > rankOf(t, got, "widely-resolved") {
		t.Errorf("package-level order = %v, want widely-chosen before widely-resolved", got)
	}
}

// packageLevelOrder is the names of the symbol-less EXPANSION candidates, in
// the order the scheduler would hand them out.
func packageLevelOrder(t *testing.T, f *Fake) []string {
	t.Helper()
	rows, err := f.ListAuthoringExpansionCandidates(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Kind == "EXPANSION" && r.Symbol == "" {
			out = append(out, r.Name)
		}
	}
	return out
}

func rankOf(t *testing.T, order []string, name string) int {
	t.Helper()
	for i, n := range order {
		if n == name {
			return i
		}
	}
	t.Fatalf("%s is not a package-level candidate at all: %v", name, order)
	return -1
}

// seedResolveDemand records that `projects` distinct project-days resolved
// this exact release as a child of a package somebody chose. One parent, many
// projects: the demand is the breadth of the resolution, not of the graph.
func seedResolveDemand(t *testing.T, store expansionStore, name, version string, projects int) {
	t.Helper()
	ctx := context.Background()
	const parent = "pkg:npm/resolving-parent@1.0.0"
	child := "pkg:npm/" + name + "@" + version
	for i := 0; i < projects; i++ {
		if _, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
			SchemaVersion: 1, Epoch: "2026-08-20",
			AnonID:        fmt.Sprintf("anon-resolver-%02d", i),
			ProjectBucket: fmt.Sprintf("proj-resolver-%02d", i),
			Package:       parent, Stage: domain.StageProjectCompile,
			Result: domain.ResultPass, ObservationCount: 1, Direct: true,
			DependsOn: []string{child},
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64",
				Runtime: "node", RuntimeVersion: "22",
			},
		}}); err != nil || len(rejected) != 0 {
			t.Fatalf("ingest resolver %d: rejected=%v err=%v", i, rejected, err)
		}
	}
	if err := store.UpsertPackage(ctx, PackageRow{
		PURL: parent, Ecosystem: "npm", Name: "resolving-parent", Version: "1.0.0",
		Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
}

// The resolve-demand term is a second place the two stores compute the same
// score, and the repo's history is that halves of this query drift. The scores
// themselves are compared here, not just the order, because a weight that
// differs between the stores gives the same order on a two-row fixture and a
// different queue on production's three thousand.
func TestIntegrationResolveDemandScoresMatchInBothStores(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	play := func(t *testing.T, store expansionStore) []string {
		t.Helper()
		seedObservedPackage(t, store, "thinly-chosen", "1.0.0", "windows", 1, true)
		seedObservedPackage(t, store, "widely-resolved", "1.0.0", "windows", 3, false)
		seedResolveDemand(t, store, "widely-resolved", "1.0.0", 20)
		rows, err := store.ListAuthoringExpansionCandidates(context.Background(), 50)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, candidateLine(r))
		}
		return out
	}

	fake := NewFake()
	fake.NowFn = func() time.Time { return now }
	fakeRows := play(t, fake)
	pgRows := play(t, openTestPG(t))

	if len(fakeRows) != len(pgRows) {
		t.Fatalf("row count differs: fake=%d pg=%d\n fake: %v\n pg:   %v",
			len(fakeRows), len(pgRows), fakeRows, pgRows)
	}
	for i := range pgRows {
		if fakeRows[i] != pgRows[i] {
			t.Errorf("row %d differs\n  fake: %s\n  pg:   %s", i, fakeRows[i], pgRows[i])
		}
	}
	// 3 carried sightings + 20 resolved project-days at authoringResolveWeight.
	want := "widely-resolved@1.0.0/(package) EXPANSION/SAMPLE score=2003 os=windows"
	for _, row := range pgRows {
		if row == want {
			return
		}
	}
	t.Errorf("neither store scored the resolved coordinate as %q: %v", want, pgRows)
}
