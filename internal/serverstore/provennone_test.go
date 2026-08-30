package serverstore

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func leafBatch() domain.ObservationBatch {
	return domain.ObservationBatch{
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
}

// "We resolved this and it declares nothing" must be recordable, because
// otherwise it is indistinguishable from "nobody has looked".
//
// The dependency axis counts a coordinate as answered only when it appears as
// a PARENT in dependency_edge, and a package with no dependencies can never be
// a parent. So every leaf sits in dependencyUnknown forever and no amount of
// farm work closes it. Measured on production: 490 coordinates appear as a
// child of some resolved tree and never as a parent — a quarter of the 2,076
// open on that axis, permanently stuck.
//
// The fact travels explicitly rather than being inferred from an absent edge.
// Absence is exactly what this store must not read meaning into: an empty
// dependsOn is also what a batch from an ecosystem with no scanner looks like.
func TestAResolutionThatFoundNoDependenciesIsRecorded(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	accepted, rejected, err := f.IngestBatches(ctx, []domain.ObservationBatch{leafBatch()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 {
		t.Fatalf("refused: %+v", rejected)
	}
	if accepted != 1 {
		t.Fatalf("accepted = %d, want 1", accepted)
	}

	none, err := f.DependencyProvenNone(ctx, "npm", "left-pad", "1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if !none {
		t.Error("a resolution that found no dependencies was not recorded, so the coordinate stays an open gap forever")
	}
}

// A batch cannot both list dependencies and claim there are none. One of the
// two is wrong and the store must not have to guess which.
func TestClaimingNoDependenciesWhileListingSomeIsRefused(t *testing.T) {
	b := leafBatch()
	b.DependsOn = []string{"pkg:npm/body-parser@1.20.1"}
	if err := ValidateBatch(b); err == nil {
		t.Error("a batch that lists a dependency and claims to have none was accepted")
	}
}

// A leaf must count as answered on the dependency axis.
//
// Recording the resolution is only half the fix: if the census still reads
// only dependency_edge for a PARENT, the coordinate stays in the open column
// and the 490 leaves measured on production remain permanently stuck.
func TestALeafCountsAsAnsweredOnTheDependencyAxis(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	if err := f.UpsertPackage(ctx, PackageRow{
		PURL: "pkg:npm/left-pad@1.3.0", Ecosystem: "npm", Name: "left-pad",
		Version: "1.3.0", Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.IngestBatches(ctx, []domain.ObservationBatch{leafBatch()}); err != nil {
		t.Fatal(err)
	}

	c, err := f.FarmCompletenessNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	withD := 0
	for state, n := range c.States {
		if len(state) == 3 && state[2] == 'D' {
			withD += n
		}
	}
	if withD == 0 {
		t.Errorf("a resolution that found no dependencies left the coordinate open on the axis: states=%v graph=%d unknown=%d",
			c.States, c.DependencyGraph, c.DependencyUnknown)
	}
}

// The census must report a leaf as proven-none, not as a resolved graph.
//
// The two are different facts and the panel splits them on purpose: "this
// release pulls nothing" was measured, "we know its children" was measured,
// and folding the first into the second says the network holds a graph it
// never recorded.
//
// This is a fake/PostgreSQL parity test because the first version of the fix
// diverged: the fake counted a leaf as DependencyProvenNone while PostgreSQL
// folded it into 'D' and therefore into DependencyGraph, which is the silent
// production bug this store exists to prevent. Only the fake could show it,
// and only if asked.
func TestALeafIsCountedAsProvenNoneNotAsAGraph(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	if err := f.UpsertPackage(ctx, PackageRow{
		PURL: "pkg:npm/left-pad@1.3.0", Ecosystem: "npm", Name: "left-pad",
		Version: "1.3.0", Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.IngestBatches(ctx, []domain.ObservationBatch{leafBatch()}); err != nil {
		t.Fatal(err)
	}

	c, err := f.FarmCompletenessNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.DependencyProvenNone != 1 {
		t.Errorf("dependencyProvenNone = %d, want 1", c.DependencyProvenNone)
	}
	if c.DependencyGraph != 0 {
		t.Errorf("dependencyGraph = %d; a release whose resolution found nothing is not a graph", c.DependencyGraph)
	}
}
