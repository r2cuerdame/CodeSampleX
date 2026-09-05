package compatibility

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func seedProvenCoordinate(t *testing.T, store *serverstore.Fake, ecosystem, name, version string) string {
	t.Helper()
	ctx := context.Background()
	purl := domain.PURL{Ecosystem: ecosystem, Name: name, Version: version}.String()
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL: purl, Ecosystem: ecosystem, Name: name, Version: version,
		Major: version[:1], Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
	id := "sha256:proof-" + name + "-" + version
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: id, ManifestJSON: `{"packages":["` + purl + `"],"symbols":[]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
		SampleID: id, ReceiptID: "receipt-" + name + "-" + version,
		PeerID: "peer-" + name, EnvHash: "env-" + name, ContractResult: "PASS",
		ReceiptJSON: `{"environment":{"os":"linux"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// The dependency axis becomes a verification the fleet already knows how to
// run. A new job REASON would have been invisible: crossclient.go skips any
// reason it does not recognise, so the work would have sat in the table until
// every client in the field upgraded.
func TestTheBuilderOpensVerificationWorkForAnOpenDependencyAxis(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	sampleID := seedProvenCoordinate(t, store, "npm", "left-pad", "1.3.0")

	b := &Builder{Store: store}
	if err := b.createDependencyAxisJobs(ctx); err != nil {
		t.Fatal(err)
	}

	jobs, err := store.JobsForSample(ctx, sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1: a coordinate with a sample and no tree got no verification", len(jobs))
	}
	if jobs[0].Reason != "cross" || jobs[0].Status != "open" {
		t.Errorf("job = %+v, want an open cross job every released verifier can claim", jobs[0])
	}
}

// A pass that runs again must not open the question twice. EnsureCrossJob
// reuses live work for the sample under an advisory lock, which is what makes
// two builder passes overlapping during a deploy safe.
func TestASecondBuilderPassDoesNotDuplicateDependencyAxisWork(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	sampleID := seedProvenCoordinate(t, store, "npm", "left-pad", "1.3.0")

	b := &Builder{Store: store}
	for i := 0; i < 3; i++ {
		if err := b.createDependencyAxisJobs(ctx); err != nil {
			t.Fatal(err)
		}
	}

	jobs, err := store.JobsForSample(ctx, sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d after three passes, want 1: the scheduler re-asks a live question", len(jobs))
	}
}

// A coordinate whose tree has arrived is done. The pass must take it off the
// board rather than keep spending verifiers on an answer it already holds.
func TestAnAnsweredDependencyAxisOpensNoFurtherWork(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	seedProvenCoordinate(t, store, "npm", "left-pad", "1.3.0")
	if _, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-09-01", AnonID: "peer-report",
		ProjectBucket: "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
		Package:       "pkg:npm/left-pad@1.3.0",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		},
		Stage: domain.StageUsed, Result: domain.ResultPass, ObservationCount: 1,
		DependsOn: []string{"pkg:npm/tiny@1.0.0"},
	}}); err != nil || len(rejected) != 0 {
		t.Fatalf("ingest: rejected=%v err=%v", rejected, err)
	}

	b := &Builder{Store: store}
	if err := b.createDependencyAxisJobs(ctx); err != nil {
		t.Fatal(err)
	}

	jobs, err := store.JobsForSample(ctx, "sha256:proof-left-pad-1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %d, want 0: the tree is already reported and the question is closed", len(jobs))
	}
}
