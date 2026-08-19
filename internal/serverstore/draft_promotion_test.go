package serverstore

import (
	"testing"
)

// A draft is released by the verification that passed, not by whether the
// session that wrote it is still open.
//
// Promotion used to require a live authoring assignment as well. An
// authoring session expires an hour after its last refresh and its
// assignment is deleted with it, so a draft whose cross verification
// landed after that window kept its signed PASS receipt and stayed
// quarantined forever — verified, and invisible to everyone. In
// production that had stranded 446 samples.
func TestDraftPromotesOnPassEvenAfterItsAuthoringSessionExpired(t *testing.T) {
	store := NewFake()
	ctx := t.Context()
	const sampleID = "sha256:draft"
	const peer = "ed25519:0123456789abcdef"

	if err := store.SaveSample(ctx, SampleRow{
		SampleID: sampleID, Status: "DRAFT", Quarantined: true,
		QuarantineReason: "private authoring draft awaiting cross verification",
	}); err != nil {
		t.Fatal(err)
	}
	// The draft row survives; the assignment does not — exactly the state
	// an expired authoring session leaves behind.
	if err := store.SaveAuthoringDraft(ctx, AuthoringDraftRow{SampleID: sampleID}); err != nil {
		t.Fatal(err)
	}
	jobID, err := store.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "cross"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClaimJob(ctx, jobID, peer); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	ok, err := store.SaveReceiptForJob(ctx, ReceiptRow{
		ReceiptID: "sha256:receipt", SampleID: sampleID, PeerID: peer,
		ContractResult: "PASS",
	}, jobID)
	if err != nil || !ok {
		t.Fatalf("receipt not accepted: ok=%v err=%v", ok, err)
	}

	row, found, err := store.GetSample(ctx, sampleID)
	if err != nil || !found {
		t.Fatalf("sample lookup: found=%v err=%v", found, err)
	}
	if row.Quarantined || row.Status != "CROSS_PASS" {
		t.Fatalf("sample = %q quarantined=%v, want CROSS_PASS and public",
			row.Status, row.Quarantined)
	}
}

// An anonymous upload is not authoring output and must not be promoted by
// this path, however its contract went.
func TestNonDraftUploadIsNotPromotedByCrossPass(t *testing.T) {
	store := NewFake()
	ctx := t.Context()
	const sampleID = "sha256:anon"
	const peer = "ed25519:0123456789abcdef"

	if err := store.SaveSample(ctx, SampleRow{
		SampleID: sampleID, Status: "DRAFT", Quarantined: true,
	}); err != nil {
		t.Fatal(err)
	}
	jobID, err := store.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "cross"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClaimJob(ctx, jobID, peer); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if ok, err := store.SaveReceiptForJob(ctx, ReceiptRow{
		ReceiptID: "sha256:receipt", SampleID: sampleID, PeerID: peer,
		ContractResult: "PASS",
	}, jobID); err != nil || !ok {
		t.Fatalf("receipt not accepted: ok=%v err=%v", ok, err)
	}
	row, _, err := store.GetSample(ctx, sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.Quarantined {
		t.Error("a sample with no authoring draft row was published by cross pass")
	}
}
