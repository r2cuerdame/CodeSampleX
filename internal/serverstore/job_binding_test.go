package serverstore

import (
	"testing"
	"time"
)

func TestSaveReceiptForJobConsumesOnlyTheExactLiveClaim(t *testing.T) {
	store := NewFake()
	jobID, err := store.CreateJob(t.Context(), JobRow{SampleID: "sha256:sample", Reason: "matrix"})
	if err != nil {
		t.Fatal(err)
	}
	peer := "ed25519:0123456789abcdef"
	if ok, err := store.ClaimJob(t.Context(), jobID, peer); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	wrong := ReceiptRow{ReceiptID: "sha256:wrong", SampleID: "sha256:sample", PeerID: "ed25519:fedcba9876543210"}
	if ok, err := store.SaveReceiptForJob(t.Context(), wrong, jobID); err != nil || ok {
		t.Fatalf("wrong peer consumed claim: ok=%v err=%v", ok, err)
	}
	if rows, _ := store.ReceiptsForSample(t.Context(), wrong.SampleID); len(rows) != 0 {
		t.Fatal("wrong-peer receipt was persisted")
	}
	right := ReceiptRow{ReceiptID: "sha256:right", SampleID: "sha256:sample", PeerID: peer}
	if ok, err := store.SaveReceiptForJob(t.Context(), right, jobID); err != nil || !ok {
		t.Fatalf("exact claim was not consumed: ok=%v err=%v", ok, err)
	}
	job, _, _ := store.Job(t.Context(), jobID)
	if job.Status != "done" {
		t.Fatalf("job status = %q, want done", job.Status)
	}
	if ok, err := store.SaveReceiptForJob(t.Context(), ReceiptRow{ReceiptID: "sha256:late", SampleID: right.SampleID, PeerID: peer}, jobID); err != nil || ok {
		t.Fatalf("completed claim accepted another receipt: ok=%v err=%v", ok, err)
	}
	replayJob, err := store.CreateJob(t.Context(), JobRow{
		SampleID: right.SampleID, Reason: "matrix", WantEnvJSON: `{"runtimeVersion":"21"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClaimJob(t.Context(), replayJob, peer); err != nil || !ok {
		t.Fatalf("claim replay target: ok=%v err=%v", ok, err)
	}
	if ok, err := store.SaveReceiptForJob(t.Context(), right, replayJob); err != nil || ok {
		t.Fatalf("one signed receipt consumed a second job: ok=%v err=%v", ok, err)
	}
	replayed, _, _ := store.Job(t.Context(), replayJob)
	if replayed.Status != "claimed" {
		t.Fatalf("replay target status = %q, want claimed", replayed.Status)
	}
}

func TestSignedPassAtomicallyPublishesQuarantinedAuthoringDraft(t *testing.T) {
	store := NewFake()
	const sampleID = "sha256:authoring-draft"
	now := time.Now().UTC()
	work, found, err := store.ClaimAuthoringWork(t.Context(), "writer-a", []WantedRow{{
		Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Asks: 1,
	}}, now, now.Add(24*time.Hour))
	if err != nil || !found {
		t.Fatalf("claim authoring work: %+v %v %v", work, found, err)
	}
	if attached, err := store.AttachAuthoringWorkSample(t.Context(), "writer-a", work, sampleID, now); err != nil || !attached {
		t.Fatalf("attach authoring work: %v %v", attached, err)
	}
	if err := store.SaveAuthoringDraft(t.Context(), AuthoringDraftRow{
		SampleID: sampleID, SessionID: "writer-a", ManifestJSON: `{}`, LocalStatus: "LOCAL_PASS", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSample(t.Context(), SampleRow{
		SampleID: sampleID, ManifestJSON: `{}`, Status: "DRAFT",
		Quarantined: true, QuarantineReason: "awaiting cross verification",
	}); err != nil {
		t.Fatal(err)
	}
	jobID, err := store.CreateJob(t.Context(), JobRow{SampleID: sampleID, Reason: "cross"})
	if err != nil {
		t.Fatal(err)
	}
	const peer = "ed25519:0123456789abcdef"
	if ok, err := store.ClaimJob(t.Context(), jobID, peer); err != nil || !ok {
		t.Fatalf("claim: %v, %v", ok, err)
	}
	if ok, err := store.SaveReceiptForJob(t.Context(), ReceiptRow{
		ReceiptID: "sha256:independent-pass", SampleID: sampleID, PeerID: peer, ContractResult: "PASS",
	}, jobID); err != nil || !ok {
		t.Fatalf("save receipt: %v, %v", ok, err)
	}
	row, ok, err := store.GetSample(t.Context(), sampleID)
	if err != nil || !ok || row.Status != "CROSS_PASS" || row.Quarantined {
		t.Fatalf("promoted sample = %+v, ok=%v err=%v", row, ok, err)
	}
}

func TestFailedCrossReceiptLeavesAuthoringDraftPrivate(t *testing.T) {
	store := NewFake()
	const sampleID = "sha256:failed-authoring-draft"
	_ = store.SaveSample(t.Context(), SampleRow{SampleID: sampleID, ManifestJSON: `{}`, Status: "DRAFT", Quarantined: true})
	jobID, _ := store.CreateJob(t.Context(), JobRow{SampleID: sampleID, Reason: "cross"})
	const peer = "ed25519:fedcba9876543210"
	_, _ = store.ClaimJob(t.Context(), jobID, peer)
	if ok, err := store.SaveReceiptForJob(t.Context(), ReceiptRow{
		ReceiptID: "sha256:independent-fail", SampleID: sampleID, PeerID: peer, ContractResult: "FAIL",
	}, jobID); err != nil || !ok {
		t.Fatalf("save fail receipt: %v, %v", ok, err)
	}
	row, _, _ := store.GetSample(t.Context(), sampleID)
	if row.Status != "DRAFT" || !row.Quarantined {
		t.Fatalf("failed draft escaped quarantine: %+v", row)
	}
}

func TestCrossPassDoesNotPublishAnUnownedQuarantinedDraft(t *testing.T) {
	store := NewFake()
	const sampleID = "sha256:unowned-draft"
	_ = store.SaveSample(t.Context(), SampleRow{SampleID: sampleID, ManifestJSON: `{}`, Status: "DRAFT", Quarantined: true})
	jobID, _ := store.CreateJob(t.Context(), JobRow{SampleID: sampleID, Reason: "cross"})
	const peer = "ed25519:0011223344556677"
	_, _ = store.ClaimJob(t.Context(), jobID, peer)
	if ok, err := store.SaveReceiptForJob(t.Context(), ReceiptRow{
		ReceiptID: "sha256:unowned-pass", SampleID: sampleID, PeerID: peer, ContractResult: "PASS",
	}, jobID); err != nil || !ok {
		t.Fatalf("save receipt: %v, %v", ok, err)
	}
	row, _, _ := store.GetSample(t.Context(), sampleID)
	if row.Status != "DRAFT" || !row.Quarantined {
		t.Fatalf("unowned draft was promoted: %+v", row)
	}
}
