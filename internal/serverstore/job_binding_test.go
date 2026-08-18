package serverstore

import "testing"

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
