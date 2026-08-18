package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestAuthoringWorkLeasesWantedWithoutDuplicateWriters(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	candidates := []WantedRow{
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Asks: 9},
		{Ecosystem: "pypi", Name: "pandas", Version: "3.0.5", Symbol: "pandas.merge", Asks: 4},
	}
	first, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok || first.Name != "axios" {
		t.Fatalf("first claim = %+v ok=%v err=%v", first, ok, err)
	}
	again, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", candidates, now.Add(time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || again.Name != first.Name || !again.ClaimedAt.Equal(first.ClaimedAt) {
		t.Fatalf("same writer claim changed = %+v ok=%v err=%v", again, ok, err)
	}
	second, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-b", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok || second.Name != "pandas" {
		t.Fatalf("duplicate target assigned = %+v ok=%v err=%v", second, ok, err)
	}
}

func TestAuthoringExpansionRanksFailureThenObservedCoverage(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }
	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", AnonID: "workerbucket", ProjectBucket: "projectbucket",
		Package: "pkg:npm/axios@1.12.0", Symbol: "axios.post", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64", Runtime: "node", RuntimeVersion: "22.18"},
		Stage:       domain.StageProjectCompile, Result: domain.ResultFail, ErrorFingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ErrorCode: "ERR_TEST", ObservationCount: 17,
	}
	if accepted, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{batch}); err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	secondary := batch
	secondary.AnonID = "otherworker"
	secondary.ProjectBucket = "otherproject"
	secondary.Symbol = "axios.get"
	secondary.Result = domain.ResultPass
	secondary.ErrorFingerprint = ""
	secondary.ErrorCode = ""
	secondary.ObservationCount = 5
	if accepted, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{secondary}); err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("secondary ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	if err := store.UpsertPackage(ctx, PackageRow{PURL: batch.Package, Ecosystem: "npm", Name: "axios", Version: "1.12.0", Publicness: "PUBLIC"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFailureCluster(ctx, ClusterRow{
		Ecosystem: "npm", PackageName: "axios", Symbol: "axios.post", Stage: "PROJECT_COMPILE",
		ErrorFingerprint: batch.ErrorFingerprint, ErrorCode: batch.ErrorCode, ObservationCount: 17,
		VersionsJSON: `["1.12.0"]`,
	}); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates = %+v err=%v", candidates, err)
	}
	if candidates[0].Kind != "FINDING" || candidates[0].Symbol != "axios.post" || candidates[0].Score != 17 {
		t.Fatalf("first candidate = %+v", candidates[0])
	}
	if candidates[1].Kind != "EXPANSION" || candidates[1].Symbol != "axios.get" || candidates[1].Score != 5 {
		t.Fatalf("second candidate = %+v", candidates[1])
	}

	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-finding", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok || work.Kind != "FINDING" || work.Score != 17 {
		t.Fatalf("claim = %+v ok=%v err=%v", work, ok, err)
	}
	remaining, err := store.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil || len(remaining) != 1 || remaining[0].Kind != "EXPANSION" || remaining[0].Symbol != "axios.get" {
		t.Fatalf("remaining = %+v err=%v", remaining, err)
	}
}

func TestFailedCrossReleasesWantedForAnotherSampleWriter(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	candidates := []WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Asks: 9}}
	work, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("claim = %+v %v %v", work, ok, err)
	}
	const sampleID = "sha256:wanted-attempt"
	if attached, err := store.AttachAuthoringWorkSample(t.Context(), "writer-a", work, sampleID, now.Add(time.Hour)); err != nil || !attached {
		t.Fatalf("attach = %v %v", attached, err)
	}
	_ = store.SaveSample(t.Context(), SampleRow{SampleID: sampleID, ManifestJSON: `{}`, Status: "DRAFT", Quarantined: true})
	jobID, _ := store.CreateJob(t.Context(), JobRow{SampleID: sampleID, Reason: "cross"})
	const peer = "ed25519:0123456789abcdef"
	_, _ = store.ClaimJob(t.Context(), jobID, peer)
	if accepted, err := store.SaveReceiptForJob(t.Context(), ReceiptRow{
		ReceiptID: "sha256:failed-wanted-attempt", SampleID: sampleID, PeerID: peer, ContractResult: "FAIL",
	}, jobID); err != nil || !accepted {
		t.Fatalf("fail receipt = %v %v", accepted, err)
	}
	retry, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-b", candidates, now.Add(2*time.Hour), now.Add(26*time.Hour))
	if err != nil || !ok || retry.Name != "axios" {
		t.Fatalf("failed target was not released = %+v %v %v", retry, ok, err)
	}
}
