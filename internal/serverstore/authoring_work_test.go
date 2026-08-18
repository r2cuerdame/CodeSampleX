package serverstore

import (
	"testing"
	"time"
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
