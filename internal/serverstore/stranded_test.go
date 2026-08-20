package serverstore

import (
	"testing"
	"time"
)

// A quarantined authoring draft whose only cross job was closed by a
// verifier that never ran the contract has no future event to wake it: the
// job is done, no receipt passed, and nothing queues another. Production
// held 159 of them. StrandedDrafts is how they are found again.
func TestStrandedDraftsFindsDraftsWithNoWorkLeft(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Now().UTC()

	seed := func(id, status string, quarantined bool) {
		if err := f.SaveSample(ctx, SampleRow{
			SampleID: id, ManifestJSON: `{}`, Status: status, License: "MIT-0",
			CreatedAt: now, Quarantined: quarantined,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Stranded: a closed cross job, a receipt that measured nothing.
	seed("sha256:stranded", "DRAFT", true)
	jobID, err := f.CreateJob(ctx, JobRow{SampleID: "sha256:stranded", Reason: "cross", Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ClaimJob(ctx, jobID, "ed25519:aaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SaveReceiptForJob(ctx, ReceiptRow{
		ReceiptID: "r1", SampleID: "sha256:stranded", PeerID: "ed25519:aaaaaaaaaaaaaaaa",
		ReceiptJSON: `{}`, ContractResult: "SKIPPED",
	}, jobID); err != nil {
		t.Fatal(err)
	}
	// Still waiting on an open job — not stranded, just queued.
	seed("sha256:queued", "DRAFT", true)
	if _, err := f.CreateJob(ctx, JobRow{SampleID: "sha256:queued", Reason: "cross", Status: "open"}); err != nil {
		t.Fatal(err)
	}
	// Already verified — nothing to do.
	seed("sha256:done", "CROSS_PASS", false)

	got, err := f.StrandedDrafts(ctx, 4, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "sha256:stranded" {
		t.Fatalf("stranded = %v, want [sha256:stranded]", got)
	}
}

// The cap is what stops a sample nothing can build from being requeued
// forever, so the finder must respect it too — otherwise the reconcile
// undoes the bound on every boot.
func TestStrandedDraftsRespectsTheAttemptCap(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	if err := f.SaveSample(ctx, SampleRow{
		SampleID: "sha256:tired", ManifestJSON: `{}`, Status: "DRAFT",
		License: "MIT-0", CreatedAt: time.Now().UTC(), Quarantined: true,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		id, err := f.CreateJob(ctx, JobRow{SampleID: "sha256:tired", Reason: "cross", Status: "done"})
		if err != nil {
			t.Fatal(err)
		}
		_ = id
	}
	got, err := f.StrandedDrafts(ctx, 4, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("stranded = %v, want none: the sample has used its attempts", got)
	}
}
