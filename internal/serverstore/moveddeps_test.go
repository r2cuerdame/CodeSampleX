package serverstore

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// R2C-139. The home page led with inventory counters — observations, samples,
// packages. Those say how big this network is, which is a fact about the
// network; a visitor is asking what it found out. A dependency that moved
// under a release is the shortest true answer the corpus can give.
//
// Measured on production: 453 (parent, child) pairs across 174 packages
// resolve to different child versions at different releases of the parent.
func TestMovedDependenciesFindOnlyChildrenThatActuallyMoved(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	seedEdge(t, f, "app", "1.0.0", "debug", "4.4.1")
	seedEdge(t, f, "app", "2.0.0", "debug", "4.4.3")
	// Same child version at both releases: not a move.
	seedEdge(t, f, "app", "1.0.0", "cookie", "0.7.2")
	seedEdge(t, f, "app", "2.0.0", "cookie", "0.7.2")
	// One release only: nothing to compare against.
	seedEdge(t, f, "solo", "1.0.0", "left-pad", "1.3.0")

	rows, err := f.MovedDependencies(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d moved pairs, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.ParentName != "app" || got.ChildName != "debug" {
		t.Errorf("moved pair = %s -> %s, want app -> debug", got.ParentName, got.ChildName)
	}
	if got.Versions != 2 || got.Releases != 2 {
		t.Errorf("counts = %d versions across %d releases, want 2 and 2", got.Versions, got.Releases)
	}
}

// The limit is honoured and the order is stable, so the home page shows the
// same few pairs between requests rather than reshuffling under the reader.
func TestMovedDependenciesAreBoundedAndStable(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	for _, p := range []string{"a", "b", "c"} {
		seedEdge(t, f, p, "1.0.0", "child", "1.0.0")
		seedEdge(t, f, p, "2.0.0", "child", "2.0.0")
	}
	first, err := f.MovedDependencies(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("limit ignored: got %d", len(first))
	}
	second, _ := f.MovedDependencies(ctx, 2)
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("row %d moved between calls: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// seedEdge records one resolved parent/child pair through the ingest path.
func seedEdge(t *testing.T, store completenessStore, parent, parentVersion, child, childVersion string) {
	t.Helper()
	if _, rejected, err := store.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-31",
		AnonID:        "anon-" + parent + parentVersion + child,
		ProjectBucket: "proj-" + parent + parentVersion + child,
		Package:       "pkg:npm/" + parent + "@" + parentVersion,
		Stage:         domain.StageProjectCompile, Result: domain.ResultPass,
		ObservationCount: 1, Direct: true,
		DependsOn: []string{"pkg:npm/" + child + "@" + childVersion},
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22",
		},
	}}); err != nil || len(rejected) != 0 {
		t.Fatalf("seed edge %s@%s -> %s: rejected=%v err=%v", parent, parentVersion, child, rejected, err)
	}
}

var _ = context.Background

// Both stores answer the same, including the order the home page renders in.
func TestIntegrationMovedDependenciesParity(t *testing.T) {
	pg := openTestPG(t)
	f := NewFake()
	for _, store := range []completenessStore{pg, f} {
		seedEdge(t, store, "app", "1.0.0", "debug", "4.4.1")
		seedEdge(t, store, "app", "2.0.0", "debug", "4.4.3")
		seedEdge(t, store, "app", "3.0.0", "debug", "4.5.0")
		seedEdge(t, store, "app", "1.0.0", "cookie", "0.7.2")
		seedEdge(t, store, "app", "2.0.0", "cookie", "0.7.2")
		seedEdge(t, store, "other", "1.0.0", "left-pad", "1.3.0")
		seedEdge(t, store, "other", "2.0.0", "left-pad", "1.3.1")
	}
	pgRows, err := pg.MovedDependencies(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	fakeRows, err := f.MovedDependencies(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pgRows) != len(fakeRows) {
		t.Fatalf("rows: pg=%d fake=%d\npg=%+v\nfake=%+v", len(pgRows), len(fakeRows), pgRows, fakeRows)
	}
	for i := range pgRows {
		if pgRows[i] != fakeRows[i] {
			t.Errorf("row %d differs:\n pg  =%+v\n fake=%+v", i, pgRows[i], fakeRows[i])
		}
	}
	// The most-moved child leads, which is what makes the strip worth reading.
	if len(pgRows) == 0 || pgRows[0].ChildName != "debug" || pgRows[0].Versions != 3 {
		t.Errorf("first row = %+v, want debug at three versions", pgRows)
	}
}
