package serverstore

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A batch carrying dependsOn must become a dependency_edge row in Postgres,
// not only in the fake.
//
// Nothing covered this. edges_test.go exercises the fake's map and the PG
// integration suite never touched dependency_edge at all, so the one write
// that fills the dependency axis had its production implementation untested —
// on a store where a divergence between the fake and PG is treated as a
// silent production bug precisely because it cannot be seen from a green
// suite.
//
// It matters now because verification started sending these: the farm
// resolves a real lockfile for every sample it builds, and this row is where
// that ends up.
func TestIntegrationABatchWithDependsOnWritesAnEdge(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
	}
	batch := domain.ObservationBatch{
		SchemaVersion:    1,
		Epoch:            "2026-08-30",
		AnonID:           "peer-abc",
		ProjectBucket:    "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
		Package:          "pkg:npm/express@4.18.2",
		Environment:      env,
		Stage:            domain.StageUsed,
		Result:           domain.ResultPass,
		ObservationCount: 1,
		Direct:           true,
		DependsOn:        []string{"pkg:npm/body-parser@1.20.1"},
	}

	accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{batch})
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 {
		t.Fatalf("postgres refused the batch: %+v", rejected)
	}
	if accepted != 1 {
		t.Fatalf("accepted = %d, want 1", accepted)
	}

	rows, err := pg.Dependencies(ctx, "npm", "express")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("dependency_edge rows = %d, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.ParentName != "express" || got.ParentVersion != "4.18.2" ||
		got.ChildName != "body-parser" || got.ChildVersion != "1.20.1" {
		t.Errorf("edge = %s@%s -> %s@%s", got.ParentName, got.ParentVersion, got.ChildName, got.ChildVersion)
	}
	if got.Projects != 1 {
		t.Errorf("projects = %d, want 1 — one bucket on one day is one project", got.Projects)
	}

	// The same resolution reported twice is still one project, or the count
	// this table exists to give would inflate with every rebuild.
	if _, _, err := pg.IngestBatches(ctx, []domain.ObservationBatch{batch}); err != nil {
		t.Fatal(err)
	}
	rows, err = pg.Dependencies(ctx, "npm", "express")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Projects != 1 {
		t.Errorf("a repeated resolution changed the edge: %+v", rows)
	}
}

// The same fact in Postgres: a resolution that found nothing must be
// recordable there too, or the axis closes on the fake and stays open in
// production — the exact divergence this store treats as a silent bug.
func TestIntegrationAResolutionThatFoundNoDependenciesIsRecorded(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	batch := domain.ObservationBatch{
		SchemaVersion:    1,
		Epoch:            "2026-08-30",
		AnonID:           "peer-abc",
		ProjectBucket:    "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
		Package:          "pkg:npm/left-pad@1.3.0",
		Environment:      domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64"},
		Stage:            domain.StageUsed,
		Result:           domain.ResultPass,
		ObservationCount: 1,
		DependsOnNone:    true,
	}

	accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{batch})
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 || accepted != 1 {
		t.Fatalf("accepted=%d rejected=%+v", accepted, rejected)
	}

	none, err := pg.DependencyProvenNone(ctx, "npm", "left-pad", "1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if !none {
		t.Error("postgres did not record the resolution, so the coordinate stays an open gap forever")
	}

	// A release nobody resolved must not come back as proven-none: that would
	// turn an unmeasured coordinate into a measured zero.
	other, err := pg.DependencyProvenNone(ctx, "npm", "left-pad", "1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if other {
		t.Error("a release nobody resolved was reported as proven to have no dependencies")
	}
}
